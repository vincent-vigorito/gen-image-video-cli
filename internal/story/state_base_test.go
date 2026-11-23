package story

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Stato della 0.12.1: voice_check con comparison. Una scena fatta con spesa e
// job, una con job partito, una scena rimossa dallo storyboard ma con addebito.
const stato0121 = `{
 "version": 1,
 "scenes": {
  "uno": {"status":"fatto","clip":{"file":"clip/uno.mp4","job_id":"job-1","done":"2026-11-01T10:00:00Z"},
          "keyframe":{"file":"kf/uno.png","done":"2026-11-01T09:00:00Z"},
          "charges":[{"kind":"fotogramma","cost_usd":0.25,"at":"2026-11-01T09:00:00Z"},
                     {"kind":"clip","job_id":"job-1","cost_est_usd":1.5,"cost_usd":1.75,"at":"2026-11-01T10:00:00Z"}],
          "voice":{"file":"voce/uno.mp3","done":"2026-11-01T10:05:00Z"},"voice_status":"fatto",
          "voice_check":{"file":"voce/uno.mp3","status":"fatto","result":"ok","at":"2026-11-01T10:06:00Z",
                         "comparison":{"words":3,"matched":3,"ratio":1,"different":[],"missing":[],"extra":[]}}},
  "due": {"status":"partito","job":{"provider":"xai","id":"job-2","started":"2026-11-01T11:00:00Z"},
          "charges":[{"kind":"clip","job_id":"job-2","cost_declared_usd":2,"at":"2026-11-01T11:00:00Z"}]},
  "rimossa": {"status":"fallito","error":"x","charges":[{"kind":"clip","cost_usd":0.5,"at":"2026-11-01T12:00:00Z"}]}
 }
}`

// Stato della 0.12.0: nessun voice_check.comparison.
const stato0120 = `{
 "version": 1,
 "scenes": {
  "uno": {"status":"fatto","clip":{"file":"clip/uno.mp4","job_id":"job-1","done":"2026-10-01T10:00:00Z"},
          "charges":[{"kind":"clip","cost_usd":1.75,"at":"2026-10-01T10:00:00Z"}],
          "voice":{"file":"voce/uno.mp3","done":"2026-10-01T10:05:00Z"},"voice_status":"fatto",
          "voice_check":{"file":"voce/uno.mp3","status":"fatto","result":"avviso","at":"2026-10-01T10:06:00Z"}}
 }
}`

func scriviStato(t *testing.T, contenuto string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "spot.stato.json")
	if err := os.WriteFile(p, []byte(contenuto), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func close2(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Uno stato 0.12.1 e uno 0.12.0 si aprono, la spesa è quella di prima e la
// sola lettura non riscrive il file.
func TestBaseStatiVecchiSiApronoSenzaRiscrittura(t *testing.T) {
	casi := []struct {
		nome, json string
		speso      float64
	}{
		{"0.12.1", stato0121, 1.75 + 0.25 + 2 + 0.5},
		{"0.12.0", stato0120, 1.75},
	}
	for _, c := range casi {
		t.Run(c.nome, func(t *testing.T) {
			p := scriviStato(t, c.json)
			prima, _ := os.ReadFile(p)
			fi1, _ := os.Stat(p)
			st, err := LoadState(p)
			if err != nil {
				t.Fatalf("apertura: %v", err)
			}
			if !close2(st.Spent(), c.speso) {
				t.Fatalf("spesa %v, attesa %v", st.Spent(), c.speso)
			}
			dopo, _ := os.ReadFile(p)
			fi2, _ := os.Stat(p)
			if !bytes.Equal(prima, dopo) || !fi1.ModTime().Equal(fi2.ModTime()) {
				t.Fatal("la sola lettura ha riscritto il file")
			}
		})
	}
}

// Le scene esistenti restano come erano: job, clip, spesa per scena.
func TestBaseStato0121ConservaJobESpesaPerScena(t *testing.T) {
	st, err := LoadState(scriviStato(t, stato0121))
	if err != nil {
		t.Fatal(err)
	}
	if st.Scenes["uno"].Status != Fatto || st.Scenes["uno"].Clip.JobID != "job-1" {
		t.Fatalf("scena uno: %+v", st.Scenes["uno"])
	}
	if j := st.Scenes["due"].Job; st.Scenes["due"].Status != Partito || j == nil || j.ID != "job-2" {
		t.Fatalf("scena due: %+v", st.Scenes["due"])
	}
	if !close2(st.Scenes["uno"].Spent(), 2.0) || !close2(st.Scenes["due"].Spent(), 2) || !close2(st.Scenes["rimossa"].Spent(), 0.5) {
		t.Fatal("spesa per scena diversa dall'attesa")
	}
}
