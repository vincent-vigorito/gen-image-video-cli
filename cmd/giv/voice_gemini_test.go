package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// V-T1: giv voice con Gemini (server finto), --kit, FIFO, tempi xAI eccessivi.

func geminiEnv(t *testing.T, h http.HandlerFunc) (hits *int32, dir string) {
	t.Helper()
	hits = new(int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	old := baseURLs["gemini"]
	baseURLs["gemini"] = srv.URL
	t.Cleanup(func() { baseURLs["gemini"] = old })
	t.Setenv("GEMINI_API_KEY", "fake-gem-key")
	t.Setenv("HOME", t.TempDir())
	dir = t.TempDir()
	t.Chdir(dir)
	return
}

func geminiOK(sec float64, usage map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"status": "completed", "steps": []any{map[string]any{"type": "model_output",
			"content": []any{map[string]any{"type": "audio", "mime_type": "audio/wav", "data": base64.StdEncoding.EncodeToString(fakeWAV(sec))}}}}}
		if usage != nil {
			resp["usage"] = usage
		}
		json.NewEncoder(w).Encode(resp)
	}
}

func TestVoiceGeminiWavEPronunce(t *testing.T) {
	var body map[string]any
	var key string
	hits, dir := geminiEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/interactions" || r.Method != "POST" {
			t.Errorf("richiesta inattesa %s %s", r.Method, r.URL.Path)
		}
		key = r.Header.Get("x-goog-api-key")
		json.NewDecoder(r.Body).Decode(&body)
		geminiOK(2, map[string]any{"total_input_tokens": 10, "total_output_tokens": 50})(w, r)
	})
	dict := filepath.Join(dir, "p.json")
	os.WriteFile(dict, []byte(`{"Swerpify":"suerpifai"}`), 0o600)
	out, se, code := voiceRun(t, "--provider", "gemini", "--out", dir, "--name", "g", "--pronuncia", dict, "Ciao Swerpify")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if *hits != 1 || key != "fake-gem-key" {
		t.Errorf("hits=%d key=%q", *hits, key)
	}
	in, _ := body["input"].([]any)
	c0, _ := in[0].(map[string]any)["content"].([]any)
	if txt := c0[0].(map[string]any)["text"]; txt != "Ciao suerpifai" {
		t.Errorf("testo inviato: %v", txt)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "g.wav")); err != nil || len(b) < 44 {
		t.Errorf("wav: %v", err)
	}
	if strings.Contains(out+se, "fake-gem-key") {
		t.Error("chiave in output")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil || m["provider"] != "gemini" {
		t.Errorf("manifest: %v %s", err, out)
	}
}

func TestVoiceGeminiRifiutiSenzaChiamate(t *testing.T) {
	hits, _ := geminiEnv(t, geminiOK(1, nil))
	for name, args := range map[string][]string{
		"testo vuoto":    {"--provider", "gemini", ""},
		"tetto superato": {"--provider", "gemini", "--max-cost", "0.0000001", "Ciao a tutti"},
	} {
		if _, se, code := voiceRun(t, args...); code != 2 {
			t.Errorf("%s: code=%d %s", name, code, se)
		}
	}
	if *hits != 0 {
		t.Errorf("chiamate=%d", *hits)
	}
}

func TestVoiceGeminiReindirizzamentoNonSeguito(t *testing.T) {
	var other int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&other, 1) }))
	defer srv2.Close()
	hits, dir := geminiEnv(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv2.URL+"/x", http.StatusTemporaryRedirect)
	})
	_, se, code := voiceRun(t, "--provider", "gemini", "--out", dir, "Ciao")
	if code != 5 || *hits != 1 || other != 0 {
		t.Fatalf("code=%d hits=%d other=%d %s", code, *hits, other, se)
	}
}

