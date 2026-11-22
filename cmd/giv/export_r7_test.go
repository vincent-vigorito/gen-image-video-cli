package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Test R-T7 (CLI-949): giv export. Materiale sintetico (testsrc, sine), binario vero.

// r7Video crea in.mp4 16:9 320x180, 25 fps, di dur secondi, con audio sine di livello basso.
func r7Video(t *testing.T, dir string, dur int, name string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		fmt.Sprintf("testsrc=size=320x180:rate=25:duration=%d", dur),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%d", dur), "-af", "volume=0.1",
		"-shortest", "-pix_fmt", "yuv420p", "-y", filepath.Join(dir, name)).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
}

func r7Run(t *testing.T, dir string, args ...string) (string, int) {
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

// r7Export lancia giv export e restituisce i percorsi dei file in ordine.
func r7Export(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	full := append([]string{"export", "--video", "in.mp4", "--out", "o"}, args...)
	so, code := r7Run(t, dir, full...)
	if code != 0 {
		t.Fatalf("giv %v: codice %d\n%s", full, code, so)
	}
	var m struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(so), &m); err != nil {
		t.Fatalf("manifest: %v\n%s", err, so)
	}
	var ps []string
	for _, f := range m.Files {
		p := f.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		ps = append(ps, p)
	}
	return ps
}

func r7Probe(t *testing.T, path, sel, entries string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", sel, "-show_entries", "stream="+entries, "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	return strings.TrimSpace(string(out))
}

func r7Size(t *testing.T, p string) (int, int) {
	var w, h int
	if _, err := fmt.Sscanf(r7Probe(t, p, "v:0", "width,height"), "%d,%d", &w, &h); err != nil {
		t.Fatal(err)
	}
	return w, h
}

func TestR7UnFilePerCanale(t *testing.T) {
	d := t.TempDir()
	r7Video(t, d, 4, "in.mp4")
	ps := r7Export(t, d, "--for", "instagram,tiktok,youtube,linkedin")
	want := []struct {
		name string
		w, h int
		fps  string
	}{
		{"in-instagram-9x16.mp4", 1080, 1920, "30/1"},
		{"in-tiktok-9x16.mp4", 1080, 1920, "30/1"},
		{"in-youtube-16x9.mp4", 1920, 1080, "25/1"},
		{"in-linkedin-16x9.mp4", 1920, 1080, "25/1"},
	}
	if len(ps) != len(want) {
		t.Fatalf("file: %v", ps)
	}
	for i, w := range want {
		p := ps[i]
		if filepath.Base(p) != w.name {
			t.Errorf("nome %q, atteso %q", filepath.Base(p), w.name)
		}
		if gw, gh := r7Size(t, p); gw != w.w || gh != w.h {
			t.Errorf("%s: %dx%d, attesa %dx%d", w.name, gw, gh, w.w, w.h)
		}
		if got := r7Probe(t, p, "v:0", "r_frame_rate"); got != w.fps {
			t.Errorf("%s: fps %s, atteso %s", w.name, got, w.fps)
		}
		if got := r7Probe(t, p, "v:0", "codec_name,pix_fmt"); got != "h264,yuv420p" {
			t.Errorf("%s: video %s", w.name, got)
		}
		if got := r7Probe(t, p, "a:0", "codec_name,sample_rate,channels"); got != "aac,48000,2" {
			t.Errorf("%s: audio %s", w.name, got)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		mo, md := bytes.Index(b, []byte("moov")), bytes.Index(b, []byte("mdat"))
		if mo < 0 || md < 0 || mo > md {
			t.Errorf("%s: moov non in testa (moov %d, mdat %d)", w.name, mo, md)
		}
	}
}

func TestR7FormatoSuRichiesta(t *testing.T) {
	d := t.TempDir()
	r7Video(t, d, 4, "in.mp4")
	ps := r7Export(t, d, "--for", "instagram:4:5,youtube:9:16")
	if len(ps) != 2 {
		t.Fatalf("file: %v", ps)
	}
	for i, w := range []struct {
		name string
		w, h int
	}{{"in-instagram-4x5.mp4", 1080, 1350}, {"in-youtube-9x16.mp4", 1080, 1920}} {
		if filepath.Base(ps[i]) != w.name {
			t.Errorf("nome %q, atteso %q", filepath.Base(ps[i]), w.name)
		}
		if gw, gh := r7Size(t, ps[i]); gw != w.w || gh != w.h {
			t.Errorf("%s: %dx%d", w.name, gw, gh)
		}
	}
}

func TestR7RifiutiCodice2(t *testing.T) {
	d := t.TempDir()
	r7Video(t, d, 4, "in.mp4")
	r7Video(t, d, 2, "corto.mp4")
	// video lungo 200 s, piccolo e leggero
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=64x36:rate=1:duration=200",
		"-pix_fmt", "yuv420p", "-y", filepath.Join(d, "lungo.mp4")).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
	cases := [][]string{
		{"--video", "in.mp4", "--for", "facebook"},
		{"--video", "in.mp4", "--for", ""},
		{"--video", "in.mp4", "--for", "instagram:3:2"},
		{"--video", "in.mp4", "--for", "instagram,instagram"},
		{"--video", "corto.mp4", "--for", "tiktok,instagram"}, // 2 s: sotto il minimo di Instagram, nessun file nemmeno per TikTok
		{"--video", "lungo.mp4", "--for", "youtube:9:16"},    // Shorts: max 180 s
	}
	for _, c := range cases {
		so, code := r7Run(t, d, append([]string{"export", "--out", "o"}, c...)...)
		if code != 2 {
			t.Errorf("giv export %v: codice %d, atteso 2 (%s)", c, code, so)
		}
	}
	if _, err := os.Stat(filepath.Join(d, "o")); err == nil {
		t.Error("i rifiuti hanno creato la cartella d'uscita")
	}
}

