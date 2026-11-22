package main

import (
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"
)

// Test V-T5 (CLI-973): picco reale non sopra -1 dBTP (tolleranza 0,1), volume
// entro 1 LU, ritardo lungo senza buffer proporzionale. Materiale sintetico.

var (
	v5TPRe = regexp.MustCompile(`(?s)True peak:\s*Peak:\s*(-?[0-9.]+) dBFS`)
	v5IRe  = regexp.MustCompile(`(?s)Integrated loudness:\s*I:\s*(-?[0-9.]+) LUFS`)
)

// v5Measure: picco vero (dBTP) e livello integrato (LUFS) dell'audio del file.
func v5Measure(t *testing.T, p string) (tp, lufs float64) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-i", p, "-vn", "-af", "ebur128=peak=true", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ebur128: %v\n%s", err, out)
	}
	a, b := v5TPRe.FindSubmatch(out), v5IRe.FindSubmatch(out)
	if a == nil || b == nil {
		t.Fatalf("misure assenti:\n%s", out)
	}
	tp, _ = strconv.ParseFloat(string(a[1]), 64)
	lufs, _ = strconv.ParseFloat(string(b[1]), 64)
	return
}

func v5Check(t *testing.T, what, p string, target float64) {
	t.Helper()
	tp, l := v5Measure(t, p)
	if tp > -0.9 {
		t.Errorf("%s: picco %.2f dBTP, atteso non sopra -1 (tol. 0,1)", what, tp)
	}
	if math.Abs(l-target) > 1 {
		t.Errorf("%s: %.2f LUFS, atteso %.0f ±1", what, l, target)
	}
}

func TestV5PiccoMix(t *testing.T) {
	d := r4Setup(t)
	// musica molto forte (picchi) sopra una voce: serve il limitatore vero
	r4ff(t, "-f", "lavfi", "-i", "sine=frequency=220:duration=10", "-af", "volume=3,alimiter=limit=1:level=0", filepath.Join(d, "forte.wav"))
	o := r4Mix(t, d, "in.mp4", "--music", "forte.wav", "--voice", "voice.wav", "--voice-at", "1", "--loudness", "-14")
	v5Check(t, "post mix", o, -14)
}

func TestV5PiccoExport(t *testing.T) {
	d := t.TempDir()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=5",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=5", "-af", "volume=0.95", "-shortest", "-pix_fmt", "yuv420p", "-y", filepath.Join(d, "in.mp4")).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
	for _, p := range r7Export(t, d, "--for", "instagram,youtube") {
		v5Check(t, "export "+filepath.Base(p), p, -14)
	}
}

func TestV5PiccoStoryCut(t *testing.T) {
	d := r6Dir(t)
	p := r6Board(t, d, `"formati":["16:9"],"volume":-14`, "")
	r, _ := r6CutOK(t, p)
	v5Check(t, "story cut", r.file(), -14)
}

// Voce a 600 s: parte al tempo giusto e la memoria del comando non cresce col
// ritardo (confronto con la stessa voce a 0 s: un buffer di 600 s in float
// stereo a 48 kHz sarebbe ~230 MB in piu').
func TestV5RitardoLungo(t *testing.T) {
	if testing.Short() {
		t.Skip("lento (~75 s)")
	}
	d := r4Setup(t)
	r4ff(t, "-f", "lavfi", "-i", "color=c=gray:size=64x64:rate=2:duration=610", "-f", "lavfi", "-i", "sine=frequency=330:duration=610",
		"-af", "volume=0.1", "-c:v", "mpeg4", "-shortest", "-pix_fmt", "yuv420p", filepath.Join(d, "lungo.mp4"))
	mix := func(at, out string) int64 {
		cmd := exec.Command(binPath, "post", "mix", "--video", "lungo.mp4", "--voice", "voice.wav", "--voice-at", at, "--out", out)
		cmd.Dir = d
		cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + t.TempDir()}
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("mix at=%s: %v\n%s", at, err, b)
		}
		return cmd.ProcessState.SysUsage().(*syscall.Rusage).Maxrss / 1024 // MB, compresi i figli
	}
	base := mix("0", "o0")
	long := mix("600", "o")
	if long-base > 60 {
		t.Errorf("memoria: %d MB con ritardo 600 s contro %d MB senza ritardo (> 60 MB in piu')", long, base)
	}
	matches, _ := filepath.Glob(filepath.Join(d, "o", "*.mp4"))
	if len(matches) != 1 {
		t.Fatalf("uscite: %v", matches)
	}
	before, during := r4Band(t, matches[0], 1000, 590, 8), r4Band(t, matches[0], 1000, 600.3, 1.4)
	if before > -42 || during < -30 {
		t.Errorf("voce non a 600 s: prima %.1f dB, durante %.1f dB", before, during)
	}
}
