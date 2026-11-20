package main

import (
	"strings"
	"testing"
)

// Caso 3: controlli non validi rifiutati con codice 2, prima di qualsiasi chiamata.
func TestVideoRifiutiNuoviFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"audio non on/off", []string{"--audio", "forse"}},
		{"param senza =", []string{"--param", "senzauguale"}},
		{"ref inesistente", []string{"--ref", "/nonexistent/ref.png"}},
	}
	for _, prov := range []string{"xai", "openrouter"} {
		for _, c := range cases {
			t.Run(prov+"/"+c.name, func(t *testing.T) {
				args := append([]string{"video", "--provider", prov, "--model", "m"}, c.args...)
				args = append(args, "un gatto")
				_, se, code, hits := runGiv(t, nil, args...)
				if code != 2 || hits != 0 {
					t.Fatalf("code=%d hits=%d, attesi 2/0\n%s", code, hits, se)
				}
				if strings.Contains(se, "not defined") {
					t.Errorf("flag non implementato: %s", se)
				}
			})
		}
	}
}
