package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// V-T0: giv voice con xAI contro un server finto (in-process).

func fakeWAV(sec float64) []byte {
	n := int(24000 * sec * 2)
	b := &bytes.Buffer{}
	b.WriteString("RIFF")
	binary.Write(b, binary.LittleEndian, uint32(36+n))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(24000), uint32(48000), uint16(2), uint16(16)} {
		binary.Write(b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(b, binary.LittleEndian, uint32(n))
	b.Write(make([]byte, n))
	return b.Bytes()
}

func voiceRun(t *testing.T, args ...string) (out, errOut string, code int) {
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
	err := cmdVoice(args)
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

func voiceEnv(t *testing.T, h http.HandlerFunc) (hits *int32, dir string) {
	t.Helper()
	hits = new(int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	old := baseURLs["xai"]
	baseURLs["xai"] = srv.URL
	t.Cleanup(func() { baseURLs["xai"] = old })
	t.Setenv("XAI_API_KEY", "fake-key-xyz")
	t.Setenv("HOME", t.TempDir())
	dir = t.TempDir()
	t.Chdir(dir)
	return
}

func TestVoiceXAIWavETempi(t *testing.T) {
	var body map[string]any
	var auth string
	wav := fakeWAV(1)
	hits, dir := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tts" || r.Method != "POST" {
			t.Errorf("richiesta inattesa %s %s", r.Method, r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"audio": base64.StdEncoding.EncodeToString(wav), "content_type": "audio/wav", "duration": 1.0,
			"audio_timestamps": map[string]any{
				"graph_chars": []string{"C", "i", "a", "o"},
				"graph_times": []map[string]float64{{"start": 0, "end": 0.2}, {"start": 0.2, "end": 0.4}, {"start": 0.4, "end": 0.6}, {"start": 0.6, "end": 0.9}},
			},
		})
	})
	out, se, code := voiceRun(t, "--out", dir, "--name", "prova", "Ciao")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if *hits != 1 || auth != "Bearer fake-key-xyz" {
		t.Errorf("hits=%d auth=%q", *hits, auth)
	}
	if body["text"] != "Ciao" || body["language"] != "it" || body["voice_id"] != "eve" || body["with_timestamps"] != true {
		t.Errorf("corpo: %v", body)
	}
	of, _ := body["output_format"].(map[string]any)
	if of["codec"] != "wav" {
		t.Errorf("output_format: %v", body["output_format"])
	}
	if _, err := os.Stat(filepath.Join(dir, "prova.wav")); err != nil {
		t.Error(err)
	}
	tb, err := os.ReadFile(filepath.Join(dir, "prova.tempi.json"))
	if err != nil || !strings.Contains(string(tb), `"provider"`) {
		t.Errorf("tempi: %v %s", err, tb)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout non JSON: %v\n%s", err, out)
	}
	if m["command"] != "voice" || m["provider"] != "xai" {
		t.Errorf("manifest: %v", m)
	}
	// stima = caratteri x 15 $/M = 4 * 15e-6
	if c, _ := m["cost_est_usd"].(float64); c < 5.9e-5 || c > 6.1e-5 {
		t.Errorf("cost_est_usd=%v", m["cost_est_usd"])
	}
	if strings.Contains(out+se, "fake-key-xyz") {
		t.Error("chiave in output")
	}
}

func TestVoiceDryRunSenzaChiamate(t *testing.T) {
	hits, _ := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {})
	t.Setenv("XAI_API_KEY", "") // il dry-run non chiede la chiave
	out, se, code := voiceRun(t, "--dry-run", "Ciao a tutti")
	if code != 0 || *hits != 0 {
		t.Fatalf("code=%d hits=%d %s", code, *hits, se)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil || m["dry_run"] != true {
		t.Fatalf("dry-run: %v %s", err, out)
	}
	req, _ := m["request"].(map[string]any)
	if req["text"] != "Ciao a tutti" {
		t.Errorf("request: %v", req)
	}
	if c, _ := m["cost_est_usd"].(float64); c < 1.7e-4 || c > 1.9e-4 { // 12 caratteri
		t.Errorf("cost_est_usd=%v", m["cost_est_usd"])
	}
}

func TestVoiceRifiutiPrimaDelleChiamate(t *testing.T) {
	hits, _ := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {})
	dict := filepath.Join(t.TempDir(), "d.json")
	var big strings.Builder
	big.WriteString("{")
	for i := 0; i < 201; i++ {
		if i > 0 {
			big.WriteString(",")
		}
		fmt.Fprintf(&big, `"parola%c%c%c":"x"`, 'a'+i/26/26%26, 'a'+i/26%26, 'a'+i%26)
	}
	big.WriteString("}")
	os.WriteFile(dict, []byte(big.String()), 0o600)
	cases := map[string][]string{
		"testo vuoto":       {""},
		"testo mancante":    {},
		"tetto superato":    {"--max-cost", "0.00001", "Ciao a tutti"},
		"tetto negativo":    {"--max-cost", "-1", "Ciao"},
		"dizionario >200":   {"--pronuncia", dict, "Ciao"},
		"testo e text-file": {"--text-file", dict, "Ciao"},
	}
	for name, args := range cases {
		_, se, code := voiceRun(t, args...)
		if code != 2 {
			t.Errorf("%s: code=%d (atteso 2) %s", name, code, se)
		}
	}
	if *hits != 0 {
		t.Errorf("chiamate=%d, attese 0", *hits)
	}
}

func TestVoiceChiaveMancante(t *testing.T) {
	hits, _ := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {})
	t.Setenv("XAI_API_KEY", "")
	_, se, code := voiceRun(t, "Ciao")
	if code != 10 || *hits != 0 || !strings.Contains(se, "XAI_API_KEY") {
		t.Errorf("code=%d hits=%d %s", code, *hits, se)
	}
}

func TestVoiceReindirizzamentoEsitoIncerto(t *testing.T) {
	hits, dir := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://altro.example/tts", http.StatusTemporaryRedirect)
	})
	_, se, code := voiceRun(t, "--out", dir, "Ciao")
	if code != 5 || *hits != 1 {
		t.Fatalf("code=%d hits=%d %s", code, *hits, se)
	}
	if !strings.Contains(se, "incert") {
		t.Errorf("messaggio: %s", se)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*.wav")); len(m) != 0 {
		t.Errorf("file scritti: %v", m)
	}
}

func TestVoiceErroreFiltrato(t *testing.T) {
	_, dir := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(400)
		fmt.Fprint(w, "<html><body>fake-key-xyz errore interno https://x.example/segreto?token=abc</body></html>")
	})
	_, se, code := voiceRun(t, "--out", dir, "Ciao")
	if code == 0 {
		t.Fatal("atteso errore")
	}
	for _, bad := range []string{"<html>", "fake-key-xyz", "token=abc"} {
		if strings.Contains(se, bad) {
			t.Errorf("errore non filtrato (%q): %s", bad, se)
		}
	}
}
