package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// CLI-886: help <-> README coerenti, flag per flag (la versione è in TestVersioneRilascio).
func TestHelpFlagsMatchReadme(t *testing.T) {
	_, help, code, _ := runGiv(t, nil, "--help")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?:^|[\s\[|` + "`" + `(])(--[a-z][a-z-]*)`)
	set := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, x := range re.FindAllStringSubmatch(s, -1) {
			m[x[1]] = true
		}
		return m
	}
	h, r := set(help), set(string(readme))
	delete(r, "--help") // flag standard, non elencato nell'help
	for f := range h {
		if !r[f] {
			t.Errorf("flag nell'help ma non nel README: %s", f)
		}
	}
	for f := range r {
		if !h[f] {
			t.Errorf("flag nel README ma non nell'help: %s", f)
		}
	}
}

// F2-T5: l'esempio dell'ultimo capitolo del README passa giv story check
// (catalogo finto, nessun avvio).
func TestEsempioSpotStoryCheck(t *testing.T) {
	n := capsServer(t)
	out, se, code, starts := storyRun(t, "../../examples/spot/storyboard.json", n)
	if code != 0 || starts != 0 {
		t.Fatalf("code=%d avvii=%d %s", code, starts, se)
	}
	var p struct {
		Scenes       []json.RawMessage `json:"scenes"`
		CostEstUSD   float64           `json:"cost_est_usd"`
		CostTotalUSD float64           `json:"cost_total_usd"`
		CapUSD       float64           `json:"cap_usd"`
		NotEstimated []json.RawMessage `json:"not_estimated"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(p.Scenes) != 4 || p.CostEstUSD != 1.5 || p.CostTotalUSD != 1.5 || p.CapUSD != 3 || len(p.NotEstimated) != 0 {
		t.Errorf("piano inatteso: %s", out)
	}
}

// F2-T5: giv story cut sull'esempio senza stato: clip mancanti, codice 2.
func TestEsempioSpotCutSenzaClip(t *testing.T) {
	board, err := filepath.Abs("../../examples/spot/storyboard.json")
	if err != nil {
		t.Fatal(err)
	}
	_, se, code, _ := runGiv(t, nil, "story", "cut", board)
	if code != 2 || !strings.Contains(se, "clip mancanti") || !strings.Contains(se, "s1") {
		t.Fatalf("code=%d %s", code, se)
	}
}

// CLI-886: i flag degli help di giv story e dei suoi sottocomandi sono nel README
// (compresi --scene e --new-keyframe) e quelli di story nel README sono negli help.
func TestStoryHelpFlagsInReadme(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?:^|[\s\[|` + "`" + `(])(--[a-z][a-z-]*)`)
	var help strings.Builder
	for _, a := range [][]string{{"story", "help"}, {"story", "--help"}, {"story", "check", "--help"}, {"story", "run", "--help"}, {"story", "cut", "--help"}} {
		so, se, code, _ := runGiv(t, nil, a...)
		if code != 0 {
			t.Fatalf("%v: exit %d %s", a, code, se)
		}
		help.WriteString(so + se)
	}
	h := map[string]bool{}
	for _, x := range re.FindAllStringSubmatch(help.String(), -1) {
		h[x[1]] = true
	}
	delete(h, "--help")
	for _, f := range []string{"--scene", "--new-keyframe"} {
		if !h[f] {
			t.Errorf("flag assente dagli help di story: %s", f)
		}
	}
	for f := range h {
		if !strings.Contains(string(readme), f) {
			t.Errorf("flag negli help di story ma non nel README: %s", f)
		}
	}
	if !strings.Contains(string(readme), "story run [--scene <id> [--new-keyframe]]") {
		t.Error("README senza la riga d'uso di story run con --new-keyframe")
	}
}
