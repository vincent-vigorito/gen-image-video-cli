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

// Le riprese esistenti (stati 0.12) risultano definitive: stessa clip, job,
// spesa; nessuna bozza né approvazione inventate.
func TestBaseRiprese012SonoDefinitive(t *testing.T) {
	st, err := LoadState(scriviStato(t, stato0121))
	if err != nil {
		t.Fatal(err)
	}
	uno, due := st.Scenes["uno"], st.Scenes["due"]
	if got := uno.TrackOf(TakeFinal); got.Status != Fatto || got.Clip == nil || got.Clip.File != "clip/uno.mp4" {
		t.Fatalf("definitivo di uno: %+v", got)
	}
	if uno.Current(TakeFinal) == nil || uno.Current(TakeFinal).JobID != "job-1" {
		t.Fatal("clip definitiva senza il suo job")
	}
	if got := due.TrackOf(TakeFinal); got.Status != Partito || got.Job == nil || got.Job.ID != "job-2" {
		t.Fatalf("job da recuperare di due: %+v", got)
	}
	if uno.Draft != nil || uno.Approval != nil || uno.Current(TakeDraft) != nil {
		t.Fatal("una scena 0.12 non ha bozza né approvazione")
	}
	if !close2(uno.SpentOn(ChargeClip), 1.75) || !close2(uno.SpentOn(ChargeKeyframe), 0.25) {
		t.Fatal("spesa per tipo diversa")
	}
}

// Addebito che sostituisce la riserva, riserve aperte, scene tolte: somma
// esatta, una voce per richiesta, nessun arrotondamento.
func TestBaseLedgerSommaEsatta(t *testing.T) {
	st := newState(filepath.Join(t.TempDir(), "x.stato.json"))
	a := st.Scene("a")
	k := a.Reserve(Charge{Kind: ChargeClip, CostEstUSD: ptr(1.5)})
	if l := a.Ledger(); !close2(l.TotalUSD, 1.5) || !close2(l.ReservedUSD, 1.5) {
		t.Fatalf("riserva aperta: %+v", l)
	}
	a.Settle(k, 1.75) // l'addebito prende il posto della stima
	if l := a.Ledger(); !close2(l.TotalUSD, 1.75) || !close2(l.ChargedUSD, 1.75) || l.ReservedUSD != 0 || len(a.Charges) != 1 {
		t.Fatalf("addebito al posto della riserva: %+v", l)
	}
	b := st.Scene("b")
	b.Reserve(Charge{Kind: ChargeDraft, CostEstUSD: ptr(0.1)})
	k2 := b.Reserve(Charge{Kind: ChargeKeyframe, CostEstUSD: ptr(0.07)})
	b.MarkUncertain(k2) // incerta: resta contata
	k3 := b.Reserve(Charge{Kind: ChargeClip, CostEstUSD: ptr(9)})
	b.Release(k3)                                                           // mai partita: non conta
	st.Scene("tolta").Charges = []Charge{{Kind: ChargeClip, CostUSD: 0.33}} // scena non più nello storyboard
	l := st.Ledger()
	want := 1.75 + 0.1 + 0.07 + 0.33
	if !close2(l.TotalUSD, want) || !close2(st.Spent(), want) {
		t.Fatalf("totale %v, atteso %v", l.TotalUSD, want)
	}
	if !close2(l.ReservedUSD, 0.17) || !close2(l.ChargedUSD, 2.08) || !close2(l.ChargedUSD+l.EstimatedUSD+l.ReservedUSD, l.TotalUSD) {
		t.Fatalf("parti del conto: %+v", l)
	}
}

func ptr(v float64) *float64 { return &v }

// Campi nuovi fuori dominio: lo stato è rifiutato (nessun avvio).
func TestBaseStatoFuoriDominioRifiutato(t *testing.T) {
	sha := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	casi := map[string]string{
		"tipo di ripresa ignoto (previous della bozza)": `{"version":1,"scenes":{"s":{"status":"fatto","clip":{"file":"c.mp4","done":"2026-11-01T10:00:00Z"},
			"draft":{"status":"fatto","clip":{"file":"b.mp4","done":"2026-11-01T10:00:00Z"},"previous":[{"kind":"fotogramma","file":"k.png","done":"2026-11-01T10:00:00Z"}]}}}}`,
		"stato della bozza ignoto":             `{"version":1,"scenes":{"s":{"status":"da_fare","draft":{"status":"boh"}}}}`,
		"riserva negativa":                     `{"version":1,"scenes":{"s":{"status":"da_fare","charges":[{"kind":"clip","status":"aperta","cost_est_usd":-1,"at":"2026-11-01T10:00:00Z"}]}}}`,
		"stato della riserva ignoto":           `{"version":1,"scenes":{"s":{"status":"da_fare","charges":[{"kind":"clip","status":"mezza","cost_est_usd":1,"at":"2026-11-01T10:00:00Z"}]}}}`,
		"giudizio senza ripresa":               `{"version":1,"scenes":{"s":{"status":"fatto","clip":{"done":"2026-11-01T10:00:00Z","judgements":[{"status":"fatto","provider":"gemini","result":"ok","at":"2026-11-01T10:00:00Z"}]}}}}`,
		"giudizio con esito ignoto":            `{"version":1,"scenes":{"s":{"status":"fatto","clip":{"file":"c.mp4","done":"2026-11-01T10:00:00Z","judgements":[{"status":"fatto","provider":"gemini","result":"forse","at":"2026-11-01T10:00:00Z"}]}}}}`,
		"approvazione con file fuori cartella": `{"version":1,"scenes":{"s":{"status":"da_fare","approval":{"take":"bozza","file":"../x.mp4","sha256":"` + sha + `","recipe":"` + sha + `","at":"2026-11-01T10:00:00Z"}}}}`,
		"approvazione di un tipo ignoto":       `{"version":1,"scenes":{"s":{"status":"da_fare","approval":{"take":"definitivo","file":"x.mp4","sha256":"` + sha + `","recipe":"` + sha + `","at":"2026-11-01T10:00:00Z"}}}}`,
	}
	for nome, js := range casi {
		t.Run(nome, func(t *testing.T) {
			if _, err := LoadState(scriviStato(t, js)); err == nil {
				t.Fatal("stato fuori dominio accettato")
			}
		})
	}
}
