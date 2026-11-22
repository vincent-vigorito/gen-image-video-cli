package montage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gen-image-video-cli/internal/speech"
)

// Test V-T6 (CLI-975): sottotitoli dai testi. Funzioni pure, nessun ffmpeg.

func v6Layout(w, h int, m Margins) CaptionLayout {
	return CaptionLayout{Width: w, Height: h, Margins: m}
}

func v6Joined(cs []Caption) string {
	var s []string
	for _, c := range cs {
		s = append(s, c.Text)
	}
	return strings.Join(s, "|")
}

// timings per carattere: ogni carattere non spazio dura step, gli spazi 0 di durata.
func v6Timings(text string, step float64) []speech.CharTime {
	var ts []speech.CharTime
	t := 0.0
	for _, r := range text {
		if r == ' ' {
			ts = append(ts, speech.CharTime{Char: " ", Start: t, End: t})
			continue
		}
		ts = append(ts, speech.CharTime{Char: string(r), Start: t, End: t + step})
		t += step
	}
	return ts
}

func TestV6SpezzaAllaPunteggiaturaEInizioFineCoerenti(t *testing.T) {
	text := "Ciao a tutti. Oggi vediamo la crema alle erbe! Funziona davvero?"
	l := v6Layout(1920, 1080, SafeDefault)
	cs, _, err := Captions([]CaptionVoice{{Text: text, Start: 2, Duration: 6, Timings: v6Timings(text, 0.1)}}, l)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 3 {
		t.Fatalf("sottotitoli: %d (%v), attesi 3", len(cs), v6Joined(cs))
	}
	prevEnd := 0.0
	for i, c := range cs {
		if !(c.Start >= 2 && c.End > c.Start && c.End <= 8+1e-9) {
			t.Errorf("%d: %.3f-%.3f fuori da [2,8] o incoerente", i, c.Start, c.End)
		}
		if c.Start < prevEnd-1e-9 {
			t.Errorf("%d: sovrapposto (inizio %.3f < fine precedente %.3f)", i, c.Start, prevEnd)
		}
		prevEnd = c.End
		if n := len(strings.Split(c.Text, "\n")); n > 2 {
			t.Errorf("%d: %d righe", i, n)
		}
	}
	if !strings.HasPrefix(cs[0].Text, "Ciao a tutti.") || !strings.HasPrefix(cs[1].Text, "Oggi") || !strings.HasPrefix(cs[2].Text, "Funziona") {
		t.Errorf("divisione: %s", v6Joined(cs))
	}
}

func TestV6RigheEntroLaLarghezzaDelFormato(t *testing.T) {
	text := "Una frase molto lunga senza punteggiatura che deve andare a capo in righe corte per il formato verticale dei reel"
	for _, l := range []CaptionLayout{v6Layout(1080, 1920, SafeVertical), v6Layout(1920, 1080, SafeDefault)} {
		max := CaptionChars(l)
		if max < 12 || max > 60 {
			t.Fatalf("CaptionChars %d", max)
		}
		cs, _, err := Captions([]CaptionVoice{{Text: text, Duration: 10}}, l)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			for _, line := range strings.Split(c.Text, "\n") {
				if n := len([]rune(line)); n > max {
					t.Errorf("%dx%d: riga di %d caratteri (> %d): %q", l.Width, l.Height, n, max, line)
				}
			}
		}
	}
	if v, o := CaptionChars(v6Layout(1080, 1920, SafeVertical)), CaptionChars(v6Layout(1920, 1080, SafeDefault)); v >= o {
		t.Errorf("il verticale (%d) deve avere righe piu' corte dell'orizzontale (%d)", v, o)
	}
}

func TestV6DizionarioLaGrafiaResta(t *testing.T) {
	text := "Swerpify e Anja"
	dict := speech.Dictionary{"Swerpify": "suèrpifai"} // il detto e' piu' lungo dello scritto
	spoken := dict.Apply(text)
	cs, warns, err := Captions([]CaptionVoice{{Text: text, Duration: 4, Timings: v6Timings(spoken, 0.1), Dict: dict}}, v6Layout(1920, 1080, SafeDefault))
	if err != nil {
		t.Fatal(err)
	}
	if v6Joined(cs) != text {
		t.Errorf("testo %q, atteso la grafia scritta %q", v6Joined(cs), text)
	}
	_ = warns
}

func TestV6TestoAllaLettera(t *testing.T) {
	text := `Prezzo {\an8} e \N di {\b1}prova`
	l := v6Layout(1920, 1080, SafeDefault)
	cs, _, err := Captions([]CaptionVoice{{Text: text, Duration: 4}}, l)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v6Joined(cs), `{\an8}`) {
		t.Errorf("il testo scritto deve restare intatto: %q", v6Joined(cs))
	}
	data, err := CaptionsASS(cs, l)
	if err != nil {
		t.Fatal(err)
	}
	var dialogue string
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(ln, "Dialogue:") {
			dialogue += ln + "\n"
		}
	}
	for _, bad := range []string{`{\an8}`, `{\b1}`} {
		if strings.Contains(dialogue, bad) {
			t.Errorf("override ASS interpretabile nel Dialogue: %q in %s", bad, dialogue)
		}
	}
	if strings.Contains(strings.ReplaceAll(dialogue, `\N`, ""), `\N`) {
		t.Errorf("\\N scritto dall'utente interpretabile: %s", dialogue)
	}
}

func TestV6TempiMancantiEIncoerenti(t *testing.T) {
	text := "Prima frase. Seconda frase."
	l := v6Layout(1920, 1080, SafeDefault)
	// assenti: distribuiti lungo la durata con avviso
	cs, warns, err := Captions([]CaptionVoice{{Text: text, Start: 1, Duration: 4}}, l)
	if err != nil || len(cs) != 2 || len(warns) == 0 {
		t.Fatalf("tempi assenti: %v %d sottotitoli, avvisi %v", err, len(cs), warns)
	}
	if cs[0].Start < 1-1e-9 || cs[1].End > 5+1e-9 || cs[1].Start < cs[0].End-1e-9 {
		t.Errorf("distribuzione fuori da [1,5]: %+v", cs)
	}
	// incoerenti (fine prima dell'inizio): scartati con avviso, stessa distribuzione
	bad := []speech.CharTime{{Char: "P", Start: 2, End: 1}}
	cs2, warns2, err := Captions([]CaptionVoice{{Text: text, Start: 1, Duration: 4, Timings: bad}}, l)
	if err != nil || len(cs2) != 2 || len(warns2) == 0 {
		t.Fatalf("tempi incoerenti: %v %d, avvisi %v", err, len(cs2), warns2)
	}
	// senza tempi e senza durata: rifiuto
	if _, _, err := Captions([]CaptionVoice{{Text: text}}, l); err == nil {
		t.Error("Duration 0 senza tempi: atteso errore")
	}
	// testo vuoto: nessun sottotitolo
	if cs, _, err := Captions([]CaptionVoice{{Text: "", Duration: 3}}, l); err != nil || len(cs) != 0 {
		t.Errorf("testo vuoto: %v %d", err, len(cs))
	}
}

func TestV6FileAssDiLavoroA0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.ass")
	ass := "[Script Info]\nPlayResX: 384\nPlayResY: 288\n\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, MarginL, MarginR, MarginV\nStyle: Default,Arial,20,10,10,10\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:00.00,0:00:01.00,Default,ciao\n"
	if err := os.WriteFile(p, []byte(ass), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := subtitleMargins(p, SafeDefault); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("permessi %v, attesi 0600", st.Mode().Perm())
	}
}