func TestVoiceGeminiErroreFiltrato(t *testing.T) {
	_, dir := geminiEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":{"code":"X","message":"<html>fake-gem-key https://x.example/s?token=abc</html>"}}`))
	})
	_, se, code := voiceRun(t, "--provider", "gemini", "--out", dir, "Ciao")
	if code == 0 {
		t.Fatal("atteso errore")
	}
	for _, bad := range []string{"<html>", "fake-gem-key", "token=abc"} {
		if strings.Contains(se, bad) {
			t.Errorf("errore non filtrato (%q): %s", bad, se)
		}
	}
}

func TestVoiceKitVoceLinguaPronunce(t *testing.T) {
	var body map[string]any
	_, dir := geminiEnv(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		geminiOK(1, nil)(w, r)
	})
	os.WriteFile(filepath.Join(dir, "kit.json"), []byte(`{"voce":{"provider":"gemini","voce":"Kore","lingua":"it"},"pronuncia":{"Anja":"Ania"}}`), 0o600)
	_, se, code := voiceRun(t, "--kit", filepath.Join(dir, "kit.json"), "--out", dir, "--name", "k", "Ciao Anja")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	gc := body["generation_config"].(map[string]any)["speech_config"].([]any)[0].(map[string]any)
	if gc["voice"] != "Kore" {
		t.Errorf("voce dal kit: %v", gc)
	}
	in := body["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if in["text"] != "Ciao Ania" {
		t.Errorf("pronunce dal kit: %v", in["text"])
	}
}

func TestVoiceFIFORifiutato(t *testing.T) {
	hits, dir := geminiEnv(t, geminiOK(1, nil))
	fifo := filepath.Join(dir, "f")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	for _, flag := range []string{"--text-file", "--pronuncia"} {
		done := make(chan int, 1)
		go func() {
			args := []string{"--provider", "gemini", "--out", dir, flag, fifo}
			if flag == "--pronuncia" {
				args = append(args, "Ciao")
			}
			_, _, code := voiceRun(t, args...)
			done <- code
		}()
		select {
		case code := <-done:
			if code != 2 {
				t.Errorf("%s: code=%d (atteso 2)", flag, code)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: il comando si è bloccato sul FIFO", flag)
		}
	}
	if *hits != 0 {
		t.Errorf("chiamate=%d", *hits)
	}
}

func TestVoiceXAITempiEccessiviScartati(t *testing.T) {
	_, dir := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := 70000
		times := make([]float64, n)
		resp := map[string]any{"audio": base64.StdEncoding.EncodeToString(fakeWAV(1)), "content_type": "audio/wav", "duration": 1.0,
			"audio_timestamps": map[string]any{"graph_chars": strings.Repeat("a", n), "graph_times": times}}
		json.NewEncoder(w).Encode(resp)
	})
	out, se, code := voiceRun(t, "--out", dir, "--name", "t", "Ciao")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if _, err := os.Stat(filepath.Join(dir, "t.wav")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "t.tempi.json")); err == nil {
		t.Error("tempi eccessivi non scartati")
	}
	if !strings.Contains(out+se, "temp") {
		t.Errorf("nessun avviso sui tempi: %s %s", out, se)
	}
}

// la stima di Gemini per il tetto è il massimo per richiesta (0,151552 $ flash).
func TestVoiceGeminiTettoSulMassimo(t *testing.T) {
	hits, dir := geminiEnv(t, geminiOK(1, nil))
	if _, se, code := voiceRun(t, "--provider", "gemini", "--max-cost", "0.1", "--out", dir, "Ciao"); code != 2 || *hits != 0 {
		t.Errorf("sotto il massimo: code=%d hits=%d %s", code, *hits, se)
	}
	if _, se, code := voiceRun(t, "--provider", "gemini", "--max-cost", "0.5", "--out", dir, "Ciao"); code != 0 || *hits != 1 {
		t.Errorf("sopra il massimo: code=%d hits=%d %s", code, *hits, se)
	}
}
