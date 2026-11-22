package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Test R-T2 (CLI-941): giv post reframe. Materiale sintetico (testsrc 16:9, sine), binario vero.

const (
	r2SrcW = 320
	r2SrcH = 180
)

func r2Video(t *testing.T, dir string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		fmt.Sprintf("testsrc=size=%dx%d:rate=25:duration=3", r2SrcW, r2SrcH),
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-shortest", "-pix_fmt", "yuv420p", "-y",
		filepath.Join(dir, "in.mp4")).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
}

func r2Run(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	var so bytes.Buffer
	cmd.Stdout = &so
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return so.String(), ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), 0
}

// r2Reframe lancia giv post reframe e restituisce il percorso del file prodotto.
func r2Reframe(t *testing.T, dir, out string, extra ...string) string {
	t.Helper()
	args := append([]string{"post", "reframe", "--video", "in.mp4", "--out", out}, extra...)
	so, code := r2Run(t, dir, args...)
	if code != 0 {
		t.Fatalf("giv %v: codice %d\n%s", args, code, so)
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

func r2Probe(t *testing.T, path, sel, entries string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", sel, "-show_entries", "stream="+entries, "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

func r2Size(t *testing.T, path string) (int, int) {
	var w, h int
	if _, err := fmt.Sscanf(r2Probe(t, path, "v:0", "width,height"), "%d,%d", &w, &h); err != nil {
		t.Fatal(err)
	}
	return w, h
}

func r2Dur(t *testing.T, path, sel string) float64 {
	f, err := strconv.ParseFloat(r2Probe(t, path, sel, "duration"), 64)
	if err != nil {
		t.Fatalf("durata %s di %s: %v", sel, path, err)
	}
	return f
}

// r2Gray estrae un fotogramma a t=1 s, scalato a w×h (opzionalmente ritagliato in
// "crop=..." prima), come byte di grigio.
func r2Gray(t *testing.T, path string, vf string) []byte {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-ss", "1", "-i", path, "-frames:v", "1",
		"-vf", vf, "-f", "rawvideo", "-pix_fmt", "gray", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg gray: %v", err)
	}
	return out
}

func r2Diff(a, b []byte) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 255
	}
	var s float64
	for i := range a {
		s += math.Abs(float64(a[i]) - float64(b[i]))
	}
	return s / float64(len(a))
}

// r2Ratio controlla che le misure siano nel rapporto aw:ah (1% di tolleranza).
func r2Ratio(t *testing.T, w, h, aw, ah int) {
	t.Helper()
	if w%2 != 0 || h%2 != 0 {
		t.Errorf("misure dispari %dx%d", w, h)
	}
	if math.Abs(float64(w)/float64(h)-float64(aw)/float64(ah)) > 0.01*float64(aw)/float64(ah) {
		t.Errorf("uscita %dx%d non è %d:%d", w, h, aw, ah)
	}
}

var r2Aspects = []struct {
	name   string
	aw, ah int
}{{"9:16", 9, 16}, {"1:1", 1, 1}, {"4:5", 4, 5}}

func TestR2CropRapportoDurataAudio(t *testing.T) {
	d := t.TempDir()
	r2Video(t, d)
	for _, a := range r2Aspects {
		p := r2Reframe(t, d, "o", "--aspect", a.name, "--fit", "crop")
		w, h := r2Size(t, p)
		r2Ratio(t, w, h, a.aw, a.ah)
		if r2Probe(t, p, "a:0", "codec_name") == "" {
			t.Errorf("%s: audio perso", a.name)
		}
		if v, au := r2Dur(t, p, "v:0"), r2Dur(t, p, "a:0"); math.Abs(v-3) > 0.15 || math.Abs(au-3) > 0.15 {
			t.Errorf("%s: durata video %.3f audio %.3f, attesa 3", a.name, v, au)
		}
	}
}

// r2Center verifica che il fotogramma originale intero (scalato alla larghezza
// dell'uscita) stia al centro dell'uscita: vale per pad e blur.
func r2Center(t *testing.T, p string) {
	t.Helper()
	w, h := r2Size(t, p)
	sh := int(math.Round(float64(w) * r2SrcH / r2SrcW))
	ch := (sh - 6) &^ 1 // altezze pari: yuv420
	got := r2Gray(t, p, fmt.Sprintf("crop=%d:%d:0:%d,scale=%d:%d:flags=area", w, ch, ((h-sh)/2+3)&^1, w/6, ch/6))
	want := r2Gray(t, filepath.Join(filepath.Dir(p), "..", "in.mp4"), fmt.Sprintf("scale=%d:%d:flags=bicubic,crop=%d:%d:0:2,scale=%d:%d:flags=area", w, sh, w, ch, w/6, ch/6))
	flip := r2Gray(t, filepath.Join(filepath.Dir(p), "..", "in.mp4"), fmt.Sprintf("scale=%d:%d:flags=bicubic,crop=%d:%d:0:2,hflip,scale=%d:%d:flags=area", w, sh, w, ch, w/6, ch/6))
	if len(got) != len(want) {
		t.Errorf("%s: %d byte contro %d", filepath.Base(p), len(got), len(want))
	}
	if d, f := r2Diff(got, want), r2Diff(got, flip); d > 30 || d > f/2 {
		t.Errorf("%s (%dx%d): fotogramma originale non tutto visibile al centro, differenza media %.1f", filepath.Base(p), w, h, d)
	}
}

