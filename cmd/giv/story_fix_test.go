package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// 0.9.1-A (CLI-884): correzioni a giv story dallo spot di prova. Provider finto.

// flag che rigenera il fotogramma chiave (solo con --scene)
const regenFlag = "--nuovo-fotogramma"

const keyDecl = `,"fotogramma_chiave":{"prompt":"un gatto fermo","costo_dichiarato_usd":0.14}`

// boardDef: storyboard con extra nel blocco default.
func boardDef(defExtra, top string, scene ...string) string {
	return fmt.Sprintf(`{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"%s}%s,"scene":[%s]}`,
		defExtra, top, strings.Join(scene, ","))
}

// raw esegue giv story <args> e restituisce stdout e stderr separati.
func rawStory(t *testing.T, args ...string) (out, errOut string, code int) {
	t.Helper()
	grab := func(f **os.File) func() string {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
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

// unicoJSON: stdout deve essere un solo documento JSON, senza altro testo.
func unicoJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("stdout non JSON: %v\n%s", err, out)
	}
	if rest, _ := io.ReadAll(dec.Buffered()); len(bytes.TrimSpace(rest)) > 0 {
		t.Fatalf("testo dopo il JSON: %q", rest)
	}
	return m
}

func chargeKinds(t *testing.T, e *runEnv, id string) (img, clip int) {
	t.Helper()
	cs, _ := e.sceneState(t, id)["charges"].([]any)
	for _, c := range cs {
		switch c.(map[string]any)["kind"] {
		case "fotogramma":
			img++
		case "clip":
			clip++
		}
	}
	return
}

// 1: --scene col flag = un avvio immagine e uno video, costo nel tetto, il
// fotogramma precedente fra le riprese precedenti; senza flag riuso; flag senza --scene = 2.
func TestStoryFixFotogrammaRigenerato(t *testing.T) {
	t.Run("rigenera", func(t *testing.T) {
		e := newRunEnv(t, sb(`,"tetto_costo_usd":3`, sc("s1", keyDecl)))
		if _, se, code := e.run(t); code != 0 || e.images != 1 || e.posts != 1 {
			t.Fatalf("prima: code=%d images=%d posts=%d\n%s", code, e.images, e.posts, se)
		}
		old, _ := e.sceneState(t, "s1")["keyframe"].(map[string]any)
		if old == nil {
			t.Fatal("manca il fotogramma dopo la prima esecuzione")
		}
		if _, se, code := e.run(t, "--scene", "s1", regenFlag); code != 0 || e.images != 2 || e.posts != 2 {
			t.Fatalf("rigenera: code=%d images=%d posts=%d\n%s", code, e.images, e.posts, se)
		}
		st := e.sceneState(t, "s1")
		if img, clip := chargeKinds(t, e, "s1"); img != 2 || clip != 2 {
			t.Errorf("addebiti immagine=%d clip=%d, attesi 2 e 2", img, clip)
		}
		found := false
		prev, _ := st["previous"].([]any)
		for _, p := range prev {
			if p.(map[string]any)["file"] == old["file"] {
				found = true
			}
		}
		if !found {
			t.Errorf("il fotogramma precedente %v non e' fra le riprese precedenti: %v", old["file"], prev)
		}
		if nk, _ := st["keyframe"].(map[string]any); nk == nil || nk["file"] == old["file"] {
			t.Errorf("fotogramma non sostituito: %v", st["keyframe"])
		}
	})
	t.Run("tetto", func(t *testing.T) {
		// prima 0.5+0.14 = 0.64; la rigenerazione costa altri 0.64: 1.28 > 1.0
		e := newRunEnv(t, sb(`,"tetto_costo_usd":1`, sc("s1", keyDecl)))
		if _, se, code := e.run(t); code != 0 {
			t.Fatalf("prima: code=%d\n%s", code, se)
		}
		_, se, code := e.run(t, "--scene", "s1", regenFlag)
		if code != 2 || e.images != 1 || e.posts != 1 {
			t.Fatalf("code=%d images=%d posts=%d\n%s", code, e.images, e.posts, se)
		}
		if !strings.Contains(se, "tetto") {
			t.Errorf("errore senza tetto: %s", se)
		}
	})
	t.Run("senza flag riusa", func(t *testing.T) {
		e := newRunEnv(t, sb(`,"tetto_costo_usd":3`, sc("s1", keyDecl)))
		e.run(t)
		if _, se, code := e.run(t, "--scene", "s1"); code != 0 || e.images != 1 || e.posts != 2 {
			t.Fatalf("code=%d images=%d posts=%d\n%s", code, e.images, e.posts, se)
		}
	})
	t.Run("flag senza --scene", func(t *testing.T) {
		e := newRunEnv(t, sb("", sc("s1", keyDecl)))
		_, se, code := e.run(t, regenFlag)
		if code != 2 || e.images != 0 || e.posts != 0 {
			t.Fatalf("code=%d images=%d posts=%d\n%s", code, e.images, e.posts, se)
		}
	})
}

