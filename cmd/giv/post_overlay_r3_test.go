package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Test R-T3 (CLI-943): giv post overlay. Sfondo blu pieno, logo rosso, binario vero.

const (
	r3W = 320
	r3H = 240
)

func r3Setup(t *testing.T, dur string) string {
	t.Helper()
	d := t.TempDir()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=0x0000ff:size=320x240:rate=25:duration="+dur,
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+dur, "-shortest", "-pix_fmt", "yuv420p", "-y", filepath.Join(d, "in.mp4")).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
	for name, c := range map[string]color.RGBA{"logo.png": {255, 0, 0, 255}, "card.png": {0, 255, 0, 255}} {
		img := image.NewRGBA(image.Rect(0, 0, 100, 100))
		for y := 0; y < 100; y++ {
			for x := 0; x < 100; x++ {
				img.Set(x, y, c)
			}
		}
		f, _ := os.Create(filepath.Join(d, name))
		png.Encode(f, img)
		f.Close()
	}
	os.WriteFile(filepath.Join(d, "falso.png"), []byte("non sono un'immagine"), 0o644)
	return d
}

func r3Run(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	var so bytes.Buffer
	cmd.Stdout = &so
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), code
}

// r3Overlay lancia giv post overlay e restituisce il percorso del file prodotto.
func r3Overlay(t *testing.T, dir string, args ...string) string {
	t.Helper()
	so, code := r3Run(t, dir, append([]string{"post", "overlay", "--video", "in.mp4", "--out", "o"}, args...)...)
	if code != 0 {
		t.Fatalf("overlay %v: codice %d\n%s", args, code, so)
	}
	var m struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(so), &m); err != nil || len(m.Files) != 1 {
		t.Fatalf("manifest: %v\n%s", err, so)
	}
	p := m.Files[0].Path
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return p
}

func r3Probe(t *testing.T, path, entries string) []string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", entries, "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(out)), ",")
}

// r3Frame restituisce i pixel rgb24 del fotogramma a t secondi.
func r3Frame(t *testing.T, path string, at float64) []byte {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-ss", strconv.FormatFloat(at, 'f', 2, 64), "-i", path, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "rgb24", "-").Output()
	if err != nil || len(out) != r3W*r3H*3 {
		t.Fatalf("fotogramma a %.2f: %v (%d byte)", at, err, len(out))
	}
	return out
}

// r3Box: riquadro dei pixel per cui pred e' vero (conteggio e limiti).
func r3Box(px []byte, pred func(r, g, b byte) bool) (n, x0, y0, x1, y1 int) {
	x0, y0, x1, y1 = r3W, r3H, -1, -1
	for y := 0; y < r3H; y++ {
		for x := 0; x < r3W; x++ {
			i := (y*r3W + x) * 3
			if pred(px[i], px[i+1], px[i+2]) {
				n++
				x0, y0 = min(x0, x), min(y0, y)
				x1, y1 = max(x1, x), max(y1, y)
			}
		}
	}
	return
}

func isRed(r, g, b byte) bool   { return r > 200 && g < 60 && b < 60 }
func notBlue(r, g, b byte) bool { return r > 100 || g > 100 || b < 150 }

func TestR3Logo(t *testing.T) {
	d := r3Setup(t, "3")
	cases := []struct {
		pos        string
		right, low bool
	}{{"alto-destra", true, false}, {"basso-sinistra", false, true}}
	for _, c := range cases {
		p := r3Overlay(t, d, "--logo", "logo.png", "--logo-pos", c.pos, "--logo-width", "0.25")
		wh := r3Probe(t, p, "stream=width,height,duration")
		if wh[0] != "320" || wh[1] != "240" {
			t.Fatalf("%s: dimensioni %v", c.pos, wh)
		}
		if dur, _ := strconv.ParseFloat(wh[2], 64); math.Abs(dur-3) > 0.15 {
			t.Fatalf("%s: durata %v", c.pos, dur)
		}
		n, x0, y0, x1, y1 := r3Box(r3Frame(t, p, 1), isRed)
		if n == 0 {
			t.Fatalf("%s: logo assente", c.pos)
		}
		if w := x1 - x0 + 1; w < 70 || w > 90 {
			t.Errorf("%s: larghezza logo %d, attesa ~80", c.pos, w)
		}
		if c.low != (y0 > r3H/2) {
			t.Errorf("%s: logo a y %d-%d", c.pos, y0, y1)
		}
		if !c.right && x1 >= r3W/2 || c.right && x0 < r3W/2 {
			t.Errorf("%s: logo nel lato sbagliato x %d-%d", c.pos, x0, x1)
		}
	}
}

