package main

import (
	"bytes"
	"encoding/json"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// Test CLI-957: correzioni dalla prova reale (margini dei sottotitoli, logo e
// titolo nei formati verticali). Clip blu uniformi, nessuna rete.

// r957Frame legge il fotogramma a t secondi del file come immagine RGB.
func r957Frame(t *testing.T, f string, w, h int, at float64) []byte {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-v", "error", "-ss", strconv.FormatFloat(at, 'f', 2, 64), "-i", f, "-frames:v", "1",
		"-f", "rawvideo", "-pix_fmt", "rgb24", "-")
	var b bytes.Buffer
	cmd.Stdout = &b
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if b.Len() != w*h*3 {
		t.Fatalf("fotogramma di %d byte, attesi %d (%dx%d)", b.Len(), w*h*3, w, h)
	}
	return b.Bytes()
}

// r957Box è il riquadro dei pixel che soddisfano pred (vuoto se nessuno).
func r957Box(px []byte, w, h int, pred func(r, g, b byte) bool) (box image.Rectangle, n int) {
	box = image.Rect(w, h, 0, 0)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 3
			if pred(px[i], px[i+1], px[i+2]) {
				n++
				box = box.Union(image.Rect(x, y, x+1, y+1))
				if n == 1 {
					box = image.Rect(x, y, x+1, y+1)
				}
			}
		}
	}
	return
}

func r957White(r, g, b byte) bool { return r > 200 && g > 200 && b > 200 }
func r957Red(r, g, b byte) bool   { return r > 180 && g < 80 && b < 80 }

func r957Cut(t *testing.T, d, montaggio string, format string) (file string, w, h int) {
	t.Helper()
	p := r6Board(t, d, montaggio, "")
	out, se, code := r6Cmd(t, "cut", "--formato", format, p)
	if code != 0 {
		t.Fatalf("cut %s: codice %d\n%s", format, code, se)
	}
	var r r6Cut
	if err := json.Unmarshal([]byte(out), &r); err != nil || len(r.Formats) != 1 {
		t.Fatalf("%v\n%s", err, out)
	}
	return r.Formats[0].Output, r.Formats[0].Width, r.Formats[0].Height
}

// In 9:16 i sottotitoli stanno sopra il margine basso sicuro (non nel 35% basso).
func TestR957SottotitoliSopraMargineBasso(t *testing.T) {
	d := r6Dir(t)
	r6Solid(t, d)
	srt := "1\n00:00:00,000 --> 00:00:03,000\nSottotitolo di prova\n"
	if err := os.WriteFile(filepath.Join(d, "sub.srt"), []byte(srt), 0o644); err != nil {
		t.Fatal(err)
	}
	f, w, h := r957Cut(t, d, `"formati":["9:16"],"sottotitoli":"sub.srt"`, "9:16")
	box, n := r957Box(r957Frame(t, f, w, h, 1), w, h, r957White)
	if n < 20 {
		t.Fatalf("sottotitolo non visibile (%d pixel bianchi)", n)
	}
	if limit := int(float64(h) * 0.65); box.Max.Y > limit {
		t.Errorf("sottotitolo fino a y=%d su %d: oltre il 65%% (%d), cioè nel 35%% basso", box.Max.Y, h, limit)
	}
}

// In 9:16 e 4:5, logo e titolo nelle posizioni predefinite non si sovrappongono.
func TestR957LogoETitoloNonSiSovrappongono(t *testing.T) {
	for _, format := range []string{"9:16", "4:5"} {
		d := r6Dir(t)
		r6Solid(t, d)
		f, w, h := r957Cut(t, d, `"formati":["`+format+`"],"logo":{"file":"logo.png"},"titoli":[{"testo":"Titolo di prova","da":0,"durata":2.5}]`, format)
		px := r957Frame(t, f, w, h, 1)
		logo, nl := r957Box(px, w, h, r957Red)
		title, nt := r957Box(px, w, h, r957White)
		if nl < 20 || nt < 20 {
			t.Fatalf("%s: logo (%d px) o titolo (%d px) non visibili", format, nl, nt)
		}
		if logo.Overlaps(title) {
			t.Errorf("%s: logo %v e titolo %v si sovrappongono", format, logo, title)
		}
	}
}

// 16:9 senza campi nuovi: stessa uscita di prima (formato dichiarato, nessun logo/testo, dimensioni attese).
func TestR957Senza16x9CampiNuoviInvariato(t *testing.T) {
	d := r6Dir(t)
	p := r6Board(t, d, `"transizione":"dissolvenza"`, "")
	out, se, code := r6Cmd(t, "cut", p)
	if code != 0 {
		t.Fatalf("cut: codice %d\n%s", code, se)
	}
	var r struct {
		Width, Height int
		DurationS     float64 `json:"duration_s"`
	}
	json.Unmarshal([]byte(out), &r)
	if r.Width != 256 || r.Height != 144 || r.DurationS < 3.4 || r.DurationS > 3.6 {
		t.Errorf("uscita 16:9 cambiata: %s", out)
	}
}
