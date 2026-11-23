package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Q-R (CLI-1030): documentazione e versione 0.13.1.

func TestQR131NovitaNelReadmeENellHelp(t *testing.T) {
	readme := pr13Readme(t)
	for _, w := range []string{"--max-cost", "0.13.1"} {
		if !strings.Contains(readme, w) {
			t.Errorf("README senza %q", w)
		}
	}
	so, se, code, _ := runGiv(t, nil, "story", "run", "--help")
	if code != 0 || !strings.Contains(so+se, "--max-cost") {
		t.Errorf("giv story run --help: code=%d senza --max-cost", code)
	}
}

func TestQR131ChangelogEVersione(t *testing.T) {
	b, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^## \[0\.13\.1\]`).Match(b) {
		t.Error("CHANGELOG senza la voce 0.13.1")
	}
	so, _, code, _ := runGiv(t, nil, "--version")
	if code != 0 || strings.TrimSpace(so) != "giv 0.13.1" {
		t.Errorf("giv --version: code=%d out=%q", code, so)
	}
}
