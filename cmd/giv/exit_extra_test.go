package main

import (
	"testing"

	"gen-image-video-cli/internal/httpx"
)

func TestExitCode403E401(t *testing.T) {
	code, _ := exitCode(&httpx.Error{Prefix: "openrouter", Status: 403})
	if code != 4 {
		t.Errorf("403: code=%d, atteso 4", code)
	}
}

func TestExitCodeChiaveMancante(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	t.Setenv(envKeys["xai"], "")
	_, err := newProvider("xai")
	if err == nil {
		t.Fatal("atteso errore")
	}
	if code, _ := exitCode(err); code != 10 {
		t.Errorf("chiave mancante: code=%d, atteso 10 (%v)", code, err)
	}
}

func TestVideoSenzaModelloEsce2(t *testing.T) {
	for _, p := range []string{"openai", "gemini"} {
		err := cmdVideo([]string{"--provider", p, "un prompt"})
		if err == nil {
			t.Fatalf("%s: atteso errore", p)
		}
		if code, _ := exitCode(err); code != 2 {
			t.Errorf("%s: code=%d, atteso 2 (%v)", p, code, err)
		}
	}
}
