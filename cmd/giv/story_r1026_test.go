package main

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

// CLI-1028 (giv 0.13.1, filone A): tetto del singolo giro (--max-cost),
// scene da approvare dopo --keyframes, nomi dei fotogrammi, addebito del
// giudizio. Solo server finti (parEnv); nessuna chiamata a pagamento.

// nessuna richiesta a pagamento è partita
func (e *parEnv) noSpend(t *testing.T) {
	t.Helper()
	if e.posts != 0 || e.images != 0 || e.polls != 0 || e.judges != 0 {
		t.Errorf("richieste a pagamento partite: posts=%d images=%d polls=%d giudizi=%d", e.posts, e.images, e.polls, e.judges)
	}
}

// campi numerici dell'uscita JSON il cui nome contiene "cap" (tetto)
func capFields(t *testing.T, so string) map[string]float64 {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(so), &m); err != nil {
		t.Fatalf("uscita non JSON: %v\n%s", err, so)
	}
	r := map[string]float64{}
	for k, v := range m {
		if f, ok := v.(float64); ok && strings.Contains(k, "cap") {
			r[k] = f
		}
	}
	return r
}

// --max-cost sotto la stima del giro: codice 2, nessuna richiesta a pagamento.
func TestR1026MaxCostSottoLaStima(t *testing.T) {
	e := newParEnv(t, ib("", "", ps("s1", "")), false)
	_, se, code := e.rawRun(t, "--max-cost", "0.3")
	if code != 2 {
		t.Fatalf("code=%d\n%s", code, se)
	}
	e.noSpend(t)
}

// --max-cost abbastanza alto: il giro parte; il risultato riporta il tetto
// del giro (2) e lo speso (0,5).
func TestR1026MaxCostAbbastanza(t *testing.T) {
	e := newParEnv(t, ib("", "", ps("s1", "")), false)
	so, se, code := e.rawRun(t, "--max-cost", "2")
	if code != 0 || e.posts != 1 {
		t.Fatalf("code=%d posts=%d\n%s\n%s", code, e.posts, so, se)
	}
	found := false
	for k, v := range capFields(t, so) {
		if k != "cap_usd" && math.Abs(v-2) < 1e-9 {
			found = true
		}
	}
	if !found {
		t.Errorf("tetto del giro (2) assente dall'uscita: %s", so)
	}
	var m map[string]any
	json.Unmarshal([]byte(so), &m)
	if f, _ := m["run_spent_usd"].(float64); math.Abs(f-0.5) > 1e-9 {
		t.Errorf("run_spent_usd=%v, atteso 0.5", m["run_spent_usd"])
	}
}

// due scene da 0,5 $: una per una stanno in 0,7 $, insieme no. Con
// --parallel 2 la seconda non parte.
func TestR1026DueSceneNonInsiemeParallelo(t *testing.T) {
	e := newParEnv(t, ib("", "", ps("s1", ""), ps("s2", "")), false)
	_, se, code := e.rawRun(t, "--max-cost", "0.7", "--parallel", "2")
	_ = code
	if e.posts != 1 {
		t.Fatalf("posts=%d, atteso 1 (la seconda scena non deve partire)\n%s", e.posts, se)
	}
	if !strings.Contains(e.bodies[0], "p-s1") {
		t.Errorf("è partita la scena sbagliata: %s", e.bodies[0])
	}
}

// vince sempre il tetto più stretto: storyboard 0,7 con --max-cost 5, e
// storyboard 5 con --max-cost 0,7.
func TestR1026VinceIlPiuStretto(t *testing.T) {
	e := newParEnv(t, ib("", `,"tetto_costo_usd":0.7`, ps("s1", ""), ps("s2", "")), false)
	e.rawRun(t, "--max-cost", "5", "--parallel", "2")
	if e.posts != 1 {
		t.Errorf("tetto storyboard più stretto: posts=%d, atteso 1", e.posts)
	}
	e2 := newParEnv(t, ib("", `,"tetto_costo_usd":5`, ps("s1", ""), ps("s2", "")), false)
	e2.rawRun(t, "--max-cost", "0.7", "--parallel", "2")
	if e2.posts != 1 {
		t.Errorf("--max-cost più stretto: posts=%d, atteso 1", e2.posts)
	}
}

