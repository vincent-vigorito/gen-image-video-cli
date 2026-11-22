package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// CLI-924 (O-T5): Kling 4.0 su fal con --keyframe e --shot, contro il server finto.

const k4EP = "/kling/v4/image-to-video"

func (e *falEnv) startBody(t *testing.T, path string) map[string]any {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range e.reqs {
		if r.Method == "POST" && r.Path == path {
			var b map[string]any
			if err := json.Unmarshal(r.Body, &b); err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
	t.Fatalf("nessun avvio su %s", path)
	return nil
}

func bodyKeys(m map[string]any) []string {
	k := make([]string, 0, len(m))
	for n := range m {
		k = append(k, n)
	}
	sort.Strings(k)
	return k
}

// caso 1: fotogrammi intermedi caricati, in ordine, in middle_image_urls.
func TestKling4Keyframe(t *testing.T) {
	e := newFalEnv(t)
	img, k1, k2 := falPNG(t, "a.png"), falPNG(t, "k1.png"), falPNG(t, "k2.png")
	out, se, code := falVideo(t, "--model", "kling-4.0", "--image", img, "--keyframe", k1, "--keyframe", k2,
		"--duration", "6", "--out", "o", "una scena")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, se)
	}
	if n := e.count("POST", "/storage/upload/initiate"); n != 3 {
		t.Errorf("caricamenti %d (attesi 3)", n)
	}
	b := e.startBody(t, k4EP)
	mid, _ := b["middle_image_urls"].([]any)
	if len(mid) != 2 || mid[0] == mid[1] {
		t.Fatalf("middle_image_urls %v", b["middle_image_urls"])
	}
	// ordine: start = 1° caricamento, poi k1, k2
	if !strings.HasSuffix(b["start_image_url"].(string), "/1.png") ||
		!strings.HasSuffix(mid[0].(string), "/2.png") || !strings.HasSuffix(mid[1].(string), "/3.png") {
		t.Errorf("ordine dei fotogrammi: start=%v mid=%v", b["start_image_url"], mid)
	}
	if b["duration"] != float64(6) || b["resolution"] != "1080p" || b["prompt"] != "una scena" {
		t.Errorf("corpo %v", b)
	}
	if !strings.Contains(out, "keyframes") {
		t.Errorf("manifest senza i fotogrammi: %s", out)
	}
}

// caso 2: --shot ripetuto, multi_prompt e durata = somma.
func TestKling4Shot(t *testing.T) {
	e := newFalEnv(t)
	img := falPNG(t, "a.png")
	_, se, code := falVideo(t, "--model", "kling-4.0", "--image", img, "--shot", "3:campo largo", "--shot", "4:primo piano", "--out", "o")
	if code != 0 {
		t.Fatalf("code %d\n%s", code, se)
	}
	b := e.startBody(t, k4EP)
	want := []any{map[string]any{"prompt": "campo largo", "duration": float64(3)}, map[string]any{"prompt": "primo piano", "duration": float64(4)}}
	if !reflect.DeepEqual(b["multi_prompt"], want) {
		t.Errorf("multi_prompt %v", b["multi_prompt"])
	}
	if b["duration"] != float64(7) {
		t.Errorf("duration %v (attesa la somma 7)", b["duration"])
	}
	if _, ok := b["prompt"]; ok {
		t.Errorf("prompt presente con --shot")
	}
}

