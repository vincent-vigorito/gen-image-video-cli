package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test R-T6 (CLI-951), passo 1: campi nuovi dello storyboard, story check e
// story cut multi-formato. Clip sintetiche, nessuna rete.

const r6Scenes = `"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":2},{"id":"s2","tipo":"clip","file":"b.mp4","durata":2}]`

func r6Dir(t *testing.T) string {
	t.Helper()
	cutNeedFF(t)
	d := t.TempDir()
	cutClip(t, filepath.Join(d, "a.mp4"), "160x90", 25, "3")
	cutClip(t, filepath.Join(d, "b.mp4"), "160x90", 25, "3")
	cutFF(t, "-f", "lavfi", "-i", "color=c=red:s=32x32:d=1", "-frames:v", "1", filepath.Join(d, "logo.png"))
	return d
}

func r6Board(t *testing.T, dir, montaggio, scenes string) string {
	t.Helper()
	if scenes == "" {
		scenes = r6Scenes
	}
	b := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},"montaggio":{` + montaggio + `},` + scenes + `}`
	p := filepath.Join(dir, "spot.json")
	if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func r6Cmd(t *testing.T, args ...string) (out, errOut string, code int) {
	t.Helper()
	grab := func(f **os.File) func() string {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := readAllFile(r); done <- string(b) }()
		return func() string { w.Close(); *f = old; return <-done }
	}
	so, se := grab(&os.Stdout), grab(&os.Stderr)
	err := cmdStory(args)
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

func TestR6CheckRifiuti(t *testing.T) {
	d := r6Dir(t)
	bad := map[string][2]string{ // nome: {montaggio, scene}
		"formato non ammesso": {`"formati":["16:9","7:3"]`, ""},
		"logo mancante":       {`"logo":{"file":"manca.png"}`, ""},
		"logo fuori cartella": {`"logo":{"file":"../logo.png"}`, ""},
		"font mancante":       {`"stile_testi":{"font":"manca.ttf"}`, ""},
		"musica mancante":     {`"musica":{"file":"manca.mp3"}`, ""},
		"fuoco fuori":         {``, `"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":2,"fuoco":1.5}]`},
		"taglio da>=a":        {``, `"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":2,"taglio":{"da":2,"a":1}}]`},
		"velocita 5":          {``, `"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":2,"velocita":5}]`},
	}
	for name, c := range bad {
		p := r6Board(t, d, c[0], c[1])
		_, se, code := r6Cmd(t, "check", p)
		if code != 2 {
			t.Errorf("%s: codice %d, atteso 2\n%s", name, code, se)
		}
	}
}

// Un titolo con i tempi fuori dalla durata del montato: oggi solo un avviso
// (il developer lo documenta così); il test registra che check lo segnala.
func TestR6TitoloFuoriDurataSegnalato(t *testing.T) {
	d := r6Dir(t)
	p := r6Board(t, d, `"titoli":[{"testo":"x","da":999,"durata":2}]`, "")
	out, se, code := r6Cmd(t, "check", p)
	if code != 0 && code != 2 {
		t.Fatalf("codice %d\n%s", code, se)
	}
	if code == 0 && !strings.Contains(strings.ToLower(out+se), "titol") {
		t.Fatalf("nessun avviso sul titolo fuori durata:\n%s\n%s", out, se)
	}
}

func TestR6CheckAccettaCampiValidi(t *testing.T) {
	d := r6Dir(t)
	p := r6Board(t, d, `"formati":["16:9","9:16"],"adatta":"sfocato","logo":{"file":"logo.png"},"titoli":[{"testo":"Ciao","da":0.5,"durata":1.5}],"cartello_finale":{"testo":"Fine"}`,
		`"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":2,"fuoco":{"x":0.3,"y":0.5},"taglio":{"da":0.5,"a":2.5}},{"id":"s2","tipo":"clip","file":"b.mp4","durata":2,"velocita":2}]`)
	_, se, code := r6Cmd(t, "check", p)
	if code != 0 {
		t.Fatalf("check: codice %d\n%s", code, se)
	}
}

type r6Cut struct {
	Formats []struct {
		Output    string  `json:"output"`
		Format    string  `json:"format"`
		DurationS float64 `json:"duration_s"`
		Width     int     `json:"width"`
		Height    int     `json:"height"`
	} `json:"formats"`
}

func TestR6CutDueFormatiEFormatoSolo(t *testing.T) {
	d := r6Dir(t)
	p := r6Board(t, d, `"formati":["16:9","9:16"]`, "")
	out, se, code := r6Cmd(t, "cut", p)
	if code != 0 {
		t.Fatalf("cut: codice %d\n%s", code, se)
	}
	var r r6Cut
	if err := json.Unmarshal([]byte(out), &r); err != nil || len(r.Formats) != 2 {
		t.Fatalf("uscita: %v\n%s", err, out)
	}
	want := map[string][3]any{"16:9": {"spot-16x9.mp4", 256, 144}, "9:16": {"spot-9x16.mp4", 144, 256}}
	for _, f := range r.Formats {
		w := want[f.Format]
		if filepath.Base(f.Output) != w[0] || f.Width != w[1] || f.Height != w[2] {
			t.Errorf("%s: %s %dx%d, atteso %v", f.Format, f.Output, f.Width, f.Height, w)
		}
		if _, err := os.Stat(f.Output); err != nil {
			t.Errorf("file mancante: %v", err)
		}
	}
	// --formato: uno solo; formato non dichiarato = 2
	out, se, code = r6Cmd(t, "cut", "--formato", "9:16", p)
	if code != 0 {
		t.Fatalf("--formato: codice %d\n%s", code, se)
	}
	r = r6Cut{}
	json.Unmarshal([]byte(out), &r)
	if len(r.Formats) != 1 || r.Formats[0].Format != "9:16" {
		t.Errorf("--formato: %s", out)
	}
	if _, _, code = r6Cmd(t, "cut", "--formato", "1:1", p); code != 2 {
		t.Errorf("--formato non dichiarato: codice %d, atteso 2", code)
	}
}

// Taglio e velocità di scena cambiano la durata della scena nel montaggio.
func TestR6TaglioVelocitaCambianoDurata(t *testing.T) {
	d := r6Dir(t)
	base := `"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":2}]`
	dur := func(scene string) float64 {
		p := r6Board(t, d, `"formati":["16:9"]`, scene)
		out, se, code := r6Cmd(t, "cut", p)
		if code != 0 {
			t.Fatalf("cut: codice %d\n%s", code, se)
		}
		var r r6Cut
		if err := json.Unmarshal([]byte(out), &r); err != nil || len(r.Formats) != 1 {
			t.Fatalf("%v\n%s", err, out)
		}
		return r.Formats[0].DurationS
	}
	d0 := dur(base)
	dTaglio := dur(`"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","taglio":{"da":0,"a":1}}]`)
	dVel := dur(`"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","velocita":2}]`)
	if math.Abs(d0-2) > 0.15 {
		t.Errorf("senza campi: %.2f, attesa 2", d0)
	}
	if !(dTaglio < d0-0.3) {
		t.Errorf("taglio non accorcia: %.2f vs %.2f", dTaglio, d0)
	}
	if !(dVel < d0-0.3) {
		t.Errorf("velocità 2 non accorcia: %.2f vs %.2f", dVel, d0)
	}
}

func TestR6StoryboardDiOggiSenzaCampiNuovi(t *testing.T) {
	d := r6Dir(t)
	p := r6Board(t, d, `"transizione":"dissolvenza"`, "")
	out, se, code := r6Cmd(t, "cut", p)
	if code != 0 {
		t.Fatalf("cut: codice %d\n%s", code, se)
	}
	if strings.Contains(out, `"formats"`) || !strings.Contains(out, `"output"`) {
		t.Errorf("senza formati l'uscita deve restare quella di prima: %s", out)
	}
}