// costo non stimabile con --max-cost: codice 2 prima di ogni avvio.
func TestR1026MaxCostNonStimabile(t *testing.T) {
	e := newParEnv(t, ib("", "", ps("s1", "")), true)
	_, se, code := e.rawRun(t, "--max-cost", "5")
	if code != 2 {
		t.Fatalf("code=%d\n%s", code, se)
	}
	e.noSpend(t)
}

// valore 0, negativo o non numerico: codice 2, nessuna richiesta.
func TestR1026MaxCostValoreNonValido(t *testing.T) {
	for _, v := range []string{"0", "-1", "abc"} {
		e := newParEnv(t, ib("", "", ps("s1", "")), false)
		_, se, code := e.rawRun(t, "--max-cost", v)
		if code != 2 {
			t.Errorf("--max-cost %s: code=%d\n%s", v, code, se)
		}
		e.noSpend(t)
	}
}

// --max-cost con --bozza: rispettato (bozza da 0,25 $ non sta in 0,1).
func TestR1026MaxCostConBozza(t *testing.T) {
	e := newParEnv(t, ib(`,"bozza":{"modello":"alibaba/wan-3.0","risoluzione":"480p"}`, "", ps("s1", ""), ps("s2", "")), false)
	e.cost = 0.25
	_, se, code := e.rawRun(t, "--bozza", "--max-cost", "0.1")
	if code != 2 {
		t.Fatalf("code=%d\n%s", code, se)
	}
	e.noSpend(t)
}

// --max-cost con --keyframes: due fotogrammi da 0,14 $: con 0,2 $ ne parte
// uno solo; con 0,1 $ nessuno.
func TestR1026MaxCostConKeyframes(t *testing.T) {
	e := newParEnv(t, ib("", `,"approvazione":true`, ps("s1", keyfr), ps("s2", keyfr)), false)
	e.rawRun(t, "--keyframes", "--max-cost", "0.2")
	if e.images != 1 {
		t.Errorf("immagini=%d con tetto 0,2 $, atteso 1", e.images)
	}
	e2 := newParEnv(t, ib("", `,"approvazione":true`, ps("s1", keyfr)), false)
	_, se, code := e2.rawRun(t, "--keyframes", "--max-cost", "0.1")
	if code != 2 || e2.images != 0 {
		t.Errorf("tetto 0,1 $: code=%d immagini=%d\n%s", code, e2.images, se)
	}
}

// i giudizi di aderenza contano nel tetto del giro: il video (0,5) ci sta in
// 0,5005, il giudizio no.
func TestR1026MaxCostContaIGiudizi(t *testing.T) {
	e := newParEnv(t, ib("", `,"controllo":{"aderenza":true}`, ps("s1", "")), false)
	e.withJudge(t)
	e.rawRun(t, "--max-cost", "0.5005")
	if e.posts != 1 || e.judges != 0 {
		t.Errorf("posts=%d giudizi=%d, attesi 1 e 0", e.posts, e.judges)
	}
}

