package config

import (
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) (home, cwd string) {
	t.Helper()
	home, cwd = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIV_TEST_KEY", "")
	t.Chdir(cwd)
	return
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalFileIsLastFallback(t *testing.T) {
	home, _ := setup(t)
	write(t, filepath.Join(home, ".config", "giv", "credentials.env"), "GIV_TEST_KEY=dal-globale\n")
	if got := Get("GIV_TEST_KEY"); got != "dal-globale" {
		t.Fatalf("got %q, want dal-globale", got)
	}
}

func TestPrecedence(t *testing.T) {
	home, cwd := setup(t)
	write(t, filepath.Join(home, ".config", "giv", "credentials.env"), "GIV_TEST_KEY=globale\n")
	write(t, filepath.Join(cwd, ".env"), "GIV_TEST_KEY=dotenv\n")
	if got := Get("GIV_TEST_KEY"); got != "dotenv" {
		t.Fatalf(".env deve battere il globale: got %q", got)
	}
	write(t, filepath.Join(cwd, "credentials.env"), "GIV_TEST_KEY=locale\n")
	if got := Get("GIV_TEST_KEY"); got != "locale" {
		t.Fatalf("./credentials.env deve battere ./.env: got %q", got)
	}
	t.Setenv("GIV_TEST_KEY", "da-env")
	if got := Get("GIV_TEST_KEY"); got != "da-env" {
		t.Fatalf("env di processo vince: got %q", got)
	}
}

func TestMissingKey(t *testing.T) {
	setup(t)
	if got := Get("GIV_TEST_KEY"); got != "" {
		t.Fatalf("got %q, want vuoto", got)
	}
}
