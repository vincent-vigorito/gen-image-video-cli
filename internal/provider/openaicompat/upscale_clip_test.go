package openaicompat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gen-image-video-cli/internal/provider"
)

// O-T6 (CLI-919): giv upscale via OpenRouter contro un OpenRouter finto.

var fakeMP4 = append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), []byte("-corpo-della-clip-")...)

type upFake struct {
	srv, cdn *httptest.Server
	mu       sync.Mutex
	body     map[string]any
	posts    int
	auth     map[string]string // percorso -> Authorization visto
	postAuth string
	postErr  int    // se !=0 il POST risponde con questo status
	jobState string // completed | failed
}

func newUpFake(t *testing.T) *upFake {
	t.Helper()
	fastPoll(t)
	f := &upFake{auth: map[string]string{}, jobState: "completed"}
	f.cdn = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth["cdn"+r.URL.Path] = r.Header.Get("Authorization")
		f.mu.Unlock()
		w.Write([]byte("UPSCALED"))
	}))
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth["api"+r.URL.Path] = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/videos":
			f.posts++
			f.postAuth = r.Header.Get("Authorization")
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &f.body)
			if f.postErr != 0 {
				http.Error(w, `{"error":{"message":"bad input"}}`, f.postErr)
				return
			}
			fmt.Fprintf(w, `{"id":"job-up","polling_url":%q,"status":"pending"}`, f.srv.URL+"/videos/job-up")
		case r.URL.Path == "/videos/job-up":
			if f.jobState == "failed" {
				fmt.Fprint(w, `{"status":"failed","error":"bad input"}`)
				return
			}
			fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q],"usage":{"cost":0.4321}}`, f.cdn.URL+"/out.mp4")
		default:
			t.Errorf("richiesta inattesa %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(f.srv.Close)
	t.Cleanup(f.cdn.Close)
	old := clipTransport
	clipTransport = f.cdn.Client().Transport
	t.Cleanup(func() { clipTransport = old })
	return f
}

func upReq(video string) provider.ClipRequest {
	return provider.ClipRequest{Op: "upscale", Model: "black-forest-labs/flux-video-upscale", Video: video, Factor: 2}
}

func upRun(f *upFake, req provider.ClipRequest) (*provider.Result, error) {
	c := New("openrouter", f.srv.URL, "chiave-finta", Options{Router: true})
	return c.GenerateClip(context.Background(), req)
}

func refURL(t *testing.T, body map[string]any) string {
	t.Helper()
	refs, _ := body["input_references"].([]any)
	if len(refs) != 1 {
		t.Fatalf("input_references: %v", body["input_references"])
	}
	r := refs[0].(map[string]any)
	if r["type"] != "video_url" {
		t.Errorf("type=%v", r["type"])
	}
	return r["video_url"].(map[string]any)["url"].(string)
}

// caso 1: URL https passato com'è, attesa, risultato scaricato, costo reale.
func TestUpscaleRouterURL(t *testing.T) {
	f := newUpFake(t)
	const u = "https://example.com/in.mp4"
	req := upReq(u)
	req.Mode, req.Prompt = "creative", "nitido"
	res, err := upRun(f, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := refURL(t, f.body); got != u {
		t.Errorf("url=%q", got)
	}
	if f.body["model"] != "black-forest-labs/flux-video-upscale" || f.body["upscale_factor"] != 2.0 ||
		f.body["creativity"] != 1.0 || f.body["prompt"] != "nitido" {
		t.Errorf("corpo: %v", f.body)
	}
	if f.posts != 1 {
		t.Errorf("POST=%d", f.posts)
	}
	if len(res.Media) != 1 || string(res.Media[0].Data) != "UPSCALED" || res.CostUSD != 0.4321 || res.JobID != "job-up" {
		t.Errorf("risultato: %+v", res)
	}
}

// precise = creativity 0; senza prompt il campo manca.
func TestUpscaleRouterPreciseSenzaPrompt(t *testing.T) {
	f := newUpFake(t)
	if _, err := upRun(f, upReq("https://example.com/in.mp4")); err != nil {
		t.Fatal(err)
	}
	if f.body["creativity"] != 0.0 {
		t.Errorf("creativity=%v", f.body["creativity"])
	}
	if _, ok := f.body["prompt"]; ok {
		t.Errorf("prompt presente: %v", f.body)
	}
}

// caso 2: file locale come data URI col contenuto del file.
func TestUpscaleRouterFileLocaleDataURI(t *testing.T) {
	f := newUpFake(t)
	p := filepath.Join(t.TempDir(), "in.mp4")
	os.WriteFile(p, fakeMP4, 0o600)
	if _, err := upRun(f, upReq(p)); err != nil {
		t.Fatal(err)
	}
	u := refURL(t, f.body)
	b64, ok := strings.CutPrefix(u, "data:video/mp4;base64,")
	if !ok {
		t.Fatalf("non è un data URI mp4: %.40s", u)
	}
	if raw, err := base64.StdEncoding.DecodeString(b64); err != nil || string(raw) != string(fakeMP4) {
		t.Errorf("contenuto: %v", err)
	}
}

// caso 3: il provider rifiuta il data URI -> errore che chiede un URL https.
func TestUpscaleRouterDataURIRifiutato(t *testing.T) {
	p := filepath.Join(t.TempDir(), "in.mp4")
	os.WriteFile(p, fakeMP4, 0o600)
	for _, st := range []int{400, 413, 415, 422} {
		f := newUpFake(t)
		f.postErr = st
		_, err := upRun(f, upReq(p))
		if err == nil || !strings.Contains(err.Error(), "URL https") || f.posts != 1 {
			t.Errorf("status %d: err=%v posts=%d", st, err, f.posts)
		}
	}
	// job fallito dopo l'avvio con file locale: stesso suggerimento
	f := newUpFake(t)
	f.jobState = "failed"
	if _, err := upRun(f, upReq(p)); err == nil || !strings.Contains(err.Error(), "URL https") {
		t.Errorf("job failed locale: %v", err)
	}
	// con URL https nessun suggerimento
	f = newUpFake(t)
	f.postErr = 400
	if _, err := upRun(f, upReq("https://example.com/in.mp4")); err == nil || strings.Contains(err.Error(), "data URI") {
		t.Errorf("URL: %v", err)
	}
}

// caso 6: la chiave va solo all'host di OpenRouter (API), mai alla CDN del risultato.
func TestUpscaleRouterChiaveSoloAlHostApi(t *testing.T) {
	f := newUpFake(t)
	if _, err := upRun(f, upReq("https://example.com/in.mp4")); err != nil {
		t.Fatal(err)
	}
	if f.postAuth != "Bearer chiave-finta" || f.auth["api/videos/job-up"] != "Bearer chiave-finta" {
		t.Errorf("API senza chiave: %q %q", f.postAuth, f.auth["api/videos/job-up"])
	}
	if a, ok := f.auth["cdn/out.mp4"]; !ok || a != "" {
		t.Errorf("la CDN ha visto Authorization %q (visitata=%v)", a, ok)
	}
}

// caso 6 bis: un polling_url su un altro host viene rifiutato prima di mandargli la chiave.
func TestUpscaleRouterPollingAltroHost(t *testing.T) {
	fastPoll(t)
	var evil int
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evil++
		if r.Header.Get("Authorization") != "" {
			t.Errorf("chiave inviata a host estraneo")
		}
	}))
	defer cdn.Close()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"id":"job-up","polling_url":%q,"status":"pending"}`, cdn.URL+"/videos/job-up")
	}))
	defer srv.Close()
	c := New("openrouter", srv.URL, "chiave-finta", Options{Router: true})
	if _, err := c.GenerateClip(context.Background(), upReq("https://example.com/in.mp4")); err == nil {
		t.Fatal("atteso rifiuto")
	}
	if evil != 0 {
		t.Errorf("host estraneo contattato %d volte", evil)
	}
}
