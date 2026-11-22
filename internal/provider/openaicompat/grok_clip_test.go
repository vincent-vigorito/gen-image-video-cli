package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gen-image-video-cli/internal/provider"
)

// xAI finto per giv extend e giv edit: /files, /videos/extensions, /videos/edits,
// GET /videos/{id} e un secondo server (la CDN) per il video finito.
type fakeXAI struct {
	mu        sync.Mutex
	api, cdn  *httptest.Server
	paths     []string
	bodies    map[string]map[string]any
	fileField []string // nomi dei campi del multipart, nell'ordine
	apiAuth   []string // Authorization visto dall'API
	cdnAuth   []string // Authorization visto dalla CDN
}

func newFakeXAI(t *testing.T) *fakeXAI {
	f := &fakeXAI{bodies: map[string]map[string]any{}}
	f.cdn = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.cdnAuth = append(f.cdnAuth, r.Header.Get("Authorization"))
		f.mu.Unlock()
		w.Write([]byte("MP4RISULTATO"))
	}))
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.Method+" "+r.URL.Path)
		f.apiAuth = append(f.apiAuth, r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/files":
			_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			mr := multipart.NewReader(r.Body, params["boundary"])
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				f.fileField = append(f.fileField, p.FormName())
				io.Copy(io.Discard, p)
			}
			fmt.Fprint(w, `{"id":"file_abc-123"}`)
		case r.Method == http.MethodPost:
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			f.bodies[r.URL.Path] = b
			fmt.Fprint(w, `{"request_id":"req-1"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/videos/req-1":
			fmt.Fprintf(w, `{"status":"done","video":{"url":%q},"usage":{"cost_in_usd_ticks":4200000000}}`, f.cdn.URL+"/v.mp4")
		default:
			t.Errorf("richiesta inattesa %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	// la CDN è https: gli scaricamenti fuori origine passano dal suo trasporto
	old := resultTransport
	resultTransport = f.cdn.Client().Transport
	t.Cleanup(func() { resultTransport = old; f.api.Close(); f.cdn.Close() })
	return f
}

func mp4File(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "clip.mp4")
	head := []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2', 0, 0, 0, 0}
	if err := os.WriteFile(p, head, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGrokExtendFileLocale(t *testing.T) {
	fastPoll(t)
	f := newFakeXAI(t)
	c := New("xai", f.api.URL, "chiave-finta", Options{Grok: true})
	var started []string
	old := provider.OnJobStart
	provider.OnJobStart = func(p string, j provider.Job, _ provider.VideoRequest) { started = append(started, p+":"+j.ID) }
	t.Cleanup(func() { provider.OnJobStart = old })

	res, err := c.GenerateClip(context.Background(), provider.ClipRequest{
		Op: "extend", Model: "grok-imagine-video", Prompt: "continua", Video: mp4File(t), Duration: 5})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /files", "POST /videos/extensions", "GET /videos/req-1"}
	if strings.Join(f.paths, ",") != strings.Join(want, ",") {
		t.Errorf("richieste=%v", f.paths)
	}
	if len(f.fileField) != 2 || f.fileField[0] != "expires_after" || f.fileField[1] != "file" {
		t.Errorf("campi del multipart=%v (expires_after prima del file)", f.fileField)
	}
	b := f.bodies["/videos/extensions"]
	vid, _ := b["video"].(map[string]any)
	if b["model"] != "grok-imagine-video" || b["prompt"] != "continua" || b["duration"] != float64(5) || vid["file_id"] != "file_abc-123" {
		t.Errorf("corpo=%v", b)
	}
	if string(res.Media[0].Data) != "MP4RISULTATO" || res.JobID != "req-1" {
		t.Errorf("risultato=%+v", res)
	}
	if res.CostUSD < 0.419 || res.CostUSD > 0.421 {
		t.Errorf("costo=%v, atteso 0.42 (ticks/1e10)", res.CostUSD)
	}
	if len(started) != 1 || started[0] != "xai:req-1" {
		t.Errorf("OnJobStart=%v", started)
	}
	// la chiave va solo all'API, mai alla CDN del risultato
	for _, a := range f.apiAuth {
		if a != "Bearer chiave-finta" {
			t.Errorf("Authorization verso l'API=%q", a)
		}
	}
	if len(f.cdnAuth) != 1 || f.cdnAuth[0] != "" {
		t.Errorf("Authorization verso la CDN=%q, attesa assente", f.cdnAuth)
	}
}

func TestGrokEditFileLocaleSenzaDurata(t *testing.T) {
	fastPoll(t)
	f := newFakeXAI(t)
	c := New("xai", f.api.URL, "k", Options{Grok: true})
	res, err := c.GenerateClip(context.Background(), provider.ClipRequest{
		Op: "edit", Model: "grok-imagine-video", Prompt: "aggiungi pioggia", Video: mp4File(t)})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /files", "POST /videos/edits", "GET /videos/req-1"}
	if strings.Join(f.paths, ",") != strings.Join(want, ",") {
		t.Errorf("richieste=%v", f.paths)
	}
	b := f.bodies["/videos/edits"]
	if _, has := b["duration"]; has || b["prompt"] != "aggiungi pioggia" {
		t.Errorf("corpo edit=%v (niente duration)", b)
	}
	if res.CostUSD <= 0 {
		t.Errorf("costo=%v", res.CostUSD)
	}
}

func TestGrokClipURLHttpsSenzaUpload(t *testing.T) {
	fastPoll(t)
	f := newFakeXAI(t)
	c := New("xai", f.api.URL, "k", Options{Grok: true})
	if _, err := c.GenerateClip(context.Background(), provider.ClipRequest{
		Op: "edit", Model: "grok-imagine-video", Prompt: "p", Video: "https://example.test/a.mp4"}); err != nil {
		t.Fatal(err)
	}
	if f.paths[0] != "POST /videos/edits" {
		t.Errorf("richieste=%v (nessun upload per gli URL)", f.paths)
	}
	vid, _ := f.bodies["/videos/edits"]["video"].(map[string]any)
	if vid["url"] != "https://example.test/a.mp4" {
		t.Errorf("video=%v", vid)
	}
}

// giv video su xai resta com'è: il costo riportato dal GET non entra nel risultato.
func TestGrokVideoSenzaCostoReale(t *testing.T) {
	fastPoll(t)
	f := newFakeXAI(t)
	// il finto risponde a /videos/generations come alle altre POST
	c := New("xai", f.api.URL, "k", Options{Grok: true})
	res, err := c.GenerateVideo(context.Background(), provider.VideoRequest{Model: "grok-imagine-video", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if res.CostUSD != 0 {
		t.Errorf("giv video su xai: costo=%v, atteso 0 (invariato)", res.CostUSD)
	}
	b := f.bodies["/videos/generations"]
	if b["model"] != "grok-imagine-video" || b["prompt"] != "p" {
		t.Errorf("corpo=%v", b)
	}
	for k := range b {
		if k != "model" && k != "prompt" && k != "duration" && k != "aspect_ratio" && k != "resolution" {
			t.Errorf("campo inatteso %q nel corpo di giv video", k)
		}
	}
}

// un 307 sull'avvio non si segue e non si ripete: una sola richiesta.
func TestGrokClipRedirectAvvioNonSeguito(t *testing.T) {
	fastPoll(t)
	var hits int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, srv.URL+"/altro", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c := New("xai", srv.URL, "k", Options{Grok: true})
	_, err := c.GenerateClip(context.Background(), provider.ClipRequest{
		Op: "edit", Model: "grok-imagine-video", Prompt: "p", Video: "https://example.test/a.mp4"})
	if err == nil || hits != 1 {
		t.Errorf("err=%v richieste=%d, attesa 1 e un errore", err, hits)
	}
}
