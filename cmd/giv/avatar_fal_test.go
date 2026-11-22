package main

import (
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CLI-922 (O-T4): giv avatar e scena avatar contro il server fal finto di fal_test.go.

const avatarEP = "/fal-ai/kling-video/ai-avatar/v2/standard"

func avatarCmd(args ...string) (out, errOut string, code int) {
	capture := func(f **os.File) func() string {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		return func() string { w.Close(); *f = old; return <-done }
	}
	so, se := capture(&os.Stdout), capture(&os.Stderr)
	err := cmdAvatar(append([]string{"--provider", "fal"}, args...))
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

func avatarWav(t *testing.T, name, dur string) string {
	cutNeedFF(t)
	p := filepath.Join(t.TempDir(), name)
	cutFF(t, "-f", "lavfi", "-i", "sine=frequency=400:duration="+dur, p)
	return p
}

// caso 1: due caricamenti, avvio, attesa, scaricamento, manifest con la stima.
func TestAvatarFalCompleto(t *testing.T) {
	e := newFalEnv(t)
	img, wav := falPNG(t, "a.png"), avatarWav(t, "v.wav", "2.4")
	out, se, code := avatarCmd("--image", img, "--audio", wav, "--out", "o", "parla")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, se)
	}
	var man map[string]any
	if err := json.Unmarshal([]byte(out), &man); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if man["command"] != "avatar" || man["provider"] != "fal" {
		t.Errorf("manifest %v", man)
	}
	if c, _ := man["cost_est_usd"].(float64); math.Abs(c-3*0.0562) > 1e-6 { // ceil(2,4 s) x 0,0562
		t.Errorf("cost_est_usd %v (atteso 0.1686)", man["cost_est_usd"])
	}
	if n := e.count("POST", "/storage/upload/initiate"); n != 2 {
		t.Errorf("initiate %d (attesi 2: immagine e audio)", n)
	}
	e.mu.Lock()
	puts := 0
	for _, r := range e.other {
		if r.Method == "PUT" {
			puts++
			if r.Auth != "" {
				t.Errorf("PUT con Authorization")
			}
		}
	}
	var start map[string]any
	for _, r := range e.reqs {
		if r.Method == "POST" && r.Path == avatarEP {
			json.Unmarshal(r.Body, &start)
		}
	}
	e.mu.Unlock()
	if puts != 2 {
		t.Errorf("PUT %d (attesi 2)", puts)
	}
	iu, _ := start["image_url"].(string)
	au, _ := start["audio_url"].(string)
	if !strings.HasPrefix(iu, falStore) || !strings.HasPrefix(au, falStore) || iu == au {
		t.Errorf("corpo avvio %v", start)
	}
	if n := e.count("POST", avatarEP); n != 1 {
		t.Errorf("avvii %d", n)
	}
	if f, _ := filepath.Glob("o/*.mp4"); len(f) != 1 {
		t.Errorf("file scaricati %v", f)
	}
}

// caso 2: file mancanti o del tipo sbagliato, codice 2 e nessuna richiesta.
func TestAvatarFalRifiuti(t *testing.T) {
	e := newFalEnv(t)
	img, wav := falPNG(t, "a.png"), avatarWav(t, "v.wav", "1")
	ogg := filepath.Join(t.TempDir(), "v.ogg")
	os.WriteFile(ogg, []byte("OggS"), 0o644)
	txt := filepath.Join(t.TempDir(), "i.png")
	os.WriteFile(txt, []byte("non sono un'immagine"), 0o644)
	cases := map[string][]string{
		"senza immagine":   {"--audio", wav},
		"senza audio":      {"--image", img},
		"immagine assente": {"--image", "/nonesiste.png", "--audio", wav},
		"audio assente":    {"--image", img, "--audio", "/nonesiste.wav"},
		"audio ogg":        {"--image", img, "--audio", ogg},
		"immagine finta":   {"--image", txt, "--audio", wav},
	}
	for name, args := range cases {
		if _, se, code := avatarCmd(args...); code != 2 {
			t.Errorf("%s: code %d (atteso 2): %s", name, code, se)
		}
	}
	if n := len(e.reqs) + len(e.other); n != 0 {
		t.Errorf("richieste: %d", n)
	}
}

