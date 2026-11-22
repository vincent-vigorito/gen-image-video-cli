package main

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gen-image-video-cli/internal/montage"
)

// V-T8: sottotitoli automatici, montaggio.senza, controllo dei file e voce
// Gemini col tetto, contro il server finto di V-T7.

func v8Cut(t *testing.T, e *v7Env) map[string]any {
	t.Helper()
	out, se, code := e.story(t, "cut")
	if code != 0 {
		t.Fatalf("cut code=%d\n%s", code, se)
	}
	var r map[string]any
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return r
}

func v8Frame(t *testing.T, file string, at string, w, h int) []byte {
	t.Helper()
	b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-ss", at, "-i", file, "-frames:v", "1",
		"-f", "rawvideo", "-pix_fmt", "gray", "-").Output()
	if err != nil || len(b) != w*h {
		t.Fatalf("fotogramma %s: %v (%d byte, attesi %d)", file, err, len(b), w*h)
	}
	return b
}

// bounding box dei pixel chiari (testo bianco su sfondo nero); n=0: nessuno.
func v8Bright(b []byte, w, h int) (n, x0, y0, x1, y1 int) {
	x0, y0, x1, y1 = w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if b[y*w+x] > 180 {
				n++
				x0, y0, x1, y1 = min(x0, x), min(y0, y), max(x1, x), max(y1, y)
			}
		}
	}
	return
}

func v8Black(t *testing.T, e *v7Env) {
	cutFF(t, "-f", "lavfi", "-i", "color=c=black:s=64x64:d=1", "-frames:v", "1", filepath.Join(e.dir, "a.png"))
}

func v8Board(montaggio, extra string) string {
	return `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"}` + v7Parlato + extra + `,
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},` + montaggio + `
"scene":[{"id":"s1","tipo":"immagine","file":"a.png","durata":4,"testo":"` + v7Text + `"}]}`
}

// sottotitoli "auto": nel montato ci sono, in ogni formato, dentro i margini
// sicuri; senza "auto" il montato non ne ha.
func TestV8SottotitoliAuto(t *testing.T) {
	cutNeedFF(t)
	e := newV7Env(t, v8Board(`"montaggio":{"sottotitoli":"auto","formati":["16:9","9:16"]},`, ""))
	v8Black(t, e)
	if _, se, code := e.story(t, "run", "--voices"); code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	r := v8Cut(t, e)
	outs, _ := r["formats"].([]any)
	if len(outs) != 2 {
		t.Fatalf("outputs: %v", r)
	}
	sv := montage.SafeVertical
	for _, o := range outs {
		m := o.(map[string]any)
		file, _ := m["output"].(string)
		w, h := int(m["width"].(float64)), int(m["height"].(float64))
		n, x0, y0, x1, y1 := v8Bright(v8Frame(t, file, "0.5", w, h), w, h)
		if n == 0 {
			t.Errorf("%s: nessun sottotitolo nel montato", file)
			continue
		}
		if w < h { // verticale: dentro i margini sicuri
			if float64(x0) < sv.Left*float64(w)-1 || float64(x1) > float64(w)*(1-sv.Right)+1 ||
				float64(y0) < sv.Top*float64(h)-1 || float64(y1) > float64(h)*(1-sv.Bottom)+1 {
				t.Errorf("%s: testo fuori dai margini sicuri: x %d-%d y %d-%d su %dx%d", file, x0, x1, y0, y1, w, h)
			}
		}
	}
}

// senza "auto": nessun testo chiaro nel montato.
func TestV8SenzaSottotitoliNienteTesto(t *testing.T) {
	cutNeedFF(t)
	e := newV7Env(t, v8Board("", ""))
	v8Black(t, e)
	if _, se, code := e.story(t, "run", "--voices"); code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	r := v8Cut(t, e)
	w, h := int(r["width"].(float64)), int(r["height"].(float64))
	if n, _, _, _, _ := v8Bright(v8Frame(t, r["output"].(string), "0.5", w, h), w, h); n != 0 {
		t.Errorf("testo nel montato senza sottotitoli: %d pixel", n)
	}
}