func TestR2PadBlurOriginaleVisibile(t *testing.T) {
	d := t.TempDir()
	r2Video(t, d)
	for _, fit := range []string{"pad", "blur"} {
		for _, a := range r2Aspects {
			p := r2Reframe(t, d, "o", "--aspect", a.name, "--fit", fit)
			w, h := r2Size(t, p)
			r2Ratio(t, w, h, a.aw, a.ah)
			r2Center(t, p)
		}
	}
	// blur: le bande non sono nere come in pad
	pb := r2Reframe(t, d, "o", "--aspect", "9:16", "--fit", "blur", "--name", "b")
	pp := r2Reframe(t, d, "o", "--aspect", "9:16", "--fit", "pad", "--name", "p")
	w, _ := r2Size(t, pb)
	top := func(p string) []byte { return r2Gray(t, p, fmt.Sprintf("crop=%d:20:0:0", w)) }
	if r2Diff(top(pb), top(pp)) < 5 {
		t.Error("blur e pad hanno le stesse bande")
	}
}

func TestR2FocusSpostaRitaglio(t *testing.T) {
	d := t.TempDir()
	r2Video(t, d)
	pl := r2Reframe(t, d, "o", "--aspect", "9:16", "--fit", "crop", "--focus", "0", "--name", "f0")
	pc := r2Reframe(t, d, "o", "--aspect", "9:16", "--fit", "crop", "--focus", "0.5", "--name", "f5")
	pr := r2Reframe(t, d, "o", "--aspect", "9:16", "--fit", "crop", "--focus", "1", "--name", "f1")
	w, h := r2Size(t, pl)
	sw := int(math.Round(float64(h) * r2SrcW / r2SrcH)) // sorgente scalata all'altezza dell'uscita
	region := func(x int) []byte {
		return r2Gray(t, filepath.Join(d, "in.mp4"), fmt.Sprintf("scale=%d:%d:flags=bicubic,crop=%d:%d:%d:0", sw, h, w, h, x))
	}
	xs := []int{0, (sw - w) / 2, sw - w}
	for i, c := range []struct{ name, p string }{{"focus 0", pl}, {"focus 0.5", pc}, {"focus 1", pr}} {
		got := r2Gray(t, c.p, "null")
		best, bestD := -1, 1e9
		for j, x := range xs {
			if df := r2Diff(got, region(x)); df < bestD {
				best, bestD = j, df
			}
		}
		if best != i {
			t.Errorf("%s: il ritaglio somiglia di più a x=%d che a x=%d", c.name, xs[best], xs[i])
		}
	}
	if r2Diff(r2Gray(t, pl, "null"), r2Gray(t, pr, "null")) < 10 {
		t.Error("focus 0 e 1 danno lo stesso ritaglio")
	}
}

func TestR2RifiutiCodice2(t *testing.T) {
	d := t.TempDir()
	r2Video(t, d)
	cases := [][]string{
		{"--aspect", "3:2"},
		{"--aspect", "abc"},
		{"--aspect", "9:16", "--fit", "stretch"},
		{"--aspect", "9:16", "--focus", "1.5"},
		{"--aspect", "9:16", "--focus", "-0.1"},
		{"--aspect", "9:16", "--focus", "abc"},
	}
	for _, c := range cases {
		so, code := r2Run(t, d, append([]string{"post", "reframe", "--video", "in.mp4", "--out", "o"}, c...)...)
		if code != 2 {
			t.Errorf("giv post reframe %v: codice %d, atteso 2 (%s)", c, code, so)
		}
	}
	if _, err := os.Stat(filepath.Join(d, "o")); err == nil {
		t.Error("i rifiuti hanno creato la cartella d'uscita")
	}
}

func TestR2DryRunNienteFile(t *testing.T) {
	d := t.TempDir()
	r2Video(t, d)
	so, code := r2Run(t, d, "post", "reframe", "--video", "in.mp4", "--out", "o", "--aspect", "9:16", "--fit", "blur", "--focus", "0.3", "--dry-run")
	if code != 0 || !json.Valid([]byte(so)) {
		t.Fatalf("dry-run: codice %d\n%s", code, so)
	}
	if _, err := os.Stat(filepath.Join(d, "o")); err == nil {
		t.Error("--dry-run ha creato file")
	}
}