// caso 3: rifiuti a codice 2, nessuna richiesta.
func TestKling4Rifiuti(t *testing.T) {
	e := newFalEnv(t)
	img, k := falPNG(t, "a.png"), falPNG(t, "k.png")
	many := []string{}
	for i := 0; i < 9; i++ {
		many = append(many, "--keyframe", k)
	}
	cases := map[string][]string{
		"keyframe su v3":        {"--model", "kling-v3-pro", "--image", img, "--keyframe", k, "--duration", "5", "x"},
		"keyframe oltre 8":      append([]string{"--model", "kling-4.0", "--image", img, "--duration", "5", "x"}, many...),
		"shot testo vuoto":      {"--model", "kling-4.0", "--image", img, "--shot", "3:"},
		"shot senza durata":     {"--model", "kling-4.0", "--image", img, "--shot", "testo"},
		"shot durata non num":   {"--model", "kling-4.0", "--image", img, "--shot", "a:testo"},
		"shot durata 0":         {"--model", "kling-4.0", "--image", img, "--shot", "0:testo"},
		"shot oltre 30 s":       {"--model", "kling-4.0", "--image", img, "--shot", "31:testo"},
		"shot v3 oltre 15 s":    {"--model", "kling-v3-pro", "--image", img, "--shot", "16:testo"},
		"shot con prompt":       {"--model", "kling-4.0", "--image", img, "--shot", "3:a", "un prompt"},
		"shot e durata diversa": {"--model", "kling-4.0", "--image", img, "--shot", "3:a", "--duration", "9"},
		"durata oltre 30":       {"--model", "kling-4.0", "--image", img, "--duration", "31", "x"},
		"kling4 senza image":    {"--model", "kling-4.0", "--duration", "5", "x"},
		"kling4 con aspect":     {"--model", "kling-4.0", "--image", img, "--aspect", "16:9", "--duration", "5", "x"},
		"seedance con keyframe": {"--model", "seedance-2.0-ref", "--ref", img, "--keyframe", k, "--duration", "5", "x"},
	}
	for name, args := range cases {
		if _, se, code := falVideo(t, args...); code != 2 {
			t.Errorf("%s: code %d (atteso 2): %s", name, code, se)
		}
	}
	for name, args := range map[string][]string{
		"keyframe vuoto":        {"--model", "kling-4.0", "--image", img, "--keyframe", "", "--duration", "5", "x"},
		"shot con durata < 0":   {"--model", "kling-4.0", "--image", img, "--shot", "3:a", "--duration", "-2"},
		"keyframe con userinfo": {"--model", "kling-4.0", "--image", img, "--keyframe", "https://utente:pw@example.com/k.png", "--duration", "5", "x"},
	} {
		if _, se, code := falVideo(t, args...); code != 2 {
			t.Errorf("%s: code %d (atteso 2): %s", name, code, se)
		}
	}
	// altri provider: stesso rifiuto, anche senza chiave né catalogo
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("FAL_KEY", "")
	if _, _, code := cutArgsVideoProv("openrouter", []string{"--image", img, "--keyframe", k, "--shot", "3:a"}); code != 2 {
		t.Errorf("openrouter senza chiave con --keyframe/--shot: code %d", code)
	}
	if _, _, code := falVideo(t, "--model", "kling-v3-pro", "--image", img, "--keyframe", k); code != 2 {
		t.Errorf("v3 senza chiave con --keyframe: code %d", code)
	}
	t.Setenv("FAL_KEY", falKey)
	t.Setenv("OPENROUTER_API_KEY", "fake")
	if _, _, code := cutArgsVideoProv("openrouter", []string{"--image", img, "--keyframe", k, "--shot", "3:a"}); code != 2 {
		t.Errorf("openrouter con --keyframe/--shot: code %d", code)
	}
	if n := len(e.reqs) + len(e.other); n != 0 {
		t.Errorf("richieste: %d", n)
	}
}

