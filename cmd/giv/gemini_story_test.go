package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func gemStory(t *testing.T, tetto string, args ...string) (so, se string, code int) {
	t.Helper()
	board := `{"default":{"provider":"gemini","modello":"gemini-omni-1.2-flash","risoluzione":"720p","aspect":"16:9"}` + tetto + `,"scene":[{"id":"s1","tipo":"video","durata":10,"prompt":"a cat"}]}`
	os.WriteFile("spot.json", []byte(board), 0o644)
	grab := func(f **os.File) func() string {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		return func() string { w.Close(); *f = old; return <-done }
	}
	a, b := grab(&os.Stdout), grab(&os.Stderr)
	err := cmdStory(append([]string{"run"}, append(args, "spot.json")...))
	so, se = a(), b()
	if err != nil {
		code, _ = exitCode(err)
		se += err.Error()
	}
	return
}

// chargeEst legge la stima registrata nello stato per la scena s1 (somma delle Charge).
func chargeEst(t *testing.T) float64 {
	t.Helper()
	b, err := os.ReadFile("spot.stato.json")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Scenes map[string]struct {
			Charges []struct {
				Est *float64 `json:"cost_est_usd"`
			}
		}
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	var sum float64
	for _, c := range st.Scenes["s1"].Charges {
		if c.Est != nil {
			sum += *c.Est
		}
	}
	return sum
}

// Scena Gemini conclusa con contatori incompleti, incoerenti o con costo piu'
// basso della riserva: il conto nello stato non scende sotto la riserva da
// listino (1.2864), e un --scene successivo al limite del tetto e' fermato
// prima di ogni POST (1.2864 + 1.2864 > 2.0; con 0.695 passerebbe).
func TestGeminiStoryContoNonScendeSottoRiserva(t *testing.T) {
	for _, mode := range []string{"inline", "usagepoor", "usagemismatch"} {
		t.Run(mode, func(t *testing.T) {
			g := newFakeGem(t, mode)
			if _, se, code := gemStory(t, `,"tetto_costo_usd":2.0`); code != 0 || g.posts() != 1 {
				t.Fatalf("run: code=%d posts=%d\n%s", code, g.posts(), se)
			}
			if got := chargeEst(t); got < 1.286 {
				t.Fatalf("conto %v sceso sotto la riserva 1.2864", got)
			}
			_, se, code := gemStory(t, `,"tetto_costo_usd":2.0`, "--scene", "s1")
			if code != 2 || g.posts() != 1 {
				t.Fatalf("--scene al limite: code=%d posts=%d\n%s", code, g.posts(), se)
			}
		})
	}
}

// Stesso dopo un recupero: job gia' partito (errore di scaricamento nella
// run precedente) recuperato da story run con usage a costo piu' basso.
func TestGeminiStoryRecuperoContoNonScendeSottoRiserva(t *testing.T) {
	g := newFakeGem(t, "getinline")
	st := fmt.Sprintf(`{"version":1,"scenes":{"s1":{"status":"partito","job":{"provider":"gemini","id":"int-1","polling_url":%q,"model":"gemini-omni-1.2-flash","started":"2026-01-01T00:00:00Z"},"charges":[{"kind":"clip","job_id":"int-1","cost_est_usd":1.2864075,"at":"2026-01-01T00:00:00Z"}]}}}`,
		g.srv.URL+"/interactions/int-1")
	os.WriteFile("spot.stato.json", []byte(st), 0o600)
	so, se, code := gemStory(t, `,"tetto_costo_usd":2.0`)
	if code != 0 || g.posts() != 0 || !strings.Contains(so, `"jobs_recovered": 1`) {
		t.Fatalf("recupero: code=%d posts=%d\n%s\n%s", code, g.posts(), so, se)
	}
	if got := chargeEst(t); got < 1.286 {
		t.Fatalf("conto dopo recupero %v sceso sotto la riserva", got)
	}
	_, se, code = gemStory(t, `,"tetto_costo_usd":2.0`, "--scene", "s1")
	if code != 2 || g.posts() != 0 {
		t.Fatalf("--scene al limite: code=%d posts=%d\n%s", code, g.posts(), se)
	}
}
