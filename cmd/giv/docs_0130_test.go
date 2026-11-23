package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// P-R (CLI-1017): documentazione e versione 0.13.0.

func pr13Readme(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ogni flag dell'help dei comandi della 0.13.0 è nel README, e i comandi nuovi ci sono.
func TestPR13HelpENelReadme(t *testing.T) {
	readme := pr13Readme(t)
	flagRe := regexp.MustCompile(`--([a-z][a-z0-9-]*)`)
	for _, c := range [][]string{{"story", "run"}, {"story", "approve"}, {"story", "cut"}, {"story", "check"}, {"qc"}} {
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

// flag e campi nuovi: devono stare nell'help e nel README.
func TestPR13NovitaNelReadmeENellHelp(t *testing.T) {
	readme := pr13Readme(t)
	for _, w := range []string{"--keyframes", "--bozza", "--parallel", "--all", "--judge", "--max-cost",
		"giv story approve", "approvazione", "parallelo", "aderenza", "BOZZA"} {
		if !strings.Contains(readme, w) {
			t.Errorf("README senza %q", w)
		}
	}
	helps := map[string][]string{
		"story run":     {"--keyframes", "--bozza", "--parallel"},
		"story approve": {"--all"},
		"story cut":     {"--bozza"},
		"qc":            {"--judge", "--max-cost"},
	}
	for cmd, flags := range helps {
		so, se, code, _ := runGiv(t, nil, append(strings.Fields(cmd), "--help")...)
		if code != 0 {
			t.Errorf("giv %s --help: exit %d", cmd, code)
		}
		for _, f := range flags {
			if !strings.Contains(so+se, f) {
				t.Errorf("giv %s --help senza %s", cmd, f)
			}
		}
	}
}

func TestPR13ChangelogEVersione(t *testing.T) {
	b, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^## \[0\.13\.0\]`).Match(b) {
		t.Error("CHANGELOG senza la voce 0.13.0")
	}
	so, _, code, _ := runGiv(t, nil, "--version")
	if code != 0 || strings.TrimSpace(so) != "giv 0.13.1" {
		t.Errorf("giv --version: code=%d out=%q", code, so)
	}
}

// almeno un esempio usa le opzioni nuove (e passa story check: lo prova
// TestV9EsempiStoryboardCheck su ogni esempio).
func TestPR13EsempioConOpzioniNuove(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*/storyboard*.json")
	re := regexp.MustCompile(`"(approvazione|parallelo|aderenza|bozza)"`)
	found := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			found[m[1]] = true
		}
	}
	for _, k := range []string{"approvazione", "parallelo", "aderenza", "bozza"} {
		if !found[k] {
			t.Errorf("nessun esempio usa il campo %q", k)
		}
	}
}
