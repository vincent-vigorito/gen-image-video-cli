package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// CLI-1025 (giv 0.13.0): tetto di spesa e stima dei giudizi in story check,
// contro cataloghi finti (parEnv): check non fa richieste a pagamento.

// ps16 è una scena video da 16 s: 1,6 $ col listino del catalogo di prova.
func ps16(id, extra string) string {
	return fmt.Sprintf(`{"id":"%s","tipo":"video","durata":16,"prompt":"p-%s"%s}`, id, id, extra)
}

func (e *parEnv) check(t *testing.T) (stdout, errOut string, code int) {
	t.Helper()
	so, se := grabFile(&os.Stdout), grabFile(&os.Stderr)
	err := cmdStory([]string{"check", e.board})
	stdout, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

func (e *parEnv) setBoard(t *testing.T, b string) {
	t.Helper()
	if err := os.WriteFile(e.board, []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stato con 1,5 $ registrati, una scena approvata (1,6 $) e due no.
func approvedScenario(t *testing.T, cap string) *parEnv {
	t.Helper()
	board := func(c string) string {
		return ib("", `,"approvazione":true`+c, ps16("s1", keyfr), ps16("s2", keyfr), ps16("s3", keyfr))
	}
	e := newParEnv(t, board(""), false)
	if _, se, code := e.rawRun(t, "--keyframes"); code != 0 || e.images != 3 {
		t.Fatalf("keyframes: code=%d images=%d\n%s", code, e.images, se)
	}
	so, se := grabFile(&os.Stdout), grabFile(&os.Stderr)
	err := cmdStory([]string{"approve", e.board, "s1"})
	so()
	if es := se(); err != nil {
		t.Fatalf("approve: %v %s", err, es)
	}
	// 0,42 $ di fotogrammi già registrati + 1,08 $ di una scena tolta = 1,5 $
	raw, _ := os.ReadFile(e.statePath())
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	st["scenes"].(map[string]any)["old"] = map[string]any{"status": "fallito", "charges": []any{
		map[string]any{"kind": "clip", "cost_est_usd": 1.08, "cost_usd": 1.08, "at": "2026-01-01T00:00:00Z"}}}
	raw, _ = json.Marshal(st)
	if err := os.WriteFile(e.statePath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	e.setBoard(t, board(cap))
	return e
}

// il giro di adesso (scena approvata) ci sta nel tetto 3,2 $, l'intero
// storyboard no: check esce 0, avvisa e lo riporta nel JSON.
func TestTettoCheckGiroEntroIlTettoStoryboardOltre(t *testing.T) {
	e := approvedScenario(t, `,"tetto_costo_usd":3.2`)
	so, se, code := e.check(t)
	if code != 0 {
		t.Fatalf("code=%d\n%s\n%s", code, so, se)
	}
	if !strings.Contains(se, "intero storyboard") {
		t.Errorf("manca l'avviso sull'intero storyboard: %s", se)
	}
	var r struct {
		Over bool `json:"storyboard_over_cap"`
	}
	if err := json.Unmarshal([]byte(so), &r); err != nil || !r.Over {
		t.Errorf("storyboard_over_cap non è true nel JSON (%v): %.200s", err, so)
	}
}

// stesso scenario con tetto 3,0 $: il giro di adesso non ci sta, codice 2.
func TestTettoCheckGiroOltreIlTetto(t *testing.T) {
	e := approvedScenario(t, `,"tetto_costo_usd":3.0`)
	if so, se, code := e.check(t); code != 2 {
		t.Fatalf("code=%d\n%s\n%s", code, so, se)
	}
}

// senza stato né approvazioni: oltre il tetto codice 2, entro 0.
func TestTettoCheckSenzaStatoNeApprovazioni(t *testing.T) {
	scene := []string{ps16("s1", ""), ps16("s2", "")} // 3,2 $
	for cap, want := range map[string]int{"3.0": 2, "3.5": 0} {
		e := newParEnv(t, ib("", `,"tetto_costo_usd":`+cap, scene...), false)
		if so, se, code := e.check(t); code != want {
			t.Errorf("tetto %s: code=%d atteso %d\n%s\n%s", cap, code, want, so, se)
		}
	}
}

// aderenza accesa: la stima dei giudizi compare in check ed è nella stima del giro.
func TestTettoCheckStimaDeiGiudizi(t *testing.T) {
	scene := []string{ps16("s1", ""), ps16("s2", "")}
	off := newParEnv(t, ib("", `,"tetto_costo_usd":10`, scene...), false)
	so0, se, code := off.check(t)
	if code != 0 {
		t.Fatalf("senza aderenza: code=%d\n%s", code, se)
	}
	on := newParEnv(t, ib("", `,"tetto_costo_usd":10,"controllo":{"aderenza":true}`, scene...), false)
	so1, se, code := on.check(t)
	if code != 0 {
		t.Fatalf("con aderenza: code=%d\n%s", code, se)
	}
	if !strings.Contains(strings.ToLower(so1), "giudiz") && !strings.Contains(strings.ToLower(so1), "judg") {
		t.Errorf("la stima dei giudizi non compare in check: %s", so1)
	}
	var r struct {
		Judge float64 `json:"judge_cost_est_usd"`
		Run   float64 `json:"run_cost_est_usd"`
		Scene []struct {
			Judge float64 `json:"judge_cost_est_usd"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal([]byte(so1), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Scene) != 2 || r.Scene[0].Judge <= 0 || r.Scene[1].Judge <= 0 {
		t.Errorf("judge_cost_est_usd per scena mancante: %+v", r.Scene)
	}
	if r.Run < 3.2+r.Scene[0].Judge+r.Scene[1].Judge-1e-9 {
		t.Errorf("run_cost_est_usd %v non comprende i giudizi", r.Run)
	}
	var r0 struct {
		Run float64 `json:"run_cost_est_usd"`
	}
	if err := json.Unmarshal([]byte(so0), &r0); err != nil {
		t.Fatal(err)
	}
	if r.Run <= r0.Run {
		t.Errorf("stima del giro con aderenza %v non supera quella senza %v", r.Run, r0.Run)
	}
}

// costo non stimabile con tetto: codice 2.
func TestTettoCheckNonStimabile(t *testing.T) {
	e := newParEnv(t, ib("", `,"tetto_costo_usd":5`, ps16("s1", "")), true)
	if so, se, code := e.check(t); code != 2 {
		t.Fatalf("code=%d\n%s\n%s", code, so, se)
	}
}