// testo con { \ e a capo: reso alla lettera (resta in basso, niente
// direttive ASS interpretate), il montaggio riesce.
func TestV8TestoSpecialeAllaLettera(t *testing.T) {
	cutNeedFF(t)
	b := strings.Replace(v8Board(`"montaggio":{"sottotitoli":"auto"},`, ""), v7Text, `{\\an8}{\\pos(5,5)} ciao \\N a capo\\nsecondo`, 1)
	e := newV7Env(t, b)
	v8Black(t, e)
	if _, se, code := e.story(t, "run", "--voices"); code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	r := v8Cut(t, e)
	w, h := int(r["width"].(float64)), int(r["height"].(float64))
	n, _, y0, _, _ := v8Bright(v8Frame(t, r["output"].(string), "0.5", w, h), w, h)
	if n == 0 {
		t.Fatal("nessun sottotitolo")
	}
	if float64(y0) < float64(h)*0.35 {
		t.Errorf("testo in alto (y0=%d su %d): direttiva interpretata?", y0, h)
	}
}

// "auto" senza testi: codice 2.
func TestV8AutoSenzaTesti(t *testing.T) {
	b := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},"montaggio":{"sottotitoli":"auto"},
"scene":[{"id":"s1","tipo":"immagine","file":"a.png","durata":3}]}`
	e := newV7Env(t, b)
	if _, _, code := e.story(t, "check"); code != 2 {
		t.Errorf("code=%d", code)
	}
}

// montaggio.senza: il kit non mette l'elemento; valori non validi = codice 2.
func TestV8Senza(t *testing.T) {
	cutNeedFF(t)
	e := newV7Env(t, v7Board(v7Parlato))
	inh := func() []string {
		out, se, code := e.story(t, "check")
		if code != 0 {
			t.Fatalf("check code=%d\n%s", code, se)
		}
		var r struct{ Kit struct{ Inherited []string } }
		json.Unmarshal([]byte(out), &r)
		return r.Kit.Inherited
	}
	has := func(l []string, k string) bool {
		for _, x := range l {
			if strings.HasPrefix(x, "montaggio."+k) {
				return true
			}
		}
		return false
	}
	base := inh()
	for _, k := range []string{"logo", "cartello_finale", "musica"} {
		if !has(base, k) {
			t.Fatalf("il kit non porta %s: %v", k, base)
		}
	}
	for _, k := range []string{"logo", "cartello_finale", "musica"} {
		os.WriteFile(e.board, []byte(strings.Replace(v7Board(v7Parlato+`,"montaggio":{"senza":["`+strings.TrimSuffix(k, "_finale")+`"]}`), "KIT", mustRel(t, e), 1)), 0o644)
		got := inh()
		if has(got, k) {
			t.Errorf("senza %s: il kit lo mette ancora: %v", k, got)
		}
		for _, o := range []string{"logo", "cartello_finale", "musica"} {
			if o != k && !has(got, o) {
				t.Errorf("senza %s: %s e' sparito: %v", k, o, got)
			}
		}
	}
	for name, m := range map[string]string{
		"sconosciuto":            `"montaggio":{"senza":["titoli"]}`,
		"ripetuto":               `"montaggio":{"senza":["logo","logo"]}`,
		"anche nello storyboard": `"montaggio":{"senza":["logo"],"logo":{"file":"a.png"}}`,
	} {
		os.WriteFile(e.board, []byte(strings.Replace(v7Board(v7Parlato+","+m), "KIT", mustRel(t, e), 1)), 0o644)
		if _, _, code := e.story(t, "check"); code != 2 {
			t.Errorf("%s: code=%d", name, code)
		}
	}
}

// senza cartello: il montato e' piu' corto di 3 s.
func TestV8SenzaCartelloDurata(t *testing.T) {
	cutNeedFF(t)
	dur := func(montaggio string) float64 {
		e := newV7Env(t, v7Board(v7Parlato+montaggio))
		v8Black(t, e)
		if _, se, code := e.story(t, "run", "--voices"); code != 0 {
			t.Fatalf("run code=%d\n%s", code, se)
		}
		r := v8Cut(t, e)
		return r["duration_s"].(float64)
	}
	con, senza := dur(""), dur(`,"montaggio":{"senza":["cartello"]}`)
	if con-senza < 2.5 {
		t.Errorf("con cartello %v, senza %v", con, senza)
	}
}

// controllo tecnico: run e cut scrivono <nome>.controllo.json (0600), una riga
// per scena, avvisano sui difetti e non fanno richieste a pagamento.
func TestV8ControlloTecnico(t *testing.T) {
	cutNeedFF(t)
	b := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"}` + v7Parlato + `,"controllo":{"tecnico":true},
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},
"scene":[{"id":"s1","tipo":"immagine","file":"a.png","durata":3,"testo":"` + v7Text + `"},
{"id":"s2","tipo":"clip","file":"nera.mp4","durata":3}]}`
	e := newV7Env(t, b)
	cutFF(t, "-f", "lavfi", "-i", "color=c=black:s=256x144:r=25:d=3", "-pix_fmt", "yuv420p", filepath.Join(e.dir, "nera.mp4"))
	out, se, code := e.story(t, "run")
	if code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	if e.tts != 1 || e.videos != 0 {
		t.Fatalf("richieste: tts=%d video=%d", e.tts, e.videos)
	}
	var r struct {
		Controllo *struct {
			Scenes []struct{ ID, Result string }
			Voices []struct{ ID, Result string }
		}
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Controllo == nil {
		t.Fatalf("manca il campo controllo nell'uscita di run: %v\n%s", err, out)
	}
	cf := filepath.Join(e.dir, "spot.controllo.json")
	fi, err := os.Stat(cf)
	if err != nil {
		t.Fatalf("controllo.json non scritto: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("permessi %v", fi.Mode().Perm())
	}
	if len(r.Controllo.Scenes) != 2 || len(r.Controllo.Voices) != 1 {
		t.Errorf("righe: scene=%d voci=%d", len(r.Controllo.Scenes), len(r.Controllo.Voices))
	}
	if !strings.Contains(se, "controllo: s2") {
		t.Errorf("nessun avviso sul difetto della clip nera:\n%s", se)
	}
	// cut: aggiunge il montato; nessuna richiesta, nessuna rigenerazione
	before := e.tts
	v8Cut(t, e)
	var c struct{ Outputs []any }
	b2, _ := os.ReadFile(cf)
	json.Unmarshal(b2, &c)
	if len(c.Outputs) != 1 {
		t.Errorf("montati nel controllo dopo cut: %d", len(c.Outputs))
	}
	if e.tts != before || e.videos != 0 {
		t.Errorf("il controllo ha speso: tts %d->%d video=%d", before, e.tts, e.videos)
	}
}

// strict + difetto: run esce 1 dopo aver finito (voce gia' fatta, nessuna spesa in piu').
func TestV8ControlloStrict(t *testing.T) {
	cutNeedFF(t)
	b := `{"strict":true,"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},"controllo":{"tecnico":true},
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},
"scene":[{"id":"s2","tipo":"clip","file":"nera.mp4","durata":3}]}`
	e := newV7Env(t, b)
	cutFF(t, "-f", "lavfi", "-i", "color=c=black:s=256x144:r=25:d=3", "-pix_fmt", "yuv420p", filepath.Join(e.dir, "nera.mp4"))
	if _, se, code := e.story(t, "run"); code != 1 {
		t.Errorf("code=%d\n%s", code, se)
	}
	if e.videos != 0 || e.tts != 0 {
		t.Errorf("richieste: %d %d", e.videos, e.tts)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "spot.controllo.json")); err != nil {
		t.Errorf("controllo.json assente: %v", err)
	}
}

