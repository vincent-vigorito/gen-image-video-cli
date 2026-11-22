package main

import (
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
)

// O-T6 (CLI-919): giv upscale in-process contro un OpenRouter finto.

type upSrv struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   int
	posts  []map[string]any
	fail   int  // status del POST, 0 = ok
	noCost bool // il poll non riporta usage.cost: nel manifest resta la stima
}

func upEnv(t *testing.T) *upSrv {
	t.Helper()
	u := &upSrv{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		defer u.mu.Unlock()
		u.reqs++
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/videos":
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			json.Unmarshal(b, &m)
			u.posts = append(u.posts, m)
			if u.fail != 0 {
				http.Error(w, `{"error":{"message":"no"}}`, u.fail)
				return
			}
			fmt.Fprintf(w, `{"id":"job-up","polling_url":%q,"status":"pending"}`, u.srv.URL+"/videos/job-up")
		case r.URL.Path == "/videos/job-up":
			usage := `,"usage":{"cost":0.0123}`
			if u.noCost {
				usage = ""
			}
			fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q]%s}`, u.srv.URL+"/videos/job-up/content", usage)
		case strings.HasSuffix(r.URL.Path, "/content"):
			w.Write([]byte("UPSCALED"))
		}
	}))
	t.Cleanup(u.srv.Close)
	old := baseURLs["openrouter"]
	baseURLs["openrouter"] = u.srv.URL
	t.Cleanup(func() { baseURLs["openrouter"] = old })
	t.Setenv("OPENROUTER_API_KEY", "fake")
	t.Chdir(t.TempDir())
	return u
}

func upGiv(t *testing.T, args ...string) (out, errOut string, code int) {
	t.Helper()
	capture := func(f **os.File) func() string {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		return func() string { w.Close(); *f = old; return <-done }
	}
	so, se := capture(&os.Stdout), capture(&os.Stderr)
	err := cmdUpscale(args)
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

func upClip(t *testing.T) string {
	t.Helper()
	cutNeedFF(t)
	p := filepath.Join(t.TempDir(), "in.mp4")
	cutClip(t, p, "320x240", 10, "1")
	return p
}

func upJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("stdout non è JSON (%v): %q", err, s)
	}
	return m
}

// caso 1: da URL https: avvio, attesa, risultato scaricato, manifest.
func TestUpscaleCmdURL(t *testing.T) {
	u := upEnv(t)
	out, se, code := upGiv(t, "--video", "https://example.com/in.mp4", "--out", "o", "--name", "up")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if len(u.posts) != 1 {
		t.Fatalf("POST=%d", len(u.posts))
	}
	m := upJSON(t, out)
	if m["command"] != "upscale" || m["provider"] != "openrouter" || m["cost_usd"] != 0.0123 || m["job_id"] != "job-up" {
		t.Errorf("manifest: %v", m)
	}
	if ents, _ := os.ReadDir("o"); len(ents) == 0 {
		t.Error("nessun file scaricato in o/")
	}
	if log, _ := os.ReadFile("giv-jobs.jsonl"); !strings.Contains(string(log), "job-up") {
		t.Errorf("job non registrato: %q", log)
	}
}

// O-T11: giv upscale con openrouter e --video locale: codice 2, nessuna richiesta, le due vie nel messaggio.
func TestUpscaleCmdFileLocaleRifiutato(t *testing.T) {
	u := upEnv(t)
	p := upClip(t)
	for _, extra := range [][]string{nil, {"--dry-run"}, {"--max-cost", "1"}, {"--provider", "openrouter"}} {
		_, se, code := upGiv(t, append([]string{"--video", p}, extra...)...)
		if code != 2 {
			t.Fatalf("%v: code=%d %s", extra, code, se)
		}
		if !strings.Contains(se, "https") || !strings.Contains(se, "--provider fal") {
			t.Errorf("%v: messaggio senza le due vie: %s", extra, se)
		}
	}
	if u.reqs != 0 {
		t.Errorf("richieste al provider: %d", u.reqs)
	}
}

// caso 4 e 5 (rifiuti): codice 2 e nessuna richiesta.
func TestUpscaleCmdRifiuti(t *testing.T) {
	u := upEnv(t)
	txt := filepath.Join(t.TempDir(), "x.txt")
	os.WriteFile(txt, []byte("non un mp4"), 0o600)
	cases := map[string][]string{
		"file inesistente":    {"--video", filepath.Join(t.TempDir(), "no.mp4")},
		"non mp4":             {"--video", txt},
		"http":                {"--video", "http://example.com/in.mp4"},
		"senza video":         {},
		"fattore sopra":       {"--video", "https://example.com/in.mp4", "--factor", "4"},
		"fattore sotto":       {"--video", "https://example.com/in.mp4", "--factor", "1.2"},
		"mode sconosciuto":    {"--video", "https://example.com/in.mp4", "--mode", "x"},
		"duration":            {"--video", "https://example.com/in.mp4", "--duration", "5"},
		"provider xai":        {"--video", "https://example.com/in.mp4", "--provider", "xai"},
		"modello sconosciuto": {"--video", "https://example.com/in.mp4", "--model", "altro/modello"},
		"tetto non stimabile": {"--video", "https://example.com/in.mp4", "--max-cost", "5"},
	}
	for name, args := range cases {
		_, se, code := upGiv(t, args...)
		if code != 2 {
			t.Errorf("%s: code=%d %s", name, code, se)
		}
	}
	if u.reqs != 0 {
		t.Errorf("richieste al provider: %d", u.reqs)
	}
	if _, se, _ := upGiv(t, "--video", "https://example.com/in.mp4", "--max-cost", "5"); !strings.Contains(se, "--max-cost") {
		t.Error("messaggio del tetto senza --max-cost")
	}
}

// caso 5: --dry-run su openrouter (solo URL: nessuna misura, nessuna stima) e tetto senza stima.
func TestUpscaleCmdDryRun(t *testing.T) {
	u := upEnv(t)
	out, _, code := upGiv(t, "--video", "https://example.com/in.mp4", "--dry-run")
	if code != 0 || upJSON(t, out)["dry_run"] != true || upJSON(t, out)["cost_est_usd"] != nil {
		t.Errorf("dry-run URL: code=%d %s", code, out)
	}
	if _, _, code := upGiv(t, "--video", "https://example.com/in.mp4", "--dry-run", "--max-cost", "1"); code != 2 {
		t.Errorf("dry-run URL con tetto: code=%d", code)
	}
	if u.reqs != 0 {
		t.Errorf("richieste al provider: %d", u.reqs)
	}
}
