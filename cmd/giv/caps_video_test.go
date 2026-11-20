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
	"sync/atomic"
	"testing"
)

// F1-T2: capacita' e prezzi prima di spendere. OpenRouter finto con il catalogo
// salvato; ogni richiesta diversa da GET /videos/models e' un avvio di job.
func capsServer(t *testing.T) (starts *int32) {
	t.Helper()
	cat, err := os.ReadFile("testdata/or-video-models.json")
	if err != nil {
		t.Fatal(err)
	}
	starts = new(int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/videos/models") {
			w.Header().Set("Content-Type", "application/json")
			w.Write(cat)
			return
		}
		io.Copy(io.Discard, r.Body)
		atomic.AddInt32(starts, 1)
		http.Error(w, "avvio non atteso", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	old := baseURLs["openrouter"]
	baseURLs["openrouter"] = srv.URL
	t.Cleanup(func() { baseURLs["openrouter"] = old })
	t.Setenv("OPENROUTER_API_KEY", "fake")
	return starts
}

// video lancia cmdVideo in-process e ne cattura stdout e stderr.
func video(t *testing.T, args ...string) (out, errOut string, code int) {
	t.Helper()
	capture := func(f **os.File) (restore func() string) {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		return func() string { w.Close(); *f = old; return <-done }
	}
	so, se := capture(&os.Stdout), capture(&os.Stderr)
	err := cmdVideo(append([]string{"--provider", "openrouter"}, args...))
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

func dryCost(t *testing.T, args ...string) float64 {
	t.Helper()
	out, se, code := video(t, append([]string{"--dry-run"}, args...)...)
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	var p struct {
		CostEstUSD *float64 `json:"cost_est_usd"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil || p.CostEstUSD == nil {
		t.Fatalf("piano senza stima: %q %v", out, err)
	}
	return *p.CostEstUSD
}

func TestDryRunCostEstimate(t *testing.T) {
	starts := capsServer(t)
	got := dryCost(t, "--model", "kwaivgi/kling-v3.0-pro", "--duration", "5", "--audio", "off", "x")
	if got < 0.555 || got > 0.565 {
		t.Errorf("kling 5s muto: %v want 0.56", got)
	}
	got = dryCost(t, "--model", "google/gemini-omni-1.2-flash", "--duration", "5", "--resolution", "720p", "x")
	if got < 0.345 || got > 0.355 {
		t.Errorf("omni 5s 720p: %v want 0.35", got)
	}
	if *starts != 0 {
		t.Errorf("avvii di job: %d", *starts)
	}
}

func TestVideoCapsRefusals(t *testing.T) {
	starts := capsServer(t)
	frame := filepath.Join(t.TempDir(), "f.png")
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	os.WriteFile(frame, buf.Bytes(), 0o644)
	cases := map[string][]string{
		"last-frame su runway": {"--model", "runway/gen-4.5", "--last-frame", frame, "--image", frame, "x"},
		"durata 5 su veo":      {"--model", "google/veo-3.1", "--duration", "5", "x"},
		"param non in lista":   {"--model", "kwaivgi/kling-v3.0-pro", "--param", "foo=1", "x"},
		"stima oltre max-cost": {"--model", "kwaivgi/kling-v3.0-pro", "--duration", "5", "--audio", "off", "--max-cost", "0.1", "x"},
	}
	for name, args := range cases {
		for _, extra := range [][]string{nil, {"--dry-run"}} {
			_, se, code := video(t, append(extra, args...)...)
			if code != 2 {
				t.Errorf("%s %v: code=%d %s", name, extra, code, se)
			}
		}
	}
	if *starts != 0 {
		t.Errorf("avvii di job: %d", *starts)
	}
}

func TestNegativeWarnStrict(t *testing.T) {
	starts := capsServer(t)
	args := []string{"--dry-run", "--model", "runway/gen-4.5", "--duration", "5", "--negative", "blur", "x"}
	out, se, code := video(t, args...)
	if code != 0 || !strings.Contains(se, "avviso") || !strings.Contains(out, "warnings") {
		t.Errorf("atteso avviso e codice 0: code=%d out=%s se=%s", code, out, se)
	}
	_, se, code = video(t, append([]string{"--strict"}, args...)...)
	if code != 2 {
		t.Errorf("--strict: code=%d %s", code, se)
	}
	if *starts != 0 {
		t.Errorf("avvii di job: %d", *starts)
	}
}

// Audio non indicato: la stima e' il massimo fra le tariffe compatibili.
func TestCostWithoutAudioFlag(t *testing.T) {
	starts := capsServer(t)
	base := []string{"--model", "kwaivgi/kling-v3.0-pro", "--duration", "5", "--resolution", "720p"}
	if got := dryCost(t, append(base, "x")...); got < 0.835 || got > 0.845 {
		t.Errorf("senza --audio: %v want 0.84", got)
	}
	if got := dryCost(t, append(base, "--audio", "off", "x")...); got < 0.555 || got > 0.565 {
		t.Errorf("--audio off: %v want 0.56", got)
	}
	for _, extra := range [][]string{nil, {"--dry-run"}} {
		_, se, code := video(t, append(append(extra, "--max-cost", "0.60"), append(base, "x")...)...)
		if code != 2 {
			t.Errorf("--max-cost 0.60 senza --audio %v: code=%d %s", extra, code, se)
		}
	}
	if *starts != 0 {
		t.Errorf("avvii di job: %d", *starts)
	}
}