// controllo.voce non ancora disponibile: codice 2 (V-T2 non e' su questo branch).
// (non testato: si collega a V-T2)

// decisione 22: voce Gemini con un tetto = codice 2 senza nessuna richiesta, in
// check e in run; senza tetto parte contro il server finto, con avviso.
func TestV8GeminiConTettoRifiutata(t *testing.T) {
	g := `,"parlato":{"provider":"gemini","voce":"Kore","lingua":"it"}`
	e := newV7Env(t, strings.Replace(v7Board(g+`,"tetto_costo_usd":1`), v7Parlato, "", 1))
	for _, a := range [][]string{{"check"}, {"run"}, {"run", "--voices"}} {
		if _, se, code := e.story(t, a...); code != 2 {
			t.Errorf("%v: code=%d\n%s", a, code, se)
		}
	}
	if e.gem != 0 || e.tts != 0 || e.videos != 0 {
		t.Errorf("richieste: gem=%d tts=%d video=%d", e.gem, e.tts, e.videos)
	}
}

func TestV8GeminiSenzaTettoParteConAvviso(t *testing.T) {
	g := `,"parlato":{"provider":"gemini","voce":"Kore","lingua":"it"}`
	e := newV7Env(t, v7Board(g))
	out, se, code := e.story(t, "check")
	if code != 0 || !strings.Contains(out, "voice_cost_probable") || e.gem != 0 {
		t.Fatalf("check code=%d gem=%d\n%s\n%s", code, e.gem, out, se)
	}
	_, se, code = e.story(t, "run", "--voices")
	if code != 0 || e.gem != 1 {
		t.Fatalf("run code=%d gem=%d\n%s", code, e.gem, se)
	}
	if !strings.Contains(se, "avviso") {
		t.Errorf("nessun avviso su stderr:\n%s", se)
	}
	ss := e.state(t)["s1"].(map[string]any)
	if ss["voice_status"] != "fatto" {
		t.Errorf("stato: %v", ss)
	}
}