// 2: tutela dello storyboard.
func TestStoryFixTutela(t *testing.T) {
	const prodotto, illus = "keep the product", "NO text, NO words"
	run := func(t *testing.T, board string) string {
		e := newRunEnv(t, board)
		if _, se, code := e.run(t); code != 0 || e.posts != 1 {
			t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
		}
		return e.bodies[0]
	}
	t.Run("illustrazione default", func(t *testing.T) {
		b := run(t, boardDef(`,"tutela":"illustrazione"`, "", sc("s1", keyDecl)))
		if !strings.Contains(b, illus) || strings.Contains(b, prodotto) {
			t.Errorf("corpo: %s", b)
		}
	})
	t.Run("illustrazione per scena", func(t *testing.T) {
		b := run(t, boardDef("", "", sc("s1", keyDecl+`,"tutela":"illustrazione"`)))
		if !strings.Contains(b, illus) || strings.Contains(b, prodotto) {
			t.Errorf("corpo: %s", b)
		}
	})
	t.Run("senza campo come oggi", func(t *testing.T) {
		b := run(t, sb("", sc("s1", keyDecl)))
		if !strings.Contains(b, prodotto) || strings.Contains(b, illus) {
			t.Errorf("corpo: %s", b)
		}
	})
	t.Run("sconosciuta in check", func(t *testing.T) {
		for _, board := range []string{
			boardDef(`,"tutela":"boh"`, "", sc("s1", "")),
			boardDef("", "", sc("s1", `,"tutela":"boh"`)),
		} {
			out, se, code, starts := storyCheckRun(t, board)
			if code != 2 || starts != 0 || out != "" {
				t.Fatalf("code=%d starts=%d out=%q\n%s", code, starts, out, se)
			}
		}
	})
}

// 3: help.
func TestStoryFixHelp(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"--help"}, []string{"check", "run", "cut"}},
		{[]string{"check", "--help"}, []string{"check"}},
		{[]string{"run", "--help"}, []string{"run", "--scene", regenFlag}},
		{[]string{"cut", "--help"}, []string{"cut"}},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			out, se, code := rawStory(t, c.args...)
			if code != 0 {
				t.Fatalf("code=%d\n%s", code, se)
			}
			all := out + se
			for _, w := range c.want {
				if !strings.Contains(all, w) {
					t.Errorf("manca %q in:\n%s", w, all)
				}
			}
		})
	}
}

// 4: spesa di scene non piu' nello storyboard: a parte, con gli id; conta nel tetto.
const orphanState = `{"version":1,"scenes":{"vecchia":{"status":"fatto","clip":{"file":"clip/vecchia.mp4","job_id":"j0","done":"2026-01-01T00:00:00Z"},"charges":[{"kind":"clip","job_id":"j0","cost_usd":0.7,"at":"2026-01-01T00:00:00Z"}]}}}`

func TestStoryFixSpesaSceneOrfane(t *testing.T) {
	t.Run("check", func(t *testing.T) {
		e := newRunEnv(t, sb("", sc("s1", "")))
		e.writeState(t, orphanState)
		out, se, code := rawStory(t, "check", e.board)
		if code != 0 {
			t.Fatalf("code=%d\n%s", code, se)
		}
		unicoJSON(t, out)
		if !strings.Contains(out, "vecchia") || !strings.Contains(out, "0.7") {
			t.Errorf("spesa delle scene tolte non mostrata: %s", out)
		}
	})
	t.Run("run mostra", func(t *testing.T) {
		e := newRunEnv(t, sb(`,"tetto_costo_usd":5`, sc("s1", "")))
		e.writeState(t, orphanState)
		out, se, code := rawStory(t, "run", e.board)
		if code != 0 || e.posts != 1 {
			t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
		}
		unicoJSON(t, out)
		if !strings.Contains(out, "vecchia") || !strings.Contains(out, "0.7") {
			t.Errorf("spesa delle scene tolte non mostrata: %s", out)
		}
	})
	t.Run("tetto la conta e la nomina", func(t *testing.T) {
		// 0.7 orfana + 0.5 stima s1 = 1.2 > 1.0
		e := newRunEnv(t, sb(`,"tetto_costo_usd":1`, sc("s1", "")))
		e.writeState(t, orphanState)
		_, se, code := rawStory(t, "run", e.board)
		if code != 2 || e.posts != 0 {
			t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
		}
		if !strings.Contains(se, "tetto") || !strings.Contains(se, "vecchia") {
			t.Errorf("messaggio senza tetto o senza id: %s", se)
		}
	})
}

// 5 e 6: campo scenes e stdout = un solo JSON.
func TestStoryFixScenesEStdoutJSON(t *testing.T) {
	e := newRunEnv(t, sb("", sc("s1", "")))
	out, se, code := rawStory(t, "check", e.board)
	if code != 0 {
		t.Fatalf("check code=%d\n%s", code, se)
	}
	m := unicoJSON(t, out)
	if _, ok := m["scenes"].([]any); !ok {
		t.Errorf("check: manca scenes: %s", out)
	}
	if _, ok := m["scene"]; ok {
		t.Errorf("check: campo scene ancora presente")
	}
	out, se, code = rawStory(t, "run", e.board)
	if code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	m = unicoJSON(t, out)
	if _, ok := m["scenes"].([]any); !ok {
		t.Errorf("run: manca scenes: %s", out)
	}
	if _, ok := m["scene"]; ok {
		t.Errorf("run: campo scene ancora presente")
	}
	if !strings.Contains(se, "s1") {
		t.Errorf("avanzamento non su stderr: %q", se)
	}
}

// 6: cut, un solo JSON su stdout.
func TestStoryFixCutStdoutJSON(t *testing.T) {
	cutNeedFF(t)
	dir := cutDir(t)
	out, se, code := rawStory(t, "cut", dir+"/sb.json")
	if code != 0 {
		t.Fatalf("cut code=%d\n%s", code, se)
	}
	unicoJSON(t, out)
}

// 6: avviso (negative ignorato) su stderr, stdout resta un solo JSON.
func TestStoryFixAvvisoSuStderr(t *testing.T) {
	e := newRunEnv(t, sb("", sc("s1", `,"negative":"blur"`)))
	out, _, code := rawStory(t, "check", e.board)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	unicoJSON(t, out)
}
