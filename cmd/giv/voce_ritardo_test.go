package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/story"
)

// CLI-888: ritardo_voce per scena.

func ritardoBoard(extra1 string) string {
	return `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"montaggio":{"sottotitoli":"s.ass"},
"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":3,"voce":"v.wav"` + extra1 + `},{"id":"s2","tipo":"clip","file":"a.mp4","durata":3,"voce":"v.wav"}]}`
}

func ritardoPlan(t *testing.T, board string) montage.Plan {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "sb.json")
	os.WriteFile(p, []byte(board), 0o644)
	for _, f := range []string{"a.mp4", "v.wav", "s.ass"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	s, err := story.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := cutPlan(s, p)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// casi 1 e 2: il piano di montaggio porta il ritardo solo dove indicato.
func TestStoryCutRitardoVocePiano(t *testing.T) {
	plan := ritardoPlan(t, ritardoBoard(`,"ritardo_voce":0.75`))
	if plan.Clips[0].VoiceAt != 0.75 || plan.Clips[1].VoiceAt != 0 {
		t.Errorf("VoiceAt = %v, %v (attesi 0.75, 0)", plan.Clips[0].VoiceAt, plan.Clips[1].VoiceAt)
	}
	args, err := montage.Args(plan)
	if err != nil {
		t.Fatal(err)
	}
	graph := ""
	for i, a := range args {
		if a == "-filter_complex" {
			graph = args[i+1]
		}
	}
	if !strings.Contains(graph, "adelay=delays=") {
		t.Errorf("manca adelay nel grafo: %s", graph)
	}
	// senza il campo: stesso piano di oggi (VoiceAt 0 ovunque)
	plain := ritardoPlan(t, ritardoBoard(""))
	for i, c := range plain.Clips {
		if c.VoiceAt != 0 {
			t.Errorf("clip %d: VoiceAt %v senza campo", i, c.VoiceAt)
		}
	}
	explicit := ritardoPlan(t, ritardoBoard(`,"ritardo_voce":0`))
	a1, _ := montage.Args(plain)
	a2, _ := montage.Args(explicit)
	norm := func(a []string, pl montage.Plan) string {
		return strings.ReplaceAll(strings.Join(a, "\x00"), filepath.Dir(pl.Output), "DIR")
	}
	if norm(a1, plain) != norm(a2, explicit) {
		t.Errorf("ritardo_voce 0 diverso dall'assenza del campo")
	}
}

// caso 3: valori non validi, codice 2 e nessuna chiamata.
func TestStoryCheckRitardoVoceNonValido(t *testing.T) {
	n := capsServer(t)
	for name, extra := range map[string]string{
		"negativo":   `,"ritardo_voce":-1`,
		"stringa":    `,"ritardo_voce":"due"`,
		"enorme":     `,"ritardo_voce":1e400`,
		"senza voce": `,"voce":"","ritardo_voce":1`,
	} {
		dir := t.TempDir()
		board := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":3,"voce":"v.wav"` + extra + `}]}`
		p := filepath.Join(dir, "sb.json")
		os.WriteFile(p, []byte(board), 0o644)
		_, se, code, starts := storyRun(t, p, n)
		if code != 2 || starts != 0 {
			t.Errorf("%s: code %d starts %d (atteso 2, 0): %s", name, code, starts, se)
		}
	}
}

// caso 4: ritardo + durata della voce oltre la fine della scena.
func TestStoryCheckRitardoVoceAvviso(t *testing.T) {
	cutNeedFF(t)
	n := capsServer(t)
	dir := t.TempDir()
	cutClip(t, filepath.Join(dir, "a.mp4"), "160x90", 24, "3")
	cutFF(t, "-f", "lavfi", "-i", "sine=frequency=500:duration=1.5", filepath.Join(dir, "v.wav"))
	board := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":3,"voce":"v.wav","ritardo_voce":2},
{"id":"s2","tipo":"clip","file":"a.mp4","durata":3,"voce":"v.wav","ritardo_voce":1}]}`
	p := filepath.Join(dir, "sb.json")
	os.WriteFile(p, []byte(board), 0o644)
	out, se, code, _ := storyRun(t, p, n)
	if code != 0 {
		t.Fatalf("code %d\n%s", code, se)
	}
	var r struct {
		Scenes []struct {
			ID       string   `json:"id"`
			Delay    float64  `json:"voice_delay_s"`
			Warnings []string `json:"warnings"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(r.Scenes) != 2 {
		t.Fatalf("scene %d", len(r.Scenes))
	}
	s1, s2 := r.Scenes[0], r.Scenes[1]
	if s1.Delay != 2 || len(s1.Warnings) != 1 || !strings.Contains(s1.Warnings[0], "oltre la fine della scena") {
		t.Errorf("s1: %+v", s1)
	}
	if len(s2.Warnings) != 0 { // 1 + 1,5 = 2,5 <= 3
		t.Errorf("s2 non doveva avvisare: %+v", s2)
	}
	if !strings.Contains(se, "avviso: scena s1:") || strings.Contains(se, "scena s2:") {
		t.Errorf("stderr: %q", se)
	}
}