func v8VoceBoard(strict bool, controllo string) string {
	st := ""
	if strict {
		st = `"strict":true,`
	}
	return `{` + st + `"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"}` + v7Parlato + `,"controllo":` + controllo + `,
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},
"scene":[{"id":"s1","tipo":"immagine","file":"a.png","durata":3,"testo":"` + v7Text + `"}]}`
}

// controllo.voce: la voce generata e' trascritta una volta (riserva e stato
// "incerto" scritti prima della richiesta), il confronto e' nel controllo; un
// secondo giro e cut non rifanno la richiesta; controllo.voce senza tecnico
// da' solo le righe delle voci.
func TestV8ControlloVoce(t *testing.T) {
	e := newV7Env(t, v8VoceBoard(false, `{"voce":true}`))
	out, se, code := e.story(t, "run")
	if code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	if e.tts != 1 || e.stt != 1 || e.videos != 0 {
		t.Fatalf("richieste: tts=%d stt=%d video=%d", e.tts, e.stt, e.videos)
	}
	if !strings.Contains(e.stateAtSTT[0], `"incerto"`) || !strings.Contains(e.stateAtSTT[0], "trascrizione") {
		t.Errorf("riserva non scritta prima della trascrizione: %s", e.stateAtSTT[0])
	}
	var r struct {
		Controllo *struct {
			Scenes []any
			Voices []struct {
				ID, Result string
				Comparison any
				Transcript any
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.Controllo == nil || len(r.Controllo.Voices) != 1 {
		t.Fatalf("controllo nell'uscita: %v\n%s", err, out)
	}
	if v := r.Controllo.Voices[0]; v.ID != "s1" || v.Comparison == nil || v.Transcript == nil || v.Result == "defect" {
		t.Errorf("riga voce: %+v", v)
	}
	if len(r.Controllo.Scenes) != 0 {
		t.Errorf("senza tecnico non ci sono righe di scena: %d", len(r.Controllo.Scenes))
	}
	ss := e.state(t)["s1"].(map[string]any)
	vc, _ := ss["voice_check"].(map[string]any)
	if vc["status"] != "fatto" {
		t.Errorf("voice_check: %v", ss["voice_check"])
	}
	if ch, _ := ss["charges"].([]any); len(ch) != 2 {
		t.Errorf("addebiti (voce + trascrizione): %v", ss["charges"])
	}
	// secondo giro e cut: nessuna richiesta in piu'
	if _, se, code := e.story(t, "run"); code != 0 || e.stt != 1 || e.tts != 1 {
		t.Errorf("secondo giro code=%d tts=%d stt=%d\n%s", code, e.tts, e.stt, se)
	}
}

// esito incerto della trascrizione: resta contata e non si rilancia mai da sola.
func TestV8TrascrizioneIncertaNonRilanciata(t *testing.T) {
	e := newV7Env(t, v8VoceBoard(false, `{"voce":true}`))
	e.sttMode = "drop"
	e.story(t, "run")
	if e.stt != 1 {
		t.Fatalf("stt=%d", e.stt)
	}
	ss := e.state(t)["s1"].(map[string]any)
	vc, _ := ss["voice_check"].(map[string]any)
	if vc["status"] != "incerto" {
		t.Fatalf("voice_check: %v", ss["voice_check"])
	}
	if ch, _ := ss["charges"].([]any); len(ch) != 2 {
		t.Errorf("la riserva della trascrizione deve restare contata: %v", ss["charges"])
	}
	e.sttMode = ""
	for i := 0; i < 2; i++ {
		e.story(t, "run")
	}
	if e.stt != 1 || e.tts != 1 {
		t.Errorf("rilancio da solo: tts=%d stt=%d", e.tts, e.stt)
	}
}

// una voce con parole diverse e' un difetto che si avvisa: non si rigenera,
// il run finisce; con strict esce 1 dopo aver finito.
func TestV8VoceDiversaAvvisaNonRigenera(t *testing.T) {
	e := newV7Env(t, v8VoceBoard(false, `{"voce":true}`))
	e.sttMode = "altro"
	_, se, code := e.story(t, "run")
	if code != 0 || e.tts != 1 {
		t.Fatalf("code=%d tts=%d\n%s", code, e.tts, se)
	}
	if !strings.Contains(se, "controllo: s1") {
		t.Errorf("nessun avviso sulla voce:\n%s", se)
	}
	if _, se, code := e.story(t, "run"); code != 0 || e.tts != 1 || e.stt != 1 {
		t.Errorf("secondo giro: code=%d tts=%d stt=%d\n%s", code, e.tts, e.stt, se)
	}

	e2 := newV7Env(t, v8VoceBoard(true, `{"voce":true}`))
	e2.sttMode = "altro"
	if _, se, code := e2.story(t, "run"); code != 1 || e2.tts != 1 {
		t.Errorf("strict: code=%d tts=%d\n%s", code, e2.tts, se)
	}
}

// story check mostra il costo della trascrizione senza fare richieste.
func TestV8CheckCostoTrascrizione(t *testing.T) {
	e := newV7Env(t, v8VoceBoard(false, `{"voce":true}`))
	out, se, code := e.story(t, "check")
	if code != 0 {
		t.Fatalf("code=%d\n%s", code, se)
	}
	if !strings.Contains(out, "voice_check_cost_est_usd") || e.stt != 0 || e.tts != 0 {
		t.Errorf("stt=%d tts=%d\n%s", e.stt, e.tts, out)
	}
}

// caso P4: il manifest di story run --voices riporta la spesa del giro (somma
// delle stime delle voci); un secondo giro senza richieste non ha i campi.
func TestV8ManifestSpesaDelGiro(t *testing.T) {
	texts := []string{"Prima frase dello spot.", "Seconda frase, un po' piu' lunga.", "Terza."}
	var sc []string
	chars := 0
	for i, tx := range texts {
		chars += len([]rune(tx))
		sc = append(sc, `{"id":"s`+string(rune('1'+i))+`","tipo":"immagine","file":"a.png","durata":3,"testo":"`+tx+`"}`)
	}
	b := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"}` + v7Parlato + `,
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},"scene":[` + strings.Join(sc, ",") + `]}`
	e := newV7Env(t, b)
	out, se, code := e.story(t, "run", "--voices")
	if code != 0 || e.tts != 3 {
		t.Fatalf("code=%d tts=%d\n%s", code, e.tts, se)
	}
	var r map[string]any
	json.Unmarshal([]byte(out), &r)
	want := float64(chars) * 15 / 1e6
	for _, k := range []string{"run_cost_est_usd", "run_spent_usd"} {
		v, ok := r[k].(float64)
		if !ok || math.Abs(v-want) > 1e-4 {
			t.Errorf("%s = %v, atteso %v", k, r[k], want)
		}
	}
	out, _, code = e.story(t, "run", "--voices")
	r = map[string]any{}
	json.Unmarshal([]byte(out), &r)
	if code != 0 || e.tts != 3 {
		t.Fatalf("secondo giro code=%d tts=%d", code, e.tts)
	}
	for _, k := range []string{"run_cost_est_usd", "run_spent_usd"} {
		if _, ok := r[k]; ok {
			t.Errorf("secondo giro: %s presente", k)
		}
	}
}

func v8State(t *testing.T, e *v7Env, voiceFile string) {
	st := `{"version":1,"scenes":{"s1":{"status":"","voice":{"file":"` + voiceFile + `","done":"2026-01-01T00:00:00Z"},"voice_status":"fatto","voice_key":"x","charges":[{"kind":"voce","cost_est_usd":0.0006,"at":"2026-01-01T00:00:00Z"}]}}}`
	if err := os.WriteFile(filepath.Join(e.dir, "spot.stato.json"), []byte(st), 0o600); err != nil {
		t.Fatal(err)
	}
}

// controllo.voce non trascrive file che non sono una voce generata da giv:
// altro audio dello storyboard, link, nessuna richiesta e nessun addebito.
func TestV8ControlloVoceSoloVociGenerate(t *testing.T) {
	cases := map[string]func(e *v7Env){
		"altro audio": func(e *v7Env) {
			os.MkdirAll(filepath.Join(e.dir, "audio"), 0o755)
			os.WriteFile(filepath.Join(e.dir, "audio", "x.wav"), v7WAV(1), 0o644)
			v8State(t, e, "audio/x.wav")
		},
		"voce/ e' un link a cartella": func(e *v7Env) {
			os.MkdirAll(filepath.Join(e.dir, "altra"), 0o755)
			os.WriteFile(filepath.Join(e.dir, "altra", "s1-v1.wav"), v7WAV(1), 0o644)
			if err := os.Symlink("altra", filepath.Join(e.dir, "voce")); err != nil {
				t.Skip(err)
			}
			v8State(t, e, "voce/s1-v1.wav")
		},
		"file voce e' un link": func(e *v7Env) {
			os.MkdirAll(filepath.Join(e.dir, "voce"), 0o755)
			os.WriteFile(filepath.Join(e.dir, "vera.wav"), v7WAV(1), 0o644)
			if err := os.Symlink("../vera.wav", filepath.Join(e.dir, "voce", "s1-v1.wav")); err != nil {
				t.Skip(err)
			}
			v8State(t, e, "voce/s1-v1.wav")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			e := newV7Env(t, v8VoceBoard(false, `{"voce":true}`))
			setup(e)
			_, se, code := e.story(t, "run")
			if e.stt != 0 || e.tts != 0 {
				t.Errorf("richieste: stt=%d tts=%d (code=%d)\n%s", e.stt, e.tts, code, se)
			}
			ss := e.state(t)["s1"].(map[string]any)
			if ch, _ := ss["charges"].([]any); len(ch) != 1 {
				t.Errorf("addebiti cambiati: %v", ss["charges"])
			}
		})
	}
}

// nomi di voce non canonici nello stato: rifiutati, nessuna richiesta.
func TestV8NomiVoceNonCanonici(t *testing.T) {
	for _, n := range []string{"voce/s1-v01.wav", "voce/s1-v0.wav", "voce/s1-v+1.wav"} {
		e := newV7Env(t, v8VoceBoard(false, `{"voce":true}`))
		os.MkdirAll(filepath.Join(e.dir, "voce"), 0o755)
		os.WriteFile(filepath.Join(e.dir, n), v7WAV(1), 0o644)
		v8State(t, e, n)
		if _, se, code := e.story(t, "run"); code == 0 || e.stt != 0 || e.tts != 0 {
			t.Errorf("%s: code=%d stt=%d tts=%d\n%s", n, code, e.stt, e.tts, se)
		}
	}
}

// un file del kit chiamato <nome>.controllo.json (kit nella cartella dello
// storyboard) non viene sovrascritto dal controllo: avviso, kit intatto.
func TestV8ControlloNonSovrascriveKit(t *testing.T) {
	cutNeedFF(t)
	e := newV7Env(t, v8Board("", ""))
	v7Copy(t, v7ExamplesKit, e.dir)
	kit := filepath.Join(e.dir, "spot.controllo.json")
	if err := os.Rename(filepath.Join(e.dir, "kit.json"), kit); err != nil {
		t.Fatal(err)
	}
	b := strings.Replace(v8VoceBoard(false, `{"tecnico":true}`), `{"default"`, `{"kit":"spot.controllo.json","default"`, 1)
	os.WriteFile(e.board, []byte(b), 0o644)
	v8Black(t, e)
	before, _ := os.ReadFile(kit)
	for _, a := range []string{"run", "cut"} {
		_, se, code := e.story(t, a)
		if code != 0 {
			t.Fatalf("%s code=%d\n%s", a, code, se)
		}
		if !strings.Contains(se, "controllo non scritto") {
			t.Errorf("%s: nessun avviso 'controllo non scritto':\n%s", a, se)
		}
		if after, _ := os.ReadFile(kit); string(after) != string(before) {
			t.Fatalf("%s: il file del kit e' stato modificato", a)
		}
	}
}
