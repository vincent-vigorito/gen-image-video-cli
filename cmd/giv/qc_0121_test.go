package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// P-A (CLI-994), giv 0.12.1: il controllo tecnico non avvisa a vuoto.
// Materiale sintetico (testsrc2, sine, copertina allegata con ffmpeg).

type pa1Ctl struct {
	Scenes  []qcRow `json:"scenes"`
	Outputs []qcRow `json:"outputs"`
}

func pa1Check(t *testing.T, row qcRow, name string) v4Check {
	t.Helper()
	if row.QCReport == nil {
		t.Fatalf("riga %s senza controlli (%s)", row.ID, row.Message)
	}
	var raw struct {
		Checks []v4Check `json:"checks"`
	}
	b, _ := json.Marshal(row)
	json.Unmarshal(b, &raw)
	for _, c := range raw.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("controllo %q assente nella riga %s", name, row.ID)
	return v4Check{}
}

func pa1Control(t *testing.T, e *v7Env) pa1Ctl {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, "spot.controllo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c pa1Ctl
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func pa1Board(scenes, montaggio string) string {
	return `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"controllo":{"tecnico":true},"formato":{"aspect":"16:9","risoluzione":"144p","fps":25}` + montaggio + `,"scene":[` + scenes + `]}`
}

const pa1Cartello = `,"montaggio":{"cartello_finale":{"durata":3,"testo":"Fine"}}`

// clip con una seconda traccia: kind "cover", "video" o "audio".
func pa1Extra(t *testing.T, dir, name, kind string) {
	base := filepath.Join(dir, "base.mp4")
	cutClip(t, base, "256x144", 25, "4")
	out := filepath.Join(dir, name)
	switch kind {
	case "cover":
		cutFF(t, "-f", "lavfi", "-i", "testsrc=size=64x64", "-frames:v", "1", filepath.Join(dir, "cover.png"))
		cutFF(t, "-i", base, "-i", filepath.Join(dir, "cover.png"), "-map", "0", "-map", "1", "-c", "copy", "-disposition:v:1", "attached_pic", out)
	case "video":
		cutFF(t, "-i", base, "-f", "lavfi", "-i", "testsrc=size=256x144:rate=25:duration=4", "-map", "0", "-map", "1:v", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "copy", out)
	case "audio":
		cutFF(t, "-i", base, "-f", "lavfi", "-i", "sine=frequency=500:duration=4", "-map", "0", "-map", "1:a", "-c:v", "copy", "-c:a", "aac", out)
	}
}

// cartello finale: nessun avviso di immagine ferma per quel tratto; un fermo
// messo apposta in una scena resta segnalato.
func TestPA1CartelloNonEFermo(t *testing.T) {
	cutNeedFF(t)
	e := newV7Env(t, pa1Board(`{"id":"s1","tipo":"clip","file":"mov.mp4"}`, pa1Cartello))
	cutClip(t, filepath.Join(e.dir, "mov.mp4"), "256x144", 25, "4")
	if _, se, code := e.story(t, "run"); code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	v8Cut(t, e)
	out := pa1Control(t, e).Outputs
	if len(out) != 1 {
		t.Fatalf("montati: %d", len(out))
	}
	f := pa1Check(t, out[0], "freeze")
	if f.Result == "avviso" || f.Result == "difetto" || len(f.Intervals) != 0 {
		t.Errorf("cartello segnalato come fermo: %+v", f)
	}

	// fermo apposta: una clip con un tratto fermo vero (4 s di colore fisso),
	// poi una clip in movimento e il cartello
	e2 := newV7Env(t, pa1Board(`{"id":"s1","tipo":"clip","file":"fermo.mp4"},{"id":"s2","tipo":"clip","file":"mov.mp4"}`, pa1Cartello))
	cutFF(t, "-f", "lavfi", "-i", "color=c=red:s=256x144:r=25:d=4", "-f", "lavfi", "-i", "sine=frequency=300:duration=4",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", filepath.Join(e2.dir, "fermo.mp4"))
	cutClip(t, filepath.Join(e2.dir, "mov.mp4"), "256x144", 25, "4")
	if _, se, code := e2.story(t, "run"); code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	v8Cut(t, e2)
	f2 := pa1Check(t, pa1Control(t, e2).Outputs[0], "freeze")
	if f2.Result != "avviso" || len(f2.Intervals) == 0 {
		t.Fatalf("il fermo apposta non e' segnalato nel montato: %+v", f2)
	}
	for _, iv := range f2.Intervals {
		if iv.End > 4.5 { // il cartello comincia a 8 s
			t.Errorf("intervallo fermo oltre la scena (cartello?): %+v", iv)
		}
	}
}

// copertina allegata: nessun avviso di tracce in piu'; seconda traccia video
// o audio vera: avviso. In giv qc, in story run e in story cut.
func TestPA1CopertinaGivQC(t *testing.T) {
	cutNeedFF(t)
	dir := t.TempDir()
	v4clip(t, dir, "normale.mp4", "testsrc=size=320x180:rate=25", "sine=frequency=440", 4)
	pa1Extra(t, dir, "cover.mp4", "cover")
	pa1Extra(t, dir, "v2.mp4", "video")
	pa1Extra(t, dir, "a2.mp4", "audio")
	for file, want := range map[string]string{"normale.mp4": "ok", "cover.mp4": "ok", "v2.mp4": "avviso", "a2.mp4": "avviso"} {
		o, code, se := v4qc(t, dir, file)
		if code != 0 {
			t.Fatalf("%s: codice %d\n%s", file, code, se)
		}
		if c := o.check(t, "streams"); c.Result != want {
			t.Errorf("%s: streams=%q, atteso %q", file, c.Result, want)
		}
	}
}

func TestPA1CopertinaStoryRunECut(t *testing.T) {
	cutNeedFF(t)
	e := newV7Env(t, pa1Board(`{"id":"s1","tipo":"clip","file":"cover.mp4"},{"id":"s2","tipo":"clip","file":"v2.mp4"},{"id":"s3","tipo":"clip","file":"a2.mp4"}`, ""))
	pa1Extra(t, e.dir, "cover.mp4", "cover")
	pa1Extra(t, e.dir, "v2.mp4", "video")
	pa1Extra(t, e.dir, "a2.mp4", "audio")
	_, se, code := e.story(t, "run")
	if code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	check := func(when string) {
		c := pa1Control(t, e)
		if len(c.Scenes) != 3 {
			t.Fatalf("%s: scene %d", when, len(c.Scenes))
		}
		for i, want := range []string{"ok", "avviso", "avviso"} {
			if got := pa1Check(t, c.Scenes[i], "streams").Result; got != want {
				t.Errorf("%s: %s streams=%q, atteso %q", when, c.Scenes[i].ID, got, want)
			}
		}
		for _, o := range c.Outputs {
			if o.QCReport == nil { // montato non ancora fatto
				continue
			}
			if got := pa1Check(t, o, "streams").Result; got != "ok" {
				t.Errorf("%s: montato streams=%q", when, got)
			}
		}
	}
	check("run")
	if strings.Contains(se, "controllo: s1") {
		t.Errorf("run avvisa sulla copertina:\n%s", se)
	}
	_, se2, _ := e.story(t, "cut")
	check("cut")
	if strings.Contains(se2, "controllo: s1") {
		t.Errorf("cut avvisa sulla scena con copertina:\n%s", se2)
	}
}
