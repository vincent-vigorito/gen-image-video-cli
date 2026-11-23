package main

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gen-image-video-cli/internal/kit"
)

// P-R (CLI-1000): documentazione e versione 0.12.1.

func pr1Readme(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ogni flag dell'help dei comandi toccati dalla 0.12.1 è nel README.
func TestPR1FlagHelpNelReadme(t *testing.T) {
	readme := pr1Readme(t)
	flagRe := regexp.MustCompile(`--([a-z][a-z0-9-]*)`)
	for _, c := range [][]string{{"voice"}, {"kit", "check"}, {"qc"}, {"story", "check"}, {"story", "run"}, {"story", "cut"}} {
		name := strings.Join(c, " ")
		so, se, code, _ := runGiv(t, nil, append(append([]string{}, c...), "--help")...)
		if code != 0 {
			t.Errorf("giv %s --help: exit %d %s", name, code, se)
			continue
		}
		if !strings.Contains(readme, "giv "+name) {
			t.Errorf("giv %s: non è nel README", name)
		}
		var missing []string
		seen := map[string]bool{}
		for _, m := range flagRe.FindAllStringSubmatch(so+se, -1) {
			if m[1] != "help" && !seen[m[1]] && !strings.Contains(readme, "--"+m[1]) {
				missing = append(missing, "--"+m[1])
			}
			seen[m[1]] = true
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("giv %s: flag nell'help ma non nel README: %v", name, missing)
		}
	}
}

// campi nuovi: story check (run_status, run_cost_est_usd), nomi propri nel kit.
func TestPR1CampiNuoviNelReadme(t *testing.T) {
	readme := pr1Readme(t)
	for _, f := range []string{"run_status", "run_cost_est_usd"} {
		if !strings.Contains(readme, f) {
			t.Errorf("campo di story check non documentato nel README: %s", f)
		}
	}
	// il campo dei nomi propri nel kit: un tag json che comincia per "nom"
	var tag string
	rt := reflect.TypeOf(kit.Kit{})
	for i := 0; i < rt.NumField(); i++ {
		if g := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]; strings.HasPrefix(g, "nom") {
			tag = g
		}
	}
	if tag == "" {
		t.Fatal("il kit non ha il campo dei nomi propri (tag json nom*)")
	}
	if !strings.Contains(readme, "`"+tag+"`") && !strings.Contains(readme, `"`+tag+`"`) {
		t.Errorf("campo del kit non documentato nel README: %s", tag)
	}
	for _, w := range []string{"controllo.json"} {
		if !strings.Contains(readme, w) {
			t.Errorf("README senza %s", w)
		}
	}
}

func TestPR1ChangelogEVersione(t *testing.T) {
	b, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^## \[0\.12\.1\]`).Match(b) {
		t.Error("CHANGELOG senza la voce 0.12.1")
	}
	so, _, code, _ := runGiv(t, nil, "--version")
	if code != 0 || strings.TrimSpace(so) != "giv 0.12.1" {
		t.Errorf("giv --version: code=%d out=%q", code, so)
	}
}