// --keyframes con approvazione sul fotogramma: le scene sono in
// awaiting_approval.
func TestR1026KeyframesAwaitingApproval(t *testing.T) {
	e := newParEnv(t, ib("", `,"approvazione":true`, ps("s1", keyfr), ps("s2", keyfr)), false)
	so, se, code := e.rawRun(t, "--keyframes")
	if code != 0 || e.images != 2 || e.posts != 0 {
		t.Fatalf("code=%d images=%d posts=%d\n%s", code, e.images, e.posts, se)
	}
	var out struct {
		Awaiting []string `json:"awaiting_approval"`
	}
	if err := json.Unmarshal([]byte(so), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Join(out.Awaiting, ",") != "s1,s2" {
		t.Errorf("awaiting_approval=%v, attese s1,s2\n%s", out.Awaiting, so)
	}
}

func pngs(e *parEnv) map[string]bool {
	r := map[string]bool{}
	for _, f := range listFiles(e.dir) {
		if strings.HasSuffix(f, ".png") {
			r[f] = true
		}
	}
	return r
}

// due fotogrammi della stessa scena nello stesso secondo: file distinti,
// nessun errore.
func TestR1026FotogrammiStessoSecondo(t *testing.T) {
	e := newParEnv(t, ib("", `,"approvazione":true`, ps("s1", keyfr)), false)
	if _, se, code := e.rawRun(t, "--keyframes"); code != 0 || e.images != 1 {
		t.Fatalf("keyframes: code=%d images=%d\n%s", code, e.images, se)
	}
	so, se := grabFile(&os.Stdout), grabFile(&os.Stderr)
	err := cmdStory([]string{"approve", e.board, "s1"})
	so()
	if es := se(); err != nil {
		t.Fatalf("approve: %v %s", err, es)
	}
	before := pngs(e)
	// nessuna pausa: stesso secondo del primo
	if out, se2, code := e.rawRun(t, "--scene", "s1", "--new-keyframe"); code != 0 || e.images != 2 {
		t.Fatalf("code=%d images=%d\n%s\n%s", code, e.images, out, se2)
	}
	after := pngs(e)
	if len(after) != len(before)+1 {
		t.Errorf("file png prima %d, dopo %d: attesi due fotogrammi distinti\n%v", len(before), len(after), after)
	}
}

// addebito del giudizio: stima e costo dai consumi si distinguono nello stato;
// uno stato 0.13.0 senza l'informazione si apre e dà gli stessi totali.
func judgeCharge(t *testing.T, e *parEnv) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(e.statePath())
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Scenes map[string]struct {
			Charges []map[string]any `json:"charges"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	for _, s := range st.Scenes {
		for _, c := range s.Charges {
			if c["kind"] == "giudizio" {
				return c
			}
		}
	}
	t.Fatal("nessun addebito di giudizio nello stato")
	return nil
}

func TestR1026GiudizioStimaOConsumi(t *testing.T) {
	run := func(use string) map[string]any {
		e := newParEnv(t, ib("", `,"tetto_costo_usd":3,"controllo":{"aderenza":true}`, ps("s1", "")), false)
		e.withJudge(t)
		e.judgeUse = use
		if so, se, code := e.rawRun(t); code != 0 || e.judges != 1 {
			t.Fatalf("code=%d giudizi=%d\n%s\n%s", code, e.judges, so, se)
		}
		return judgeCharge(t, e)
	}
	known := map[string]bool{"kind": true, "cost_est_usd": true, "cost_usd": true, "at": true}
	extra := func(c map[string]any) string {
		var r []string
		for k, v := range c {
			if !known[k] {
				b, _ := json.Marshal(v)
				r = append(r, k+"="+string(b))
			}
		}
		return strings.Join(r, ",")
	}
	cons := run(``)                           // consumi completi
	stima := run(`{"promptTokenCount":1000}`) // consumi incompleti: resta la stima
	if extra(cons) == extra(stima) {
		t.Errorf("stima e consumi non distinguibili nello stato:\nconsumi=%v\nstima=%v", cons, stima)
	}
}

// stato 0.13.0 (addebito di giudizio senza l'informazione nuova): si apre e
// i totali non cambiano.
func TestR1026StatoVecchioStessiTotali(t *testing.T) {
	e := newParEnv(t, ib("", `,"tetto_costo_usd":3,"controllo":{"aderenza":true}`, ps("s1", "")), false)
	e.withJudge(t)
	if so, se, code := e.rawRun(t); code != 0 || e.judges != 1 {
		t.Fatalf("code=%d\n%s\n%s", code, so, se)
	}
	spent := func() float64 {
		so, se, code := e.rawRun(t) // scena già fatta: nessun nuovo invio
		if code != 0 || e.posts != 1 || e.judges != 1 {
			t.Fatalf("rerun: code=%d posts=%d giudizi=%d\n%s\n%s", code, e.posts, e.judges, so, se)
		}
		var m struct {
			Spent float64 `json:"spent_usd"`
		}
		if err := json.Unmarshal([]byte(so), &m); err != nil {
			t.Fatalf("%v\n%s", err, so)
		}
		return m.Spent
	}
	want := spent()
	raw, _ := os.ReadFile(e.statePath())
	var st map[string]any
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	for _, s := range st["scenes"].(map[string]any) {
		for _, c := range s.(map[string]any)["charges"].([]any) {
			cm := c.(map[string]any)
			for k := range cm {
				if k != "kind" && k != "cost_est_usd" && k != "cost_usd" && k != "at" {
					delete(cm, k)
				}
			}
		}
	}
	raw, _ = json.Marshal(st)
	if err := os.WriteFile(e.statePath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := spent(); math.Abs(got-want) > 1e-12 || want <= 0.5 {
		t.Errorf("totale con stato 0.13.0 %v, atteso %v (>0.5)", got, want)
	}
}
