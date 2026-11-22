package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/provider"
)

// giv extend ed edit con la tabella vera di xAI: rifiuti, --dry-run e --max-cost
// prima di qualsiasi richiesta (xAI finto che conta le chiamate).

func xaiEnv(t *testing.T, dur string) *int32 {
	t.Helper()
	clipEnv(t, dur) // cartella, ffprobe finto, clip.mp4
	clipOpTables["xai"] = caps.XAIOps
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "stop", http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	old := baseURLs["xai"]
	baseURLs["xai"] = srv.URL
	t.Cleanup(func() { baseURLs["xai"] = old })
	t.Setenv("XAI_API_KEY", "fake")
	return &hits
}

func runOp(t *testing.T, fn func([]string) error, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := fn(args)
	w.Close()
	os.Stdout = old
	b := make([]byte, 1<<16)
	n, _ := r.Read(b)
	return string(b[:n]), err
}

func TestXAIRifiutiCodice2SenzaRichieste(t *testing.T) {
	hits := xaiEnv(t, "5.0")
	os.WriteFile("foto.png", []byte("\x89PNG\r\n\x1a\n"), 0o644)
	big, _ := os.Create("big.mp4")
	big.Write(mp4Head)
	big.Truncate(provider.MaxVideoBytes + 1)
	big.Close()
	for name, c := range map[string]struct {
		fn   func([]string) error
		args []string
	}{
		"extend inesistente":  {cmdExtend, []string{"--video", "no.mp4", "p"}},
		"extend non mp4":      {cmdExtend, []string{"--video", "foto.png", "p"}},
		"extend oltre 50MiB":  {cmdExtend, []string{"--video", "big.mp4", "p"}},
		"edit inesistente":    {cmdEdit, []string{"--video", "no.mp4", "p"}},
		"edit non mp4":        {cmdEdit, []string{"--video", "foto.png", "p"}},
		"edit oltre 50MiB":    {cmdEdit, []string{"--video", "big.mp4", "p"}},
		"edit con --duration": {cmdEdit, []string{"--video", "clip.mp4", "--duration", "5", "p"}},
	} {
		_, err := runOp(t, c.fn, c.args...)
		if err == nil {
			t.Errorf("%s: atteso rifiuto", name)
		} else if code, _ := exitCode(err); code != 2 {
			t.Errorf("%s: code=%d (%v)", name, code, err)
		}
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("%d richieste a xAI, attese 0", n)
	}
}

func TestXAIDryRunStimaCasoPiuCaro(t *testing.T) {
	hits := xaiEnv(t, "5.0")
	out, err := runOp(t, cmdExtend, "--dry-run", "--video", "clip.mp4", "--duration", "6", "p")
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Cost *float64 `json:"cost_est_usd"`
		Cmd  string   `json:"command"`
	}
	json.Unmarshal([]byte(out), &p)
	if p.Cmd != "extend" || p.Cost == nil || *p.Cost != 0.82 { // (5+6)*0.07 + 5*0.01
		t.Errorf("extend piano=%s", out)
	}
	out, err = runOp(t, cmdEdit, "--dry-run", "--video", "clip.mp4", "p")
	p.Cost = nil
	json.Unmarshal([]byte(out), &p)
	if err != nil || p.Cost == nil || *p.Cost != 0.4 { // 5*0.08
		t.Errorf("edit piano=%s err=%v", out, err)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("dry-run: %d richieste", n)
	}
}

func TestXAIMaxCostSopraStimaRifiutato(t *testing.T) {
	hits := xaiEnv(t, "5.0")
	_, err := runOp(t, cmdExtend, "--video", "clip.mp4", "--duration", "6", "--max-cost", "0.81", "p")
	if err == nil {
		t.Fatal("atteso rifiuto")
	}
	if code, _ := exitCode(err); code != 2 {
		t.Errorf("code=%d (%v)", code, err)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("%d richieste, attese 0 (nessun avvio)", n)
	}
	if _, err := runOp(t, cmdEdit, "--dry-run", "--video", "clip.mp4", "--max-cost", "0.40", "p"); err != nil {
		t.Errorf("tetto uguale alla stima deve passare: %v", err)
	}
}
