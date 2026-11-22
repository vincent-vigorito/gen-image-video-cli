package main

import (
	"bytes"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParsePrompt(t *testing.T) {
	var model string
	mk := func() *flag.FlagSet {
		model = ""
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(&bytes.Buffer{})
		fs.StringVar(&model, "model", "", "")
		return fs
	}
	if p, err := parsePrompt(mk(), []string{"--model", "m1", "un gatto"}); err != nil || p != "un gatto" || model != "m1" {
		t.Fatalf("flag prima: %q %q %v", p, model, err)
	}
	if p, err := parsePrompt(mk(), []string{"un gatto", "--model", "m2"}); err != nil || p != "un gatto" || model != "m2" {
		t.Fatalf("flag dopo: %q %q %v", p, model, err)
	}
	if _, err := parsePrompt(mk(), []string{"--model", "m"}); err == nil {
		t.Fatal("atteso errore senza prompt")
	}
	if _, err := parsePrompt(mk(), []string{"un", "gatto"}); err == nil {
		t.Fatal("atteso errore con prompt non quotato")
	}
}

// --- test del binario ---

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "giv-bin")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "giv")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// proxy finto: conta ogni connessione/richiesta uscente (HTTPS_PROXY/HTTP_PROXY).
func fakeProxy(t *testing.T) (url string, hits *int32) {
	t.Helper()
	hits = new(int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		http.Error(w, "proxy finto", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, hits
}

// runGiv lancia il binario con HOME/cwd temporanei e chiavi finte.
func runGiv(t *testing.T, env []string, args ...string) (stdout, stderr string, code int, hits int32) {
	t.Helper()
	proxy, h := fakeProxy(t)
	home, cwd := t.TempDir(), t.TempDir()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = cwd
	cmd.Env = append([]string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home,
		"HTTPS_PROXY=" + proxy, "HTTP_PROXY=" + proxy, "NO_PROXY=",
		"GEMINI_API_KEY=fake", "OPENAI_API_KEY=fake", "XAI_API_KEY=fake", "OPENROUTER_API_KEY=fake",
	}, env...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), se.String(), code, atomic.LoadInt32(h)
}

func TestHelpDefaults(t *testing.T) {
	_, help, code, _ := runGiv(t, nil, "--help")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"gpt-image-2.6-flare", "google/gemini-omni-1.2-flash", "gemini-3.1-flash-image"} {
		if !strings.Contains(help, want) {
			t.Errorf("help senza %q:\n%s", want, help)
		}
	}
	for _, old := range []string{"gpt-image-1", "sora-2", "veo-3.1-fast-generate-preview"} {
		if strings.Contains(help, old) {
			t.Errorf("help cita ancora %q", old)
		}
	}
}

func TestVideoOpenAIRemoved(t *testing.T) {
	_, se, code, hits := runGiv(t, nil, "video", "--provider", "openai", "x")
	if code == 0 || hits != 0 {
		t.Fatalf("code=%d hits=%d", code, hits)
	}
	if !strings.Contains(se, "openrouter") || !strings.Contains(se, "xai") {
		t.Errorf("errore non cita openrouter/xai: %s", se)
	}
}

// GIV_PROVIDER: senza chiave del provider scelto l'errore nomina la sua chiave.
func TestGivProvider(t *testing.T) {
	noKeys := []string{"GEMINI_API_KEY=", "OPENAI_API_KEY=", "XAI_API_KEY=", "OPENROUTER_API_KEY="}
	_, se, code, _ := runGiv(t, noKeys, "image", "x")
	if code == 0 || !strings.Contains(se, "GEMINI_API_KEY") {
		t.Errorf("default deve restare gemini: code=%d %s", code, se)
	}
	_, se, code, _ = runGiv(t, append(noKeys, "GIV_PROVIDER=openrouter"), "image", "x")
	if code == 0 || !strings.Contains(se, "OPENROUTER_API_KEY") {
		t.Errorf("GIV_PROVIDER=openrouter ignorato: code=%d %s", code, se)
	}
	_, se, code, _ = runGiv(t, append(noKeys, "GIV_PROVIDER=openrouter"), "image", "--provider", "xai", "x")
	if code == 0 || !strings.Contains(se, "XAI_API_KEY") {
		t.Errorf("--provider deve vincere: code=%d %s", code, se)
	}
}
