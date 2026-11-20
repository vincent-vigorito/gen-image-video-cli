package main

import (
	"strings"
	"testing"
)

// F1-T3: il percorso video Veo di Gemini e' tolto: sempre codice 2, hint OpenRouter, zero rete.
func TestVideoGeminiRemoved(t *testing.T) {
	noKey := []string{"GEMINI_API_KEY="}
	for _, args := range [][]string{
		{"video", "--provider", "gemini", "--model", "veo-3.1", "x"},
		{"video", "--provider", "gemini", "x"},
	} {
		for _, env := range [][]string{noKey, nil} {
			_, se, code, hits := runGiv(t, env, args...)
			if code != 2 || hits != 0 {
				t.Fatalf("%v env=%v: code=%d hits=%d %s", args, env, code, hits, se)
			}
			if !strings.Contains(strings.ToLower(se), "openrouter") {
				t.Errorf("%v: hint OpenRouter mancante: %s", args, se)
			}
		}
	}
}
