package main

import (
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CLI-926 (O-T7): giv upscale --provider fal contro il server fal finto di fal_test.go.

const topazEP = "/fal-ai/topaz/upscale/video"

func upFalClip(t *testing.T) string {
	t.Helper()
	cutNeedFF(t)
	p := filepath.Join(t.TempDir(), "in.mp4")
	cutClip(t, p, "1280x720", 30, "4.5") // ceil = 5 s
	return p
}

func upFal(t *testing.T, args ...string) (out, errOut string, code int) {
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
	err := cmdUpscale(append([]string{"--provider", "fal"}, args...))
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

// caso 1: caricamento, avvio, attesa, scaricamento, manifest con la stima.
func TestUpscaleFalCompleto(t *testing.T) {
	e := newFalEnv(t)
	clip := upFalClip(t)
	out, se, code := upFal(t, "--video", clip, "--factor", "1.5", "--out", "o")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, se)
	}
	var man map[string]any
	if err := json.Unmarshal([]byte(out), &man); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if man["command"] != "upscale" || man["provider"] != "fal" {
		t.Errorf("manifest %v", man)
	}
	// 5 s x 0,02 $ (uscita 1920x1080)
	if c, _ := man["cost_est_usd"].(float64); math.Abs(c-0.10) > 1e-6 {
		t.Errorf("cost_est_usd %v (atteso 0.1)", man["cost_est_usd"])
	}
	if n := e.count("POST", "/storage/upload/initiate"); n != 1 {
		t.Errorf("initiate %d", n)
	}
	e.mu.Lock()
	puts := 0
	for _, r := range e.other {
		if r.Method == "PUT" {
			puts++
			if r.Auth != "" {
				t.Errorf("PUT con Authorization")
			}
		}
	}
	var start map[string]any
	for _, r := range e.reqs {
		if r.Method == "POST" && r.Path == topazEP {
			json.Unmarshal(r.Body, &start)
		}
	}
	e.mu.Unlock()
	if puts != 1 {
		t.Errorf("PUT %d", puts)
	}
	if u, _ := start["video_url"].(string); !strings.HasPrefix(u, falStore) || start["upscale_factor"] != 1.5 {
		t.Errorf("corpo avvio %v", start)
	}
	if n := e.count("POST", topazEP); n != 1 {
		t.Errorf("avvii %d", n)
	}
	if f, _ := filepath.Glob("o/*.mp4"); len(f) != 1 {
		t.Errorf("file scaricati %v", f)
	}
}

// URL https: nessun upload, l'indirizzo va com'è.
func TestUpscaleFalURLSenzaUpload(t *testing.T) {
	e := newFalEnv(t)
	const u = "https://example.com/in.mp4"
	if _, se, code := upFal(t, "--video", u, "--out", "o"); code != 0 {
		t.Fatalf("code %d\n%s", code, se)
	}
	if n := e.count("POST", "/storage/upload/initiate"); n != 0 {
		t.Errorf("initiate %d", n)
	}
	var start map[string]any
	for _, r := range e.reqs {
		if r.Method == "POST" && r.Path == topazEP {
			json.Unmarshal(r.Body, &start)
		}
	}
	if start["video_url"] != u || start["upscale_factor"] != 2.0 {
		t.Errorf("corpo avvio %v", start)
	}
}

// caso 2: rifiuti a codice 2 senza richieste.
func TestUpscaleFalRifiuti(t *testing.T) {
	e := newFalEnv(t)
	clip := upFalClip(t)
	txt := filepath.Join(t.TempDir(), "x.mp4")
	os.WriteFile(txt, []byte("non un mp4"), 0o644)
	cases := map[string][]string{
		"senza video":    {},
		"file assente":   {"--video", "/nonesiste.mp4"},
		"non mp4":        {"--video", txt},
		"http":           {"--video", "http://example.com/in.mp4"},
		"mode":           {"--video", clip, "--mode", "precise"},
		"prompt":         {"--video", clip, "un prompt"},
		"duration":       {"--video", clip, "--duration", "5"},
		"resolution":     {"--video", clip, "--resolution", "720p"},
		"aspect":         {"--video", clip, "--aspect", "16:9"},
		"fattore sopra":  {"--video", clip, "--factor", "5"},
		"fattore sotto":  {"--video", clip, "--factor", "0.5"},
		"fattore zero":   {"--video", clip, "--factor", "0"},
		"modello strano": {"--video", clip, "--model", "altro"},
	}
	for name, args := range cases {
		if _, se, code := upFal(t, args...); code != 2 {
			t.Errorf("%s: code %d (atteso 2): %s", name, code, se)
		}
	}
	if n := len(e.reqs) + len(e.other); n != 0 {
		t.Errorf("richieste: %d", n)
	}
}

// caso 3: dry-run e tetto.
func TestUpscaleFalDryRunETetto(t *testing.T) {
	e := newFalEnv(t)
	clip := upFalClip(t)
	out, se, code := upFal(t, "--dry-run", "--video", clip, "--factor", "1.5")
	if code != 0 {
		t.Fatalf("code %d %s", code, se)
	}
	var p struct {
		Cost *float64 `json:"cost_est_usd"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil || p.Cost == nil || math.Abs(*p.Cost-0.10) > 1e-6 {
		t.Errorf("stima %v: %s", p.Cost, out)
	}
	if _, se, code := upFal(t, "--video", clip, "--factor", "1.5", "--max-cost", "0.05"); code != 2 {
		t.Errorf("oltre il tetto: code %d: %s", code, se)
	}
	if _, se, code := upFal(t, "--video", "https://example.com/in.mp4", "--max-cost", "5"); code != 2 {
		t.Errorf("senza stima con tetto: code %d: %s", code, se)
	}
	if n := len(e.reqs) + len(e.other); n != 0 {
		t.Errorf("richieste: %d", n)
	}
}

// caso 4: senza --provider resta openrouter, con lo stesso corpo di prima.
func TestUpscaleSenzaProviderResteOpenrouter(t *testing.T) {
	u := upEnv(t)
	u.noCost = true
	out, se, code := upGiv(t, "--video", "https://example.com/in.mp4", "--out", "o")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, se)
	}
	if upJSON(t, out)["provider"] != "openrouter" || len(u.posts) != 1 {
		t.Fatalf("provider/POST: %s posts=%d", out, len(u.posts))
	}
	b := u.posts[0]
	want := map[string]any{
		"model":            "black-forest-labs/flux-video-upscale",
		"upscale_factor":   2.0,
		"creativity":       0.0,
		"input_references": []any{map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://example.com/in.mp4"}}},
	}
	got, _ := json.Marshal(b)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Errorf("corpo\n got %s\nwant %s", got, exp)
	}
}
