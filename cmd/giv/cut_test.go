package main

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// F2-T4b: giv story cut e avviso della voce in story check. Clip sintetiche
// con le sorgenti interne di ffmpeg; nessuna rete.

func cutNeedFF(t *testing.T) {
	t.Helper()
	for _, b := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skip(b + " non disponibile")
		}
	}
}

func cutFF(t *testing.T, args ...string) {
	t.Helper()
	a := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)
	if o, err := exec.Command("ffmpeg", a...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v\n%s", err, o)
	}
}

func cutClip(t *testing.T, path, size string, fps int, dur string) {
	cutFF(t, "-f", "lavfi", "-i", "testsrc2=size="+size+":rate="+strconv.Itoa(fps)+":duration="+dur,
		"-f", "lavfi", "-i", "sine=frequency=300:duration="+dur,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", path)
}

func cutRun(t *testing.T, sub, path string) (out, errOut string, code int) {
	t.Helper()
	grab := func(f **os.File) func() string {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := readAllFile(r); done <- string(b) }()
		return func() string { w.Close(); *f = old; return <-done }
	}
	so, se := grab(&os.Stdout), grab(&os.Stderr)
	err := cmdStory([]string{sub, path})
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

const cutBoard = `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},
"montaggio":{"transizione":"dissolvenza"},
"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":2},{"id":"s2","tipo":"clip","file":"b.mp4","durata":2},{"id":"s3","tipo":"clip","file":"c.mp4","durata":2}]}`

func cutDir(t *testing.T) string {
	dir := t.TempDir()
	cutClip(t, filepath.Join(dir, "a.mp4"), "160x90", 24, "2.5")
	cutClip(t, filepath.Join(dir, "b.mp4"), "120x120", 30, "2.5")
	cutClip(t, filepath.Join(dir, "c.mp4"), "90x160", 15, "2.5")
	if err := os.WriteFile(filepath.Join(dir, "sb.json"), []byte(cutBoard), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStoryCutMontaggioRipetibile(t *testing.T) {
	cutNeedFF(t)
	n := capsServer(t) // qualunque richiesta di rete verso OpenRouter sarebbe contata
	dir := cutDir(t)
	sbp := filepath.Join(dir, "sb.json")
	var first string
	for i := 0; i < 2; i++ {
		out, se, code := cutRun(t, "cut", sbp)
		if code != 0 {
			t.Fatalf("giro %d code=%d\n%s", i, code, se)
		}
		var r struct {
			Output     string  `json:"output"`
			DurationS  float64 `json:"duration_s"`
			ExpectedS  float64 `json:"expected_duration_s"`
			Width      int     `json:"width"`
			Height     int     `json:"height"`
			VideoCodec string  `json:"video_codec"`
			AudioCodec string  `json:"audio_codec"`
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		// 3 x 2 s con 2 dissolvenze da 0,5 s
		if math.Abs(r.DurationS-5) > 0.1 || math.Abs(r.ExpectedS-5) > 0.1 {
			t.Errorf("durata %v/%v (attesa 5)", r.DurationS, r.ExpectedS)
		}
		if r.Height != 144 || r.Width != 256 || r.VideoCodec != "h264" || r.AudioCodec != "aac" {
			t.Errorf("formato %+v", r)
		}
		probe, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type", "-of", "csv=p=0", r.Output).Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Fields(string(probe)); len(got) != 2 {
			t.Errorf("tracce %v (attese 1 video + 1 audio)", got)
		}
		if i == 0 {
			first = out
		} else if first != out {
			t.Errorf("secondo giro diverso:\n%s\n%s", first, out)
		}
	}
	if *n != 0 {
		t.Errorf("chiamate di rete: %d", *n)
	}
}

func TestStoryCutRifiuti(t *testing.T) {
	dir := t.TempDir()
	sbp := filepath.Join(dir, "sb.json")
	if err := os.WriteFile(sbp, []byte(cutBoard), 0o644); err != nil {
		t.Fatal(err)
	}
	// clip mancanti: codice 2 e il nome del file
	if _, _, code := cutRun(t, "cut", sbp); code == 0 {
		t.Errorf("clip mancanti accettate")
	} else if code != 2 {
		t.Errorf("clip mancanti: code %d (atteso 2)", code)
	}
	// ffmpeg assente: codice 1 con messaggio chiaro
	for _, f := range []string{"a.mp4", "b.mp4", "c.mp4"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	t.Setenv("PATH", t.TempDir())
	_, se, code := cutRun(t, "cut", sbp)
	if code != 1 || !strings.Contains(se, "ffmpeg") {
		t.Errorf("ffmpeg assente: code %d msg %q", code, se)
	}
}
