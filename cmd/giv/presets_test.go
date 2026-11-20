package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type showOut struct {
	Family      string `json:"famiglia"`
	PromptFinal string `json:"prompt_final"`
	Presets     []struct {
		ID       string `json:"id"`
		Variante string `json:"variante"`
	} `json:"presets"`
	Warnings []string `json:"warnings"`
}

func TestPresetsShowOrbitVariante(t *testing.T) {
	out, _, code, hits := runGiv(t, nil, "presets", "show", "orbit", "--model", "kwaivgi/kling-v3.0-pro")
	if code != 0 || hits != 0 {
		t.Fatalf("code=%d hits=%d out=%s", code, hits, out)
	}
	var s showOut
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err, out)
	}
	if s.Family != "kling" || len(s.Presets) != 1 || s.Presets[0].Variante != "kling" {
		t.Fatalf("atteso variante kling: %+v", s)
	}
}

func TestPresetsShowSenzaVarianteAvviso(t *testing.T) {
	out, _, code, _ := runGiv(t, nil, "presets", "show", "orbit", "--model", "acme/sconosciuto-1")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	var s showOut
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err, out)
	}
	if len(s.Presets) != 1 || s.Presets[0].Variante != "generica" || len(s.Warnings) == 0 || s.PromptFinal == "" {
		t.Fatalf("attesa frase generica con avviso: %+v", s)
	}
}

func TestPresetsRifiutiCodice2(t *testing.T) {
	for name, args := range map[string][]string{
		"inesistente":  {"presets", "show", "nope"},
		"dolly in/out": {"presets", "show", "dolly-in", "dolly-out"},
		"due camera":   {"presets", "show", "orbit", "pan"},
	} {
		_, _, code, hits := runGiv(t, nil, args...)
		if code != 2 || hits != 0 {
			t.Errorf("%s: code=%d hits=%d, atteso 2/0", name, code, hits)
		}
	}
}

func TestPresetsFileUtenteMalformato(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "giv")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "presets.json"), []byte("{non json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"presets"}, {"presets", "show", "orbit"}} {
		cmd := exec.Command(binPath, args...)
		cmd.Dir = t.TempDir()
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
		out, err := cmd.CombinedOutput()
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 2 {
			t.Errorf("%v: atteso codice 2, err=%v out=%s", args, err, out)
		}
		if !strings.Contains(string(out), "presets.json") {
			t.Errorf("%v: l'errore non cita il file: %s", args, out)
		}
	}
}