func TestR3TitoloLetteraleENellaFinestra(t *testing.T) {
	d := r3Setup(t, "4")
	titles := []string{
		"Ora: sconto, 20% \\ {e} 'x'",
		"{\\an9}ALTO\\Nriga2",
		"riga uno\nriga due",
	}
	for _, title := range titles {
		p := r3Overlay(t, d, "--title", title, "--from", "1", "--duration", "1.5", "--pos", "basso")
		if wh := r3Probe(t, p, "stream=width,height"); wh[0] != "320" || wh[1] != "240" {
			t.Fatalf("dimensioni %v", wh)
		}
		for _, at := range []float64{0.5, 3.2} {
			if n, _, _, _, _ := r3Box(r3Frame(t, p, at), notBlue); n != 0 {
				t.Errorf("%q: %d pixel di testo fuori finestra a %.1fs", title, n, at)
			}
		}
		n, _, y0, _, _ := r3Box(r3Frame(t, p, 1.8), notBlue)
		if n == 0 {
			t.Errorf("%q: titolo non visibile nella finestra", title)
		}
		// pos basso: nessuna direttiva {\an9} interpretata, il testo non sale in alto
		if y0 < r3H/2 {
			t.Errorf("%q: testo in alto (y0=%d), direttiva interpretata?", title, y0)
		}
	}
}

func TestR3CartelloFinale(t *testing.T) {
	d := r3Setup(t, "3")
	p := r3Overlay(t, d, "--end-card", "card.png", "--end-duration", "2")
	wh := r3Probe(t, p, "stream=width,height,duration")
	if dur, _ := strconv.ParseFloat(wh[2], 64); math.Abs(dur-5) > 0.2 {
		t.Fatalf("durata %v, attesa 5", dur)
	}
	green := func(r, g, b byte) bool { return g > 200 && r < 60 && b < 60 }
	if n, _, _, _, _ := r3Box(r3Frame(t, p, 4), green); n == 0 {
		t.Error("cartello non visibile nell'ultimo tratto")
	}
}

func TestR3RifiutiCodice2(t *testing.T) {
	d := r3Setup(t, "3")
	cases := [][]string{
		{"--logo", "manca.png"},
		{"--logo", "falso.png"},
		{"--logo", "logo.png", "--logo-pos", "centro-mondo"},
		{"--title", "ciao", "--from", "9", "--duration", "1"},
		{},
	}
	for _, c := range cases {
		so, code := r3Run(t, d, append([]string{"post", "overlay", "--video", "in.mp4", "--out", "o"}, c...)...)
		if code != 2 {
			t.Errorf("overlay %v: codice %d, atteso 2\n%s", c, code, so)
		}
	}
	if _, err := os.Stat(filepath.Join(d, "o")); err == nil {
		t.Error("i rifiuti hanno creato la cartella d'uscita")
	}
}

func TestR3DryRunNienteFile(t *testing.T) {
	d := r3Setup(t, "3")
	so, code := r3Run(t, d, "post", "overlay", "--video", "in.mp4", "--out", "o", "--logo", "logo.png", "--title", "x", "--from", "1", "--duration", "1", "--end-card", "card.png", "--dry-run")
	if code != 0 || !json.Valid([]byte(so)) {
		t.Fatalf("dry-run: codice %d\n%s", code, so)
	}
	if es, err := os.ReadDir(filepath.Join(d, "o")); err == nil && len(es) > 0 {
		t.Error("--dry-run ha scritto file")
	}
}
