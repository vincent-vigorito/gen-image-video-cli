package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// P-R (CLI-1000): giv qc --voice --dry-run conta i nomi senza doppioni
// (--nome e kit, stessa chiave: minuscole e spazi tolti). Nessuna richiesta.
func TestPR1QCVoiceNomiSenzaDoppioni(t *testing.T) {
	hits, dir := voiceEnv(t, sttResp("x"))
	aud := qcAudio(t, dir)
	nomi := func(extra ...string) float64 {
		t.Helper()
		out, se, code := qcVoiceRun(t, append([]string{aud, "--script", "x", "--dry-run"}, extra...)...)
		if code != 0 {
			t.Fatalf("code=%d %s", code, se)
		}
		var r struct{ Params map[string]any }
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(err)
		}
		n, _ := r.Params["nomi"].(float64)
		return n
	}
	if n := nomi("--nome", "Swerpify", "--nome", " swerpify "); n != 1 {
		t.Errorf("due --nome uguali: nomi=%v, atteso 1", n)
	}
	d := v3Dir(t, strings.Replace(v3Kit, `"pronuncia"`, `"nomi": ["swerpify", "Anja"], "pronuncia"`, 1))
	if n := nomi("--nome", "Swerpify", "--kit", filepath.Join(d, "kit.json")); n != 2 {
		t.Errorf("--nome + kit con lo stesso nome: nomi=%v, atteso 2", n)
	}
	if *hits != 0 {
		t.Errorf("richieste=%d", *hits)
	}
}
