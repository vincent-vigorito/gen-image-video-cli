package main

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// V-T2: giv qc --voice contro un server finto xAI /stt.

func qcVoiceRun(t *testing.T, args ...string) (out, errOut string, code int) {
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
	err := cmdQC(append([]string{"--voice"}, args...))
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

func sttResp(text string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		words := []map[string]any{}
		for i, x := range strings.Fields(text) {
			words = append(words, map[string]any{"text": x, "start": float64(i) * 0.3, "end": float64(i)*0.3 + 0.25})
		}
		json.NewEncoder(w).Encode(map[string]any{"text": text, "language": "it", "duration": 1.0, "words": words})
	}
}

func qcAudio(t *testing.T, dir string) string {
	p := filepath.Join(dir, "v.wav")
	if err := os.WriteFile(p, fakeWAV(1), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestQCVoiceTestoUgualePassa(t *testing.T) {
	var last string
	var key string
	hits, dir := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("Authorization")
		mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.URL.Path != "/stt" || !strings.HasPrefix(mt, "multipart/") {
			t.Errorf("richiesta inattesa %s %s %s", r.Method, r.URL.Path, mt)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			last = p.FormName()
			io.Copy(io.Discard, p)
		}
		sttResp("Ciao a tutti")(w, r)
	})
	aud := qcAudio(t, dir)
	before, _ := os.ReadFile(aud)
	out, se, code := qcVoiceRun(t, aud, "--script", "Ciao a tutti", "--strict")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if *hits != 1 || key != "Bearer fake-key-xyz" || last != "file" {
		t.Errorf("hits=%d key=%q ultimo campo=%q", *hits, key, last)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout: %v\n%s", err, out)
	}
	cmp, _ := m["comparison"].(map[string]any)
	if r, _ := cmp["ratio"].(float64); r != 1 {
		t.Errorf("comparison: %v", cmp)
	}
	after, _ := os.ReadFile(aud)
	if string(before) != string(after) {
		t.Error("audio modificato")
	}
}

func TestQCVoiceDifferenzaAvvisaStrictRc1(t *testing.T) {
	hits, dir := voiceEnv(t, sttResp("Ciao tutti quanti oggi"))
	aud := qcAudio(t, dir)
	out, _, code := qcVoiceRun(t, aud, "--script", "Buongiorno a voi")
	if code != 0 || *hits != 1 {
		t.Fatalf("senza strict: code=%d hits=%d", code, *hits)
	}
	var m map[string]any
	json.Unmarshal([]byte(out), &m)
	cmp, _ := m["comparison"].(map[string]any)
	if r, _ := cmp["ratio"].(float64); r >= 0.5 {
		t.Errorf("ratio=%v", r)
	}
	out2, _, code := qcVoiceRun(t, aud, "--script", "Buongiorno a voi", "--strict")
	if code != 1 || !strings.Contains(out2, "comparison") {
		t.Errorf("strict: code=%d", code)
	}
	if *hits != 2 {
		t.Errorf("rilanci automatici? hits=%d", *hits)
	}
}

func TestQCVoiceTettoDryRunERifiuti(t *testing.T) {
	hits, dir := voiceEnv(t, sttResp("x"))
	aud := qcAudio(t, dir)
	if _, se, code := qcVoiceRun(t, aud, "--script", "x", "--max-cost", "0.0000001"); code != 2 {
		t.Errorf("tetto: code=%d %s", code, se)
	}
	empty := filepath.Join(dir, "e.wav")
	os.WriteFile(empty, nil, 0o600)
	if _, se, code := qcVoiceRun(t, empty, "--script", "x"); code != 2 {
		t.Errorf("file vuoto: code=%d %s", code, se)
	}
	if _, se, code := qcVoiceRun(t, aud, "--script", "x", "--provider", "gemini"); code != 2 {
		t.Errorf("provider gemini: code=%d %s", code, se)
	}
	if *hits != 0 {
		t.Fatalf("chiamate=%d", *hits)
	}
	out, se, code := qcVoiceRun(t, aud, "--script", "x", "--dry-run")
	if code != 0 || *hits != 0 || !strings.Contains(out, "cost_est_usd") {
		t.Errorf("dry-run: code=%d hits=%d %s %s", code, *hits, out, se)
	}
}

func TestQCVoiceCaricamentoReindirizzatoNonSeguito(t *testing.T) {
	hits, dir := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusTemporaryRedirect)
	})
	aud := qcAudio(t, dir)
	_, se, code := qcVoiceRun(t, aud, "--script", "x")
	if code != 5 || *hits != 1 {
		t.Errorf("code=%d hits=%d %s", code, *hits, se)
	}
}

func TestQCVoiceErrore4xxFiltrato(t *testing.T) {
	_, dir := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte("<html>fake-key-xyz</html>"))
	})
	aud := qcAudio(t, dir)
	_, se, code := qcVoiceRun(t, aud, "--script", "x")
	if code == 0 || strings.Contains(se, "fake-key-xyz") || strings.Contains(se, "<html>") {
		t.Errorf("code=%d %s", code, se)
	}
}

func TestQCSenzaVoiceNessunaRichiesta(t *testing.T) {
	hits, dir := voiceEnv(t, sttResp("x"))
	aud := qcAudio(t, dir)
	capture := func(f **os.File) func() {
		old := *f
		_, w, _ := os.Pipe()
		*f = w
		return func() { w.Close(); *f = old }
	}
	c1, c2 := capture(&os.Stdout), capture(&os.Stderr)
	cmdQC([]string{aud})
	c1()
	c2()
	if *hits != 0 {
		t.Errorf("chiamate=%d senza --voice", *hits)
	}
}

func TestQCVoicePronunciaDettaValeLaParolaScritta(t *testing.T) {
	_, dir := voiceEnv(t, sttResp("Ciao suèrpifai"))
	aud := qcAudio(t, dir)
	dict := filepath.Join(dir, "p.json")
	os.WriteFile(dict, []byte(`{"Swerpify":"suèrpifai"}`), 0o600)
	out, se, code := qcVoiceRun(t, aud, "--script", "Ciao Swerpify", "--pronuncia", dict, "--strict")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	var m map[string]any
	json.Unmarshal([]byte(out), &m)
	cmp, _ := m["comparison"].(map[string]any)
	if r, _ := cmp["ratio"].(float64); r != 1 {
		t.Errorf("comparison: %v", cmp)
	}
}