// caso 4: dry-run e tetto.
func TestKling4DryRunETetto(t *testing.T) {
	e := newFalEnv(t)
	img := falPNG(t, "a.png")
	out, se, code := falVideo(t, "--dry-run", "--model", "kling-4.0", "--image", img, "--shot", "3:a", "--shot", "2:b")
	if code != 0 {
		t.Fatalf("code %d %s", code, se)
	}
	var p struct {
		Cost *float64 `json:"cost_est_usd"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil || p.Cost == nil || math.Abs(*p.Cost-5*0.235) > 1e-6 {
		t.Errorf("stima %v (attesa 1.175 = 5 s x 0,235 a 1080p): %s", p.Cost, out)
	}
	if _, se, code := falVideo(t, "--model", "kling-4.0", "--image", img, "--duration", "10", "--max-cost", "1", "x"); code != 2 {
		t.Errorf("max-cost: code %d: %s", code, se)
	}
	if n := len(e.reqs) + len(e.other); n != 0 {
		t.Errorf("richieste: %d", n)
	}
}

// caso 6: senza i flag nuovi il corpo di v3 è quello di prima (nessun campo nuovo).
func TestFalV3CorpoInvariato(t *testing.T) {
	e := newFalEnv(t)
	img := falPNG(t, "a.png")
	if _, se, code := falVideo(t, "--image", img, "--duration", "5", "--out", "o", "x"); code != 0 {
		t.Fatalf("code %d %s", code, se)
	}
	b := e.startBody(t, falEP)
	if got, want := bodyKeys(b), []string{"duration", "prompt", "start_image_url"}; !reflect.DeepEqual(got, want) {
		t.Errorf("campi %v (attesi %v)", got, want)
	}
	if b["duration"] != "5" {
		t.Errorf("duration %v", b["duration"])
	}
}

func k4Board(scene string) string {
	return `{"default":{"provider":"fal","modello":"kling-4.0"},"scene":[` + scene + `]}`
}

func k4Project(t *testing.T, board string) string {
	dir := t.TempDir()
	for _, n := range []string{"p.png", "k1.png", "k2.png"} {
		b, _ := os.ReadFile(falPNG(t, n))
		os.WriteFile(filepath.Join(dir, n), b, 0o644)
	}
	p := filepath.Join(dir, "sb.json")
	os.WriteFile(p, []byte(board), 0o644)
	return p
}

// caso 5: scena con fotogrammi intermedi e inquadrature: check valida e stima, run genera.
func TestStoryKling4ScenaCheckERun(t *testing.T) {
	n := capsServer(t)
	e := newFalEnv(t)
	p := k4Project(t, k4Board(`{"id":"s1","tipo":"video","immagine":"p.png","fotogrammi_intermedi":["k1.png","k2.png"],"inquadrature":[{"prompt":"largo","durata":3},{"prompt":"stretto","durata":4}]}`))
	out, se, code, _ := storyRun(t, p, n)
	if code != 0 {
		t.Fatalf("check code %d\n%s", code, se)
	}
	var r struct {
		Scenes []struct {
			Provider string  `json:"provider"`
			Cost     float64 `json:"cost_est_usd"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || len(r.Scenes) != 1 || r.Scenes[0].Provider != "fal" ||
		math.Abs(r.Scenes[0].Cost-7*0.235) > 1e-6 {
		t.Fatalf("check: %v %s", err, out)
	}
	if len(e.reqs)+len(e.other) != 0 {
		t.Fatalf("check ha fatto richieste")
	}
	t.Setenv("FAL_KEY", falKey)
	if _, se, code := cutRun(t, "run", p); code != 0 {
		t.Fatalf("run code %d\n%s", code, se)
	}
	b := e.startBody(t, k4EP)
	if mid, _ := b["middle_image_urls"].([]any); len(mid) != 2 {
		t.Errorf("middle_image_urls %v", b["middle_image_urls"])
	}
	if mp, _ := b["multi_prompt"].([]any); len(mp) != 2 || b["duration"] != float64(7) {
		t.Errorf("corpo %v", b)
	}
}

// caso 5 (rifiuti): modello senza supporto o inquadrature incoerenti, codice 2.
func TestStoryKling4Rifiuti(t *testing.T) {
	n := capsServer(t)
	newFalEnv(t)
	cases := map[string]string{
		"prompt con inquadrature": `{"id":"s1","tipo":"video","immagine":"p.png","prompt":"x","inquadrature":[{"prompt":"a","durata":3}]}`,
		"durata diversa":          `{"id":"s1","tipo":"video","immagine":"p.png","durata":9,"inquadrature":[{"prompt":"a","durata":3}]}`,
		"inquadratura vuota":      `{"id":"s1","tipo":"video","immagine":"p.png","inquadrature":[{"prompt":"","durata":3}]}`,
		"durata inquadratura 0":   `{"id":"s1","tipo":"video","immagine":"p.png","inquadrature":[{"prompt":"a","durata":0}]}`,
		"fotogrammi su clip":      `{"id":"s1","tipo":"clip","file":"p.png","durata":3,"fotogrammi_intermedi":["k1.png"]}`,
	}
	for name, sc := range cases {
		p := k4Project(t, k4Board(sc))
		if _, se, code, _ := storyRun(t, p, n); code != 2 {
			t.Errorf("%s: code %d: %s", name, code, se)
		}
	}
	// modello senza supporto (v3) con fotogrammi intermedi
	p := k4Project(t, `{"default":{"provider":"fal","modello":"kling-v3-pro"},"scene":[{"id":"s1","tipo":"video","immagine":"p.png","durata":5,"prompt":"x","fotogrammi_intermedi":["k1.png"]}]}`)
	if _, se, code, _ := storyRun(t, p, n); code != 2 {
		t.Errorf("v3 con fotogrammi: code %d: %s", code, se)
	}
}
