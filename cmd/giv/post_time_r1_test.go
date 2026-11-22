package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Test R-T1 (CLI-939): giv post trim, speed, freeze. Materiale sintetico, binario vero.

func r1Video(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "in.mp4")
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=25:duration=5",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=5", "-shortest", "-pix_fmt", "yuv420p", "-y", p).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
	return p
}

// r1Run lancia giv in dir; restituisce stdout, codice.
func r1Run(t *testing.T, dir string, args ...string) (string, int) {
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

type r1Man struct {
	DurationS  float64 `json:"duration_s"`
	AudioCodec string  `json:"audio_codec"`
	Files      []struct {
		Path string `json:"path"`
	} `json:"files"`
}

// streamDur è la durata di un flusso (v o a) letta con ffprobe.
func streamDur(t *testing.T, path, sel string) float64 {
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", sel, "-show_entries", "stream=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("durata %s: %q", sel, out)
	}
	return f
}

func r1Ok(t *testing.T, dir string, want float64, args ...string) r1Man {
	t.Helper()
	so, code := r1Run(t, dir, append(args, "--video", "in.mp4", "--out", "o")...)
	if code != 0 {
		t.Fatalf("giv %v: codice %d\n%s", args, code, so)
	}
	var m r1Man
	if err := json.Unmarshal([]byte(so), &m); err != nil || len(m.Files) != 1 {
		t.Fatalf("manifest: %v\n%s", err, so)
	}
	p := filepath.Join(dir, m.Files[0].Path)
	if filepath.IsAbs(m.Files[0].Path) {
		p = m.Files[0].Path
	}
	v, a := streamDur(t, p, "v:0"), streamDur(t, p, "a:0")
	if math.Abs(v-want) > 0.15 || math.Abs(a-want) > 0.15 {
		t.Fatalf("giv %v: durata video %.3f audio %.3f, attesa %.3f", args, v, a, want)
	}
	return m
}

func TestR1TrimSpeedFreeze(t *testing.T) {
	d := t.TempDir()
	r1Video(t, d)
	m := r1Ok(t, d, 2, "post", "trim", "--start", "1", "--end", "3")
	if m.AudioCodec == "" {
		t.Fatal("trim senza audio")
	}
	r1Ok(t, d, 2.5, "post", "speed", "--factor", "2")
	r1Ok(t, d, 10, "post", "speed", "--factor", "0.5")
	r1Ok(t, d, 6.5, "post", "freeze", "--at", "2", "--duration", "1.5")
}

func TestR1RifiutiCodice2(t *testing.T) {
	d := t.TempDir()
	r1Video(t, d)
	cases := [][]string{
		{"trim", "--start", "3", "--end", "3"},
		{"trim", "--start", "4", "--end", "2"},
		{"trim", "--start", "1", "--end", "9"},
		{"trim", "--start", "7", "--end", "8"},
		{"speed", "--factor", "0.2"},
		{"speed", "--factor", "5"},
		{"freeze", "--at", "2", "--duration", "0"},
		{"freeze", "--at", "2", "--duration", "-1"},
		{"freeze", "--at", "9", "--duration", "1"},
	}
	for _, c := range cases {
		_, code := r1Run(t, d, append(append([]string{"post"}, c...), "--video", "in.mp4", "--out", "o")...)
		if code != 2 {
			t.Errorf("giv post %v: codice %d, atteso 2", c, code)
		}
	}
	if _, err := os.Stat(filepath.Join(d, "o")); err == nil {
		t.Error("i rifiuti hanno creato la cartella d'uscita")
	}
}

func TestR1DryRunNienteFile(t *testing.T) {
	d := t.TempDir()
	r1Video(t, d)
	for _, c := range [][]string{{"trim", "--start", "1", "--end", "3"}, {"speed", "--factor", "2"}, {"freeze", "--at", "2", "--duration", "1.5"}} {
		so, code := r1Run(t, d, append(append([]string{"post"}, c...), "--video", "in.mp4", "--out", "o", "--dry-run")...)
		if code != 0 || !json.Valid([]byte(so)) {
			t.Fatalf("%v dry-run: codice %d\n%s", c, code, so)
		}
	}
	if _, err := os.Stat(filepath.Join(d, "o")); err == nil {
		t.Error("--dry-run ha creato file")
	}
}