// caso 3: dry-run e tetto.
func TestAvatarFalDryRunETetto(t *testing.T) {
	e := newFalEnv(t)
	img, wav := falPNG(t, "a.png"), avatarWav(t, "v.wav", "2")
	out, se, code := avatarCmd("--dry-run", "--image", img, "--audio", wav)
	if code != 0 {
		t.Fatalf("code %d %s", code, se)
	}
	var p struct {
		Cost *float64 `json:"cost_est_usd"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil || p.Cost == nil || math.Abs(*p.Cost-0.1124) > 1e-6 {
		t.Errorf("stima %v: %s", p.Cost, out)
	}
	if _, se, code := avatarCmd("--image", img, "--audio", wav, "--max-cost", "0.05"); code != 2 {
		t.Errorf("max-cost: code %d: %s", code, se)
	}
	if n := len(e.reqs) + len(e.other); n != 0 {
		t.Errorf("richieste: %d", n)
	}
}

func avatarBoard(extra string) string {
	return `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"}` + extra + `,
"scene":[{"id":"a1","tipo":"avatar","immagine":"p.png","voce":"v.wav"}]}`
}

func avatarProject(t *testing.T, board string) string {
	dir := t.TempDir()
	cutFF(t, "-f", "lavfi", "-i", "sine=frequency=400:duration=2", filepath.Join(dir, "v.wav"))
	b, _ := os.ReadFile(falPNG(t, "p.png"))
	os.WriteFile(filepath.Join(dir, "p.png"), b, 0o644)
	p := filepath.Join(dir, "sb.json")
	os.WriteFile(p, []byte(board), 0o644)
	return p
}

// caso 4: check accetta e stima (senza FAL_KEY), run genera e registra nello stato.
func TestStoryAvatarCheckERun(t *testing.T) {
	cutNeedFF(t)
	n := capsServer(t)
	e := newFalEnv(t)
	t.Setenv("FAL_KEY", "")
	p := avatarProject(t, avatarBoard(""))

	out, se, code, _ := storyRun(t, p, n)
	if code != 0 {
		t.Fatalf("check code %d\n%s", code, se)
	}
	var r struct {
		Scenes []struct {
			ID       string  `json:"id"`
			Provider string  `json:"provider"`
			Cost     float64 `json:"cost_est_usd"`
		} `json:"scenes"`
		Total float64 `json:"cost_est_usd"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || len(r.Scenes) != 1 {
		t.Fatalf("%v\n%s", err, out)
	}
	if s := r.Scenes[0]; s.Provider != "fal" || math.Abs(s.Cost-2*0.0562) > 1e-6 {
		t.Errorf("scena %+v (attesa fal, 0.1124)", s)
	}
	if len(e.reqs)+len(e.other) != 0 {
		t.Fatalf("check ha fatto richieste: %d", len(e.reqs)+len(e.other))
	}

	t.Setenv("FAL_KEY", falKey)
	if _, se, code := cutRun(t, "run", p); code != 0 {
		t.Fatalf("run code %d\n%s", code, se)
	}
	if c := e.count("POST", avatarEP); c != 1 {
		t.Errorf("avvii %d", c)
	}
	raw, err := os.ReadFile(strings.TrimSuffix(p, ".json") + ".stato.json")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Scenes map[string]struct {
			Status string `json:"status"`
			Clip   *struct {
				File string `json:"file"`
			} `json:"clip"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	a := st.Scenes["a1"]
	if a.Status != "fatto" || a.Clip == nil {
		t.Fatalf("stato %s", raw)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(p), a.Clip.File)); err != nil {
		t.Errorf("clip assente: %v", err)
	}
	// secondo run: la scena è fatta, nessun nuovo avvio
	if _, se, code := cutRun(t, "run", p); code != 0 {
		t.Fatalf("run2 code %d\n%s", code, se)
	}
	if c := e.count("POST", avatarEP); c != 1 {
		t.Errorf("avvii dopo il secondo run: %d", c)
	}
}

// caso 4 (rifiuti): senza FAL_KEY run non parte e non scrive lo stato.
func TestStoryAvatarRunSenzaChiave(t *testing.T) {
	cutNeedFF(t)
	capsServer(t)
	e := newFalEnv(t)
	t.Setenv("FAL_KEY", "")
	p := avatarProject(t, avatarBoard(""))
	if _, _, code := cutRun(t, "run", p); code == 0 {
		t.Errorf("run senza chiave riuscito")
	}
	if len(e.reqs)+len(e.other) != 0 {
		t.Errorf("richieste: %d", len(e.reqs)+len(e.other))
	}
	if _, err := os.Stat(strings.TrimSuffix(p, ".json") + ".stato.json"); err == nil {
		t.Errorf("stato scritto senza chiave")
	}
}

// fix security: caricamento senza ritentativi e solo verso lo storage di fal.
func TestAvatarFalCaricamentoRifiuti(t *testing.T) {
	e := newFalEnv(t)
	img, wav := falPNG(t, "a.png"), avatarWav(t, "v.wav", "1")
	e.initStatus = 503
	if _, _, code := avatarCmd("--image", img, "--audio", wav); code == 0 {
		t.Errorf("initiate 503 accettato")
	}
	if n := e.count("POST", "/storage/upload/initiate"); n != 1 {
		t.Errorf("initiate %d (un solo tentativo)", n)
	}
	e.initStatus, e.badHost = 0, true
	if _, se, code := avatarCmd("--image", img, "--audio", wav); code == 0 || !strings.Contains(se, "fuori dallo storage") {
		t.Errorf("upload_url su altro host: code %d %q", code, se)
	}
	if len(e.other) != 0 {
		t.Errorf("richieste all'host non fal: %d", len(e.other))
	}
	if n := e.count("POST", avatarEP); n != 0 {
		t.Errorf("avvii %d", n)
	}
}

// fix security: con un tetto e senza durata misurata (ffprobe assente) la scena
// avatar non è stimabile: codice 2, anche con costo dichiarato, nessuno stato.
func TestStoryAvatarTettoSenzaDurata(t *testing.T) {
	cutNeedFF(t)
	n := capsServer(t)
	newFalEnv(t)
	p := avatarProject(t, strings.Replace(avatarBoard(`,"tetto_costo_usd":5`), `"voce":"v.wav"`, `"voce":"v.wav","costo_dichiarato_usd":0.2`, 1))
	t.Setenv("PATH", t.TempDir())
	if _, se, code, _ := storyRun(t, p, n); code != 2 {
		t.Errorf("check: code %d: %s", code, se)
	}
	if _, se, code := cutRun(t, "run", p); code != 2 {
		t.Errorf("run: code %d: %s", code, se)
	}
	if _, err := os.Stat(strings.TrimSuffix(p, ".json") + ".stato.json"); err == nil {
		t.Errorf("stato scritto")
	}
}
