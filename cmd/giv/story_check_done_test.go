package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CLI-996 (giv 0.12.1, filone B): story check con scene già fatte. Server finto
// (capsServer): solo il catalogo, ogni altra richiesta è un avvio e fa fallire.

// nomi dei campi nuovi di story check (da accordare col developer)
const (
	fldSceneWillRun = "will_run"        // per scena: cosa partirebbe
	fldTotalToRun   = "cost_to_run_usd" // totale di ciò che partirebbe
)

const doneBoard = `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"parlato":{"provider":"xai","voce":"eve","lingua":"it"},
"scene":[{"id":"s1","tipo":"video","durata":5,"prompt":"un gatto","testo":"Ciao a tutti da qui."},
{"id":"s2","tipo":"video","durata":5,"prompt":"un cane"},
{"id":"s3","tipo":"video","durata":5,"prompt":"un cavallo","testo":"Arrivederci e a presto."}]}`

type doneEnv struct {
	dir, board string
	starts     *int32
}

func newDoneEnv(t *testing.T) *doneEnv {
	t.Helper()
	e := &doneEnv{starts: capsServer(t)}
	e.dir = t.TempDir()
	t.Setenv("XAI_API_KEY", "fake")
	e.board = filepath.Join(e.dir, "spot.json")
	if err := os.WriteFile(e.board, []byte(doneBoard), 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *doneEnv) statePath() string { return filepath.Join(e.dir, "spot.stato.json") }

func (e *doneEnv) file(t *testing.T, rel string) {
	t.Helper()
	p := filepath.Join(e.dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("X"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// check lancia story check e ritorna il JSON come mappa.
func (e *doneEnv) check(t *testing.T) map[string]any {
	t.Helper()
	out, se, code, starts := storyRun(t, e.board, e.starts)
	if code != 0 || starts != 0 {
		t.Fatalf("code=%d starts=%d\n%s", code, starts, se)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("uscita non JSON: %v\n%s", err, out)
	}
	return m
}

func sceneOf(t *testing.T, m map[string]any, id string) map[string]any {
	t.Helper()
	for _, s := range m["scenes"].([]any) {
		if sm := s.(map[string]any); sm["id"] == id {
			return sm
		}
	}
	t.Fatalf("scena %s assente", id)
	return nil
}

func num(m map[string]any, k string) float64 {
	f, _ := m[k].(float64)
	return f
}

const clipDone = `{"status":"fatto","clip":{"file":"clip/%s.mp4","job_id":"j","done":"2026-01-01T00:00:00Z"}`
const voiceDone = `,"voice":{"kind":"","file":"voce/%s.wav","done":"2026-01-01T00:00:00Z"},"voice_status":"fatto"`

func (e *doneEnv) writeState(t *testing.T, scenes string) {
	t.Helper()
	if err := os.WriteFile(e.statePath(), []byte(`{"version":1,"scenes":{`+scenes+`}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *doneEnv) fullState(t *testing.T) {
	e.file(t, "clip/s1.mp4")
	e.file(t, "clip/s2.mp4")
	e.file(t, "clip/s3.mp4")
	e.file(t, "voce/s1.wav")
	e.file(t, "voce/s3.wav")
	e.writeState(t, strings.ReplaceAll(`"s1":`+clipDone+voiceDone+`},"s2":`+clipDone+`},"s3":`+clipDone+voiceDone+`}`, "%s", "X"))
	// i nomi file per scena
	b, _ := os.ReadFile(e.statePath())
	s := string(b)
	for _, id := range []string{"s1", "s2", "s3"} {
		s = strings.Replace(s, "clip/X.mp4", "clip/"+id+".mp4", 1)
		s = strings.Replace(s, "voce/X.wav", "voce/"+id+".wav", 1)
	}
	os.WriteFile(e.statePath(), []byte(s), 0o600)
}

// senza stato: tutto da fare, ciò che parte = stima dell'intero storyboard.
func TestStoryCheckSenzaStatoTuttoDaFare(t *testing.T) {
	e := newDoneEnv(t)
	m := e.check(t)
	total := num(m, "cost_total_usd")
	if total <= 0 {
		t.Fatalf("stima intera nulla: %v", m)
	}
	if got := num(m, fldTotalToRun); got != total {
		t.Errorf("%s=%v, atteso = stima intera %v", fldTotalToRun, got, total)
	}
	for _, id := range []string{"s1", "s2", "s3"} {
		if sceneOf(t, m, id)[fldSceneWillRun] == nil {
			t.Errorf("scena %s senza %s", id, fldSceneWillRun)
		}
	}
}

// tutto fatto: niente parte, stima intera invariata nei campi esistenti, stato intatto.
func TestStoryCheckTuttoFattoNienteParte(t *testing.T) {
	e := newDoneEnv(t)
	base := e.check(t)
	e.fullState(t)
	before, _ := os.ReadFile(e.statePath())
	m := e.check(t)
	if got := num(m, fldTotalToRun); got != 0 {
		t.Errorf("%s=%v, atteso 0", fldTotalToRun, got)
	}
	for _, k := range []string{"cost_total_usd", "cost_est_usd", "voice_cost_est_usd"} {
		if num(m, k) != num(base, k) {
			t.Errorf("campo esistente %s cambiato: %v -> %v", k, num(base, k), num(m, k))
		}
	}
	for _, id := range []string{"s1", "s2", "s3"} {
		if a, b := num(sceneOf(t, base, id), "cost_est_usd"), num(sceneOf(t, m, id), "cost_est_usd"); a != b {
			t.Errorf("%s cost_est_usd %v -> %v", id, a, b)
		}
	}
	after, _ := os.ReadFile(e.statePath())
	if string(before) != string(after) {
		t.Errorf("story check ha modificato lo stato")
	}
}

// parziale: s1 e la sua voce fatte, s2 da fare, s3 (con voce) da fare:
// parte solo ciò che manca.
func TestStoryCheckParzialeContaSoloCheManca(t *testing.T) {
	e := newDoneEnv(t)
	base := e.check(t)
	e.file(t, "clip/s1.mp4")
	e.file(t, "voce/s1.wav")
	e.writeState(t, `"s1":{"status":"fatto","clip":{"file":"clip/s1.mp4","job_id":"j","done":"2026-01-01T00:00:00Z"},`+
		`"voice":{"file":"voce/s1.wav","done":"2026-01-01T00:00:00Z"},"voice_status":"fatto"}`)
	m := e.check(t)
	if num(m, "cost_total_usd") != num(base, "cost_total_usd") {
		t.Errorf("stima intera cambiata: %v -> %v", num(base, "cost_total_usd"), num(m, "cost_total_usd"))
	}
	s1, s2, s3 := sceneOf(t, base, "s1"), sceneOf(t, base, "s2"), sceneOf(t, base, "s3")
	want := num(s2, "cost_est_usd") + num(s3, "cost_est_usd") + num(s3, "voice_cost_est_usd")
	_ = s1
	if got := num(m, fldTotalToRun); got < want-1e-3 || got > want+1e-3 || got >= num(base, "cost_total_usd") {
		t.Errorf("%s=%v, atteso %v (solo s2, s3 e voce s3)", fldTotalToRun, got, want)
	}
	if v := sceneOf(t, m, "s1")[fldSceneWillRun]; v == sceneOf(t, m, "s2")[fldSceneWillRun] {
		t.Errorf("s1 (fatta) e s2 (da fare) dicono lo stesso: %v", v)
	}
	for _, id := range []string{"s1", "s2", "s3"} {
		if sceneOf(t, m, id)[fldSceneWillRun] == nil {
			t.Errorf("scena %s senza %s", id, fldSceneWillRun)
		}
	}
}
