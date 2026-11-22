package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Test R-T4 (CLI-945): giv post mix. Materiale sintetico, binario vero.
// Musica = sine 220 Hz, voce = sine 1000 Hz (separabili con un passa-banda).

func r4ff(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
}

// r4Setup crea in dir: in.mp4 (6 s con audio 330 Hz), mute.mp4 (6 s senza audio),
// music.wav (10 s, 220 Hz), voice.wav (2 s, 1000 Hz), testo.txt (non audio).
func r4Setup(t *testing.T) string {
	d := t.TempDir()
	r4ff(t, "-f", "lavfi", "-i", "testsrc=size=160x120:rate=25:duration=6", "-f", "lavfi", "-i", "sine=frequency=330:duration=6",
		"-shortest", "-pix_fmt", "yuv420p", filepath.Join(d, "in.mp4"))
	r4ff(t, "-f", "lavfi", "-i", "testsrc=size=160x120:rate=25:duration=6", "-pix_fmt", "yuv420p", filepath.Join(d, "mute.mp4"))
	r4ff(t, "-f", "lavfi", "-i", "sine=frequency=220:duration=10", filepath.Join(d, "music.wav"))
	r4ff(t, "-f", "lavfi", "-i", "sine=frequency=1000:duration=2", filepath.Join(d, "voice.wav"))
	if err := os.WriteFile(filepath.Join(d, "testo.txt"), []byte("non sono audio\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func r4Run(t *testing.T, dir string, args ...string) (string, int) {
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

// r4Mix lancia giv post mix sul video e restituisce il percorso dell'uscita.
func r4Mix(t *testing.T, d, video string, args ...string) string {
	t.Helper()
	so, code := r4Run(t, d, append([]string{"post", "mix", "--video", video, "--out", "o"}, args...)...)
	if code != 0 {
		t.Fatalf("mix %v: codice %d\n%s", args, code, so)
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
		p = filepath.Join(d, p)
	}
	return p
}

func r4Probe(t *testing.T, path string, entries ...string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", append([]string{"-v", "error"}, append(entries, path)...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func r4AudioStreams(t *testing.T, p string) int {
	s := r4Probe(t, p, "-select_streams", "a", "-show_entries", "stream=index", "-of", "csv=p=0")
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func r4Dur(t *testing.T, p string, sel string) float64 {
	f, err := strconv.ParseFloat(r4Probe(t, p, "-select_streams", sel, "-show_entries", "stream=duration", "-of", "csv=p=0"), 64)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

var r4MeanRe = regexp.MustCompile(`mean_volume: (-?[0-9.]+|-inf) dB`)

// r4Band: livello medio (dB) della banda attorno a freq nella finestra [from, from+dur).
func r4Band(t *testing.T, p string, freq int, from, dur float64) float64 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-ss", ff(from), "-t", ff(dur), "-i", p, "-vn",
		"-af", "bandpass=f="+strconv.Itoa(freq)+":width_type=h:w=60,volumedetect", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("misura: %v\n%s", err, out)
	}
	m := r4MeanRe.FindSubmatch(out)
	if m == nil {
		t.Fatalf("mean_volume assente:\n%s", out)
	}
	if string(m[1]) == "-inf" {
		return -120
	}
	v, _ := strconv.ParseFloat(string(m[1]), 64)
	return v
}

func ff(f float64) string { return strconv.FormatFloat(f, 'f', 3, 64) }

func TestR4MusicaSuVideoConAudio(t *testing.T) {
	d := r4Setup(t)
	o := r4Mix(t, d, "in.mp4", "--music", "music.wav")
	if n := r4AudioStreams(t, o); n != 1 {
		t.Fatalf("tracce audio %d, attesa 1", n)
	}
	if v, a := r4Dur(t, o, "v:0"), r4Dur(t, o, "a:0"); math.Abs(v-6) > 0.15 || math.Abs(a-6) > 0.15 {
		t.Fatalf("durata video %.3f audio %.3f, attesa 6 (musica lunga 10)", v, a)
	}
	// le due sorgenti (330 originale, 220 musica) sono nell'uscita
	if l := r4Band(t, o, 220, 1, 3); l < -40 {
		t.Errorf("musica assente: %.1f dB", l)
	}
	if l := r4Band(t, o, 330, 1, 3); l < -40 {
		t.Errorf("audio originale perso: %.1f dB", l)
	}
}

func TestR4MusicaSuVideoSenzaAudio(t *testing.T) {
	d := r4Setup(t)
	o := r4Mix(t, d, "mute.mp4", "--music", "music.wav")
	if n := r4AudioStreams(t, o); n != 1 {
		t.Fatalf("tracce audio %d, attesa 1", n)
	}
	if v, a := r4Dur(t, o, "v:0"), r4Dur(t, o, "a:0"); math.Abs(v-6) > 0.15 || math.Abs(a-6) > 0.15 {
		t.Fatalf("durata video %.3f audio %.3f, attesa 6", v, a)
	}
	if l := r4Band(t, o, 220, 1, 3); l < -40 {
		t.Errorf("musica assente: %.1f dB", l)
	}
}

func TestR4VoceAlTempoEDuck(t *testing.T) {
	d := r4Setup(t)
	// voce di 2 s (1000 Hz) da 2.0 s; musica 220 Hz su video senza audio
	o := r4Mix(t, d, "mute.mp4", "--music", "music.wav", "--voice", "voice.wav", "--voice-at", "2", "--duck")
	if n := r4AudioStreams(t, o); n != 1 {
		t.Fatalf("tracce audio %d, attesa 1", n)
	}
	// la voce parte a 2 s: prima niente, dopo presente
	before, during := r4Band(t, o, 1000, 0.2, 1.4), r4Band(t, o, 1000, 2.4, 1.2)
	if before > -50 || during < -40 {
		t.Errorf("voce non al tempo: prima %.1f dB, durante %.1f dB", before, during)
	}
	// la musica è più bassa mentre la voce parla (finestre equivalenti, stessa durata)
	free, ducked := r4Band(t, o, 220, 0.3, 1.2), r4Band(t, o, 220, 2.5, 1.2)
	if free-ducked < 3 {
		t.Errorf("duck assente: musica %.1f dB fuori, %.1f dB sotto la voce (differenza < 3 dB)", free, ducked)
	}
	// senza --duck la musica resta uguale sotto la voce
	n := r4Mix(t, d, "mute.mp4", "--music", "music.wav", "--voice", "voice.wav", "--voice-at", "2", "--name", "senzaduck")
	if diff := r4Band(t, n, 220, 0.3, 1.2) - r4Band(t, n, 220, 2.5, 1.2); math.Abs(diff) > 1.5 {
		t.Errorf("senza --duck la musica cambia di %.1f dB", diff)
	}
}

var r4IRe = regexp.MustCompile(`(?s)Integrated loudness:\s*I:\s*(-?[0-9.]+) LUFS`)

func TestR4Loudness(t *testing.T) {
	d := r4Setup(t)
	o := r4Mix(t, d, "in.mp4", "--music", "music.wav", "--loudness", "-14")
	out, err := exec.Command("ffmpeg", "-hide_banner", "-nostdin", "-i", o, "-vn", "-af", "ebur128", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ebur128: %v\n%s", err, out)
	}
	m := r4IRe.FindSubmatch(out)
	if m == nil {
		t.Fatalf("I: assente\n%s", out)
	}
	i, _ := strconv.ParseFloat(string(m[1]), 64)
	if math.Abs(i-(-14)) > 1 {
		t.Fatalf("livello integrato %.1f LUFS, atteso -14 ±1", i)
	}
}

func TestR4RifiutiCodice2(t *testing.T) {
	d := r4Setup(t)
	cases := [][]string{
		{"--music", "assente.wav"},
		{"--music", "testo.txt"},
		{"--voice", "assente.wav", "--voice-at", "1"},
		{"--voice", "testo.txt", "--voice-at", "1"},
		{"--music", "music.wav", "--music-volume", "-1"},
		{"--music", "music.wav", "--music-volume", "100"},
		{"--music", "music.wav", "--loudness", "5"},
		{"--music", "music.wav", "--loudness", "-100"},
		{"--voice", "voice.wav", "--voice-at", "99"},
	}
	for _, c := range cases {
		_, code := r4Run(t, d, append([]string{"post", "mix", "--video", "in.mp4", "--out", "o"}, c...)...)
		if code != 2 {
			t.Errorf("giv post mix %v: codice %d, atteso 2", c, code)
		}
	}
	if _, err := os.Stat(filepath.Join(d, "o")); err == nil {
		t.Error("i rifiuti hanno creato la cartella d'uscita")
	}
}

func TestR4DryRunNienteFile(t *testing.T) {
	d := r4Setup(t)
	so, code := r4Run(t, d, "post", "mix", "--video", "in.mp4", "--out", "o", "--music", "music.wav", "--voice", "voice.wav", "--voice-at", "1", "--duck", "--loudness", "-14", "--dry-run")
	if code != 0 || !json.Valid([]byte(so)) {
		t.Fatalf("dry-run: codice %d\n%s", code, so)
	}
	if _, err := os.Stat(filepath.Join(d, "o")); err == nil {
		t.Error("--dry-run ha creato file")
	}
}
