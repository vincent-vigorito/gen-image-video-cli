package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gen-image-video-cli/internal/provider/fal"
)

// CLI-914 (O-T3): provider fal contro un server finto (TLS, mai fal vero).

const falKey = "fal-test-key-0123456789"

// falStore è un host di storage di fal (ammesso dal filtro di upload_url); il
// trasporto finto lo rimanda al secondo server.
const falStore = "https://v3.fal.media"

type falRewrite struct {
	rt  http.RoundTripper
	cdn string
}

func (f falRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host == "v3.fal.media" {
		r = r.Clone(r.Context())
		r.URL.Host = f.cdn
	}
	return f.rt.RoundTrip(r)
}

const falEP = "/fal-ai/kling-video/v3/pro/image-to-video"

type falReq struct {
	Method, Path, Auth, Key string
	Body                    []byte
	Header                  http.Header
}

type falEnv struct {
	mu     sync.Mutex
	reqs   []falReq // server fal
	other  []falReq // secondo host (scaricamento, upload)
	srv    *httptest.Server
	cdn    *httptest.Server
	status int
	// initStatus != 0: initiate risponde con questo codice; badHost: upload_url su un host non fal
	initStatus int
	badHost    bool
}

func (e *falEnv) rec(dst *[]falReq, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	*dst = append(*dst, falReq{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Key"), b, r.Header.Clone()})
	e.mu.Unlock()
}

func (e *falEnv) count(method, prefix string) (n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.reqs {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return
}

func newFalEnv(t *testing.T) *falEnv {
	t.Helper()
	e := &falEnv{}
	e.cdn = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.rec(&e.other, r)
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "video/mp4")
			w.Write([]byte("FAKEMP4-BYTES"))
		}
	}))
	polls := 0
	e.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.rec(&e.reqs, r)
		switch {
		case r.URL.Path == "/storage/upload/initiate" && e.initStatus != 0:
			http.Error(w, "boom", e.initStatus)
		case r.URL.Path == "/storage/upload/initiate" && e.badHost:
			json.NewEncoder(w).Encode(map[string]string{"upload_url": e.cdn.URL + "/put/1", "file_url": falStore + "/files/1.png"})
		case r.URL.Path == "/storage/upload/initiate":
			n := e.count("POST", "/storage/upload/initiate")
			json.NewEncoder(w).Encode(map[string]string{
				"upload_url": falStore + "/put/" + string(rune('0'+n)),
				"file_url":   falStore + "/files/" + string(rune('0'+n)) + ".png",
			})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/fal-ai/") || r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/bytedance/"):
			json.NewEncoder(w).Encode(map[string]string{"request_id": "req-1",
				"status_url": e.srv.URL + r.URL.Path + "/requests/req-1/status"})
		case strings.HasSuffix(r.URL.Path, "/status"):
			polls++
			st := "IN_PROGRESS"
			if polls > 1 {
				st = "COMPLETED"
			}
			json.NewEncoder(w).Encode(map[string]string{"status": st})
		case strings.Contains(r.URL.Path, "/requests/req-1"):
			json.NewEncoder(w).Encode(map[string]any{"video": map[string]string{"url": e.cdn.URL + "/dl/v.mp4"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(e.srv.Close)
	t.Cleanup(e.cdn.Close)
	old := falConfig
	falConfig = fal.Config{Queue: e.srv.URL, REST: e.srv.URL, Transport: falRewrite{e.srv.Client().Transport, e.cdn.Listener.Addr().String()}, PollInterval: time.Millisecond}
	t.Cleanup(func() { falConfig = old })
	t.Setenv("FAL_KEY", falKey)
	t.Chdir(t.TempDir())
	return e
}

func falPNG(t *testing.T, name string) string {
	p := filepath.Join(t.TempDir(), name)
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	os.WriteFile(p, buf.Bytes(), 0o644)
	return p
}

func falVideo(t *testing.T, args ...string) (out, errOut string, code int) {
	t.Helper()
	o, e, c := cutArgsVideo(args)
	return o, e, c
}

func cutArgsVideo(args []string) (out, errOut string, code int) {
	capture := func(f **os.File) func() string {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		return func() string { w.Close(); *f = old; return <-done }
	}
	so, se := capture(&os.Stdout), capture(&os.Stderr)
	err := cmdVideo(append([]string{"--provider", "fal"}, args...))
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

// casi 1 e 3: avvio in coda, attesa, scaricamento, manifest con la stima; la
// chiave va solo a fal, mai all'host dello scaricamento.
func TestFalVideoCodaScaricamentoEChiave(t *testing.T) {
	e := newFalEnv(t)
	img := falPNG(t, "a.png")
	out, se, code := falVideo(t, "--image", img, "--duration", "5", "--out", "o", "muovi piano")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, se)
	}
	var man map[string]any
	if err := json.Unmarshal([]byte(out), &man); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if man["provider"] != "fal" {
		t.Errorf("provider %v", man["provider"])
	}
	if c, _ := man["cost_est_usd"].(float64); c < 0.83 || c > 0.85 { // 5 s x 0,168 (audio on di default)
		t.Errorf("cost_est_usd %v (atteso 0.84)", man["cost_est_usd"])
	}
	if src, _ := man["cost_source"].(string); !strings.Contains(src, "fal") {
		t.Errorf("cost_source %q", src)
	}
	if _, ok := man["cost_usd"]; ok {
		t.Errorf("cost_usd presente: %v", man["cost_usd"])
	}
	if n := e.count("POST", falEP); n != 1 {
		t.Errorf("avvii %d (atteso 1)", n)
	}
	files, _ := filepath.Glob("o/*.mp4")
	if len(files) != 1 {
		t.Fatalf("file scaricati %v", files)
	}
	if b, _ := os.ReadFile(files[0]); string(b) != "FAKEMP4-BYTES" {
		t.Errorf("contenuto %q", b)
	}
	// la chiave: solo verso fal, con Key; l'altro host non la vede da nessuna parte
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.reqs {
		if r.Auth != "Key "+falKey {
			t.Errorf("fal %s %s: Authorization %q", r.Method, r.Path, r.Auth)
		}
	}
	if len(e.other) == 0 {
		t.Fatal("nessuna richiesta all'host del file")
	}
	for _, r := range e.other {
		for k, v := range r.Header {
			if strings.Contains(strings.Join(v, " "), falKey) {
				t.Errorf("chiave verso l'altro host (%s %s) nell'intestazione %s", r.Method, r.Path, k)
			}
		}
		if strings.Contains(string(r.Body), falKey) || strings.Contains(r.Path, falKey) {
			t.Errorf("chiave verso l'altro host nel corpo o nel percorso")
		}
	}
	if strings.Contains(out+se, falKey) {
		t.Errorf("la chiave compare nell'output")
	}
}

// caso 2: file locale = caricamento in due passi, poi avvio con l'indirizzo ottenuto.
func TestFalVideoFileLocaleDueCaricamenti(t *testing.T) {
	e := newFalEnv(t)
	img := falPNG(t, "a.png")
	if _, se, code := falVideo(t, "--image", img, "--duration", "5", "--out", "o", "x"); code != 0 {
		t.Fatalf("code %d\n%s", code, se)
	}
	if n := e.count("POST", "/storage/upload/initiate"); n != 1 {
		t.Fatalf("initiate %d", n)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var put *falReq
	for i := range e.other {
		if e.other[i].Method == http.MethodPut {
			put = &e.other[i]
		}
	}
	if put == nil || put.Auth != "" || len(put.Body) == 0 || put.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("PUT: %+v", put)
	}
	var start map[string]any
	for _, r := range e.reqs {
		if r.Method == http.MethodPost && r.Path == falEP {
			json.Unmarshal(r.Body, &start)
		}
	}
	if u, _ := start["start_image_url"].(string); !strings.HasPrefix(u, falStore+"/files/") {
		t.Errorf("start_image_url %v (atteso l'indirizzo ottenuto dal caricamento)", start["start_image_url"])
	}
	if strings.Contains(string(falJSON(start)), img) {
		t.Errorf("il percorso locale è finito nel corpo")
	}
}

func falJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// caso 4: senza chiave errore chiaro e nessuna richiesta.
func TestFalSenzaChiave(t *testing.T) {
	e := newFalEnv(t)
	t.Setenv("FAL_KEY", "")
	img := falPNG(t, "a.png")
	_, se, code := falVideo(t, "--image", img, "--duration", "5", "x")
	if code == 0 || !strings.Contains(se, "FAL_KEY") {
		t.Errorf("code %d msg %q", code, se)
	}
	if len(e.reqs)+len(e.other) != 0 {
		t.Errorf("richieste senza chiave: %d", len(e.reqs)+len(e.other))
	}
}

// caso 5: dry-run stima e niente richieste (nemmeno il caricamento).
func TestFalDryRun(t *testing.T) {
	e := newFalEnv(t)
	img := falPNG(t, "a.png")
	out, se, code := falVideo(t, "--dry-run", "--image", img, "--duration", "10", "--audio", "off", "x")
	if code != 0 {
		t.Fatalf("code %d %s", code, se)
	}
	var p struct {
		Cost *float64 `json:"cost_est_usd"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil || p.Cost == nil || *p.Cost < 1.39 || *p.Cost > 1.41 {
		t.Errorf("stima %v (atteso 1.4): %s", p.Cost, out)
	}
	if len(e.reqs)+len(e.other) != 0 {
		t.Errorf("richieste in dry-run: %d", len(e.reqs)+len(e.other))
	}
}

// caso 6: stima oltre --max-cost, codice 2, nessun avvio.
func TestFalMaxCost(t *testing.T) {
	e := newFalEnv(t)
	img := falPNG(t, "a.png")
	_, se, code := falVideo(t, "--image", img, "--duration", "10", "--max-cost", "0.5", "x")
	if code != 2 {
		t.Errorf("code %d (atteso 2): %s", code, se)
	}
	if len(e.reqs)+len(e.other) != 0 {
		t.Errorf("richieste: %d", len(e.reqs)+len(e.other))
	}
}