func TestR7DryRunNienteFile(t *testing.T) {
	d := t.TempDir()
	r7Video(t, d, 4, "in.mp4")
	so, code := r7Run(t, d, "export", "--video", "in.mp4", "--out", "o", "--for", "instagram,youtube", "--dry-run")
	if code != 0 || !json.Valid([]byte(so)) {
		t.Fatalf("dry-run: codice %d\n%s", code, so)
	}
	if _, err := os.Stat(filepath.Join(d, "o")); err == nil {
		t.Error("--dry-run ha creato file o cartella")
	}
}

// r7Gray: fotogramma a t=1 s, filtrato con vf, come grigio.
func r7Gray(t *testing.T, path, vf string) []byte {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-ss", "1", "-i", path, "-frames:v", "1",
		"-vf", vf, "-f", "rawvideo", "-pix_fmt", "gray", "-").Output()
	if err != nil {
		t.Fatalf("ffmpeg gray: %v", err)
	}
	return out
}

func r7Diff(a, b []byte) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 255
	}
	var s float64
	for i := range a {
		s += math.Abs(float64(a[i]) - float64(b[i]))
	}
	return s / float64(len(a))
}

func TestR7FocusSpostaRitaglio(t *testing.T) {
	d := t.TempDir()
	r7Video(t, d, 4, "in.mp4")
	var got [3][]byte
	for i, f := range []string{"0", "0.5", "1"} {
		p := r7Export(t, d, "--for", "instagram", "--fit", "crop", "--focus", f, "--name", "f"+strconv.Itoa(i))
		got[i] = r7Gray(t, p[0], "scale=108:192:flags=area")
	}
	// sorgente scalata a 192 di altezza → 341 di larghezza; finestra 108 a x = 0, centro, fine
	const sw, w, h = 341, 108, 192
	xs := []int{0, (sw - w) / 2, sw - w}
	for i := range got {
		best, bd := -1, 1e9
		for j, x := range xs {
			ref := r7Gray(t, filepath.Join(d, "in.mp4"), fmt.Sprintf("scale=%d:%d:flags=area,crop=%d:%d:%d:0", sw, h, w, h, x))
			if df := r7Diff(got[i], ref); df < bd {
				best, bd = j, df
			}
		}
		if best != i {
			t.Errorf("focus #%d: il ritaglio somiglia di più alla finestra x=%d che a x=%d", i, xs[best], xs[i])
		}
	}
}

var r7LUFS = regexp.MustCompile(`I:\s+(-?[0-9.]+) LUFS`)

func r7Loudness(t *testing.T, p string) float64 {
	t.Helper()
	out, _ := exec.Command("ffmpeg", "-hide_banner", "-nostats", "-i", p, "-af", "ebur128", "-f", "null", "-").CombinedOutput()
	ms := r7LUFS.FindAllStringSubmatch(string(out), -1)
	if len(ms) == 0 {
		t.Fatalf("ebur128 senza risultato: %s", out)
	}
	v, _ := strconv.ParseFloat(ms[len(ms)-1][1], 64)
	return v
}

func TestR7VolumeFinale(t *testing.T) {
	d := t.TempDir()
	r7Video(t, d, 6, "in.mp4")
	for _, c := range []struct {
		args []string
		want float64
	}{{nil, -14}, {[]string{"--loudness", "-20"}, -20}} {
		p := r7Export(t, d, append([]string{"--for", "youtube", "--name", "v" + strconv.Itoa(int(-c.want))}, c.args...)...)
		if got := r7Loudness(t, p[0]); math.Abs(got-c.want) > 1 {
			t.Errorf("%v: volume %.1f LUFS, atteso %.0f ±1", c.args, got, c.want)
		}
	}
}
