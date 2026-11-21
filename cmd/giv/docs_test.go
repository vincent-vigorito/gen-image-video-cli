package main

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// F1-T6: versione 0.8.0 e help <-> README coerenti, flag per flag.
func TestVersion080(t *testing.T) {
	so, _, code, _ := runGiv(t, nil, "version")
	if code != 0 || strings.TrimSpace(so) != "giv 0.8.0" {
		t.Fatalf("code=%d out=%q", code, so)
	}
}

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
		Scenes       []json.RawMessage `json:"scene"`
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
