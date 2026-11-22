package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Test V-T4 (CLI-967): giv qc. Materiale sintetico (testsrc, color, sine), binario vero.

type v4Check struct {
	Name      string `json:"name"`
	Result    string `json:"result"`
	Skipped   bool   `json:"skipped"`
	Intervals []struct {
		Start float64 `json:"start_s"`
		End   float64 `json:"end_s"`
	} `json:"intervals"`
}

type v4Out struct {
	Command  string    `json:"command"`
	Result   string    `json:"result"`
	CostUSD  float64   `json:"cost_usd"`
	Checks   []v4Check `json:"checks"`
	Channels []struct {
		Channel string    `json:"channel"`
		Result  string    `json:"result"`
		Checks  []v4Check `json:"checks"`
	} `json:"channels"`
}

func v4ff(t *testing.T, out string, args ...string) {
	t.Helper()
	full := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)
	full = append(full, out)
	if b, err := exec.Command("ffmpeg", full...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg: %v %s", err, b)
	}
}

// clip: sorgente video (v) e audio (a) lavfi, durata dur.
func v4clip(t *testing.T, dir, name, v, a string, dur float64) string {
	p := filepath.Join(dir, name)
	args := []string{"-f", "lavfi", "-i", fmt.Sprintf("%s:duration=%g", v, dur)}
	if a != "" {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("%s:duration=%g", a, dur), "-af", "volume=0.1", "-c:a", "aac", "-b:a", "128k", "-ar", "48000")
	}
	args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", "-shortest")
	v4ff(t, p, args...)
	return p
}

func v4qc(t *testing.T, dir string, args ...string) (v4Out, int, string) {
	t.Helper()
	cmd := exec.Command(binPath, append([]string{"qc"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	var o v4Out
	if code != 2 {
		if err := json.Unmarshal(so.Bytes(), &o); err != nil {
			t.Fatalf("json: %v\n%s\n%s", err, so.String(), se.String())
		}
	}
	return o, code, se.String()
}

func (o v4Out) check(t *testing.T, name string) v4Check {
	t.Helper()
	for _, c := range o.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("controllo %q assente: %+v", name, o.Checks)
	return v4Check{}
}

func v4sum(t *testing.T, p string) [32]byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func TestV4ClipBuonaSenzaDifetti(t *testing.T) {
	dir := t.TempDir()
	v4clip(t, dir, "ok.mp4", "testsrc=size=320x180:rate=25", "sine=frequency=440", 4)
	o, code, _ := v4qc(t, dir, "ok.mp4", "--expect-duration", "4", "--expect-size", "320x180", "--expect-fps", "25", "--expect-audio", "on", "--strict")
	if code != 0 {
		t.Fatalf("codice %d, atteso 0: %+v", code, o)
	}
	if o.Command != "qc" || o.CostUSD != 0 {
		t.Errorf("command/costo: %+v", o)
	}
	for _, c := range o.Checks {
		if c.Result == "difetto" || c.Result == "avviso" {
			t.Errorf("clip buona: %s = %s", c.Name, c.Result)
		}
	}
	if len(o.Checks) != 10 {
		t.Errorf("controlli: %d, attesi 10", len(o.Checks))
	}
}

func TestV4DifettiSuClipSintetiche(t *testing.T) {
	dir := t.TempDir()
	v4clip(t, dir, "nera.mp4", "color=c=black:size=320x180:rate=25", "sine=frequency=440", 4)
	v4clip(t, dir, "ferma.mp4", "color=c=red:size=320x180:rate=25", "sine=frequency=440", 5)
	v4clip(t, dir, "muta.mp4", "testsrc=size=320x180:rate=25", "anullsrc=r=48000:cl=stereo", 4)
	v4clip(t, dir, "corta.mp4", "testsrc=size=320x180:rate=25", "sine=frequency=440", 0.5)
	v4clip(t, dir, "senzaaudio.mp4", "testsrc=size=320x180:rate=25", "", 4)

	cases := []struct {
		file, check string
		args        []string
		want        string
	}{
		{"nera.mp4", "black", nil, "difetto"},
		{"ferma.mp4", "freeze", nil, "difetto"},
		{"muta.mp4", "silence", nil, "difetto"},
		{"corta.mp4", "duration", []string{"--expect-duration", "5"}, "difetto"},
		{"corta.mp4", "duration", nil, "avviso"},
		{"ok-misura", "", nil, ""},
		{"senzaaudio.mp4", "audio", []string{"--expect-audio", "on"}, "difetto"},
		{"senzaaudio.mp4", "audio", nil, "avviso"},
		{"muta.mp4", "audio", []string{"--expect-audio", "off"}, "difetto"},
	}
	for _, c := range cases {
		if c.check == "" {
			continue
		}
		t.Run(c.file+"/"+c.check+"/"+c.want, func(t *testing.T) {
			o, code, _ := v4qc(t, dir, append([]string{c.file}, c.args...)...)
			if code != 0 {
				t.Fatalf("codice %d", code)
			}
			if got := o.check(t, c.check); got.Result != c.want {
				t.Errorf("%s: %s, atteso %s", c.check, got.Result, c.want)
			}
			if o.Result != "difetto" && c.want == "difetto" {
				t.Errorf("risultato di livello %q", o.Result)
			}
		})
	}

	t.Run("misure e fps", func(t *testing.T) {
		o, _, _ := v4qc(t, dir, "senzaaudio.mp4", "--expect-size", "640x360", "--expect-fps", "30")
		if r := o.check(t, "size").Result; r != "difetto" {
			t.Errorf("size %s", r)
		}
		if r := o.check(t, "fps").Result; r != "difetto" {
			t.Errorf("fps %s", r)
		}
	})
	t.Run("intervalli neri", func(t *testing.T) {
		o, _, _ := v4qc(t, dir, "nera.mp4")
		if iv := o.check(t, "black").Intervals; len(iv) == 0 || iv[0].End <= iv[0].Start {
			t.Errorf("intervalli: %+v", iv)
		}
	})
}

func TestV4ProfiloCanale(t *testing.T) {
	dir := t.TempDir()
	// 16:9 320x180, 4 s: per instagram (9:16) altro rapporto = difetto; per youtube rapporto uguale, misura diversa = avviso.
	v4clip(t, dir, "orizz.mp4", "testsrc=size=320x180:rate=25", "sine=frequency=440", 4)
	// 2 s: sotto i 3 s minimi di instagram.
	v4clip(t, dir, "breve.mp4", "testsrc=size=320x180:rate=25", "sine=frequency=440", 2)
	// 1080x1920 h264/aac, 4 s: misura e durata giuste per instagram.
	v4clip(t, dir, "reel.mp4", "testsrc=size=1080x1920:rate=30", "sine=frequency=440", 4)

	ch := func(o v4Out, name string) (string, map[string]string) {
		for _, c := range o.Channels {
			if c.Channel == name {
				m := map[string]string{}
				for _, k := range c.Checks {
					m[k.Name] = k.Result
				}
				return c.Result, m
			}
		}
		t.Fatalf("canale %s assente: %+v", name, o.Channels)
		return "", nil
	}

	o, code, _ := v4qc(t, dir, "orizz.mp4", "--for", "instagram,youtube")
	if code != 0 {
		t.Fatalf("codice %d", code)
	}
	if _, m := ch(o, "instagram"); m["size"] != "difetto" {
		t.Errorf("instagram size: %v", m)
	}
	if _, m := ch(o, "youtube"); m["size"] != "avviso" {
		t.Errorf("youtube size (stesso rapporto): %v", m)
	}

	o, _, _ = v4qc(t, dir, "breve.mp4", "--for", "instagram")
	if _, m := ch(o, "instagram"); m["duration"] != "difetto" {
		t.Errorf("instagram durata 2 s: %v", m)
	}

	o, _, _ = v4qc(t, dir, "reel.mp4", "--for", "instagram")
	_, m := ch(o, "instagram")
	if m["size"] != "ok" || m["duration"] != "ok" || m["fps"] != "ok" || m["video_codec"] != "ok" || m["audio_codec"] != "ok" || m["faststart"] != "ok" {
		t.Errorf("reel a posto per instagram: %v", m)
	}
}

func TestV4StrictEAvvisaSenzaModificare(t *testing.T) {
	dir := t.TempDir()
	p := v4clip(t, dir, "nera.mp4", "color=c=black:size=320x180:rate=25", "sine=frequency=440", 4)
	before := v4sum(t, p)
	_, code, _ := v4qc(t, dir, "nera.mp4")
	if code != 0 {
		t.Errorf("senza --strict: codice %d, atteso 0", code)
	}
	o, code, _ := v4qc(t, dir, "nera.mp4", "--strict")
	if code != 1 || o.Result != "difetto" {
		t.Errorf("--strict: codice %d result %q, atteso 1/difetto", code, o.Result)
	}
	if v4sum(t, p) != before {
		t.Error("il file e' stato modificato")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("file in piu' nella cartella: %v", ents)
	}
}

func TestV4RifiutiCodice2(t *testing.T) {
	dir := t.TempDir()
	v4clip(t, dir, "ok.mp4", "testsrc=size=320x180:rate=25", "sine=frequency=440", 3)
	for name, args := range map[string][]string{
		"file mancante":   {"non-esiste.mp4"},
		"canale ignoto":   {"ok.mp4", "--for", "myspace"},
		"nessun file":     {},
		"durata negativa": {"ok.mp4", "--expect-duration", "-3"},
	} {
		t.Run(name, func(t *testing.T) {
			_, code, _ := v4qc(t, dir, args...)
			if code != 2 {
				t.Errorf("codice %d, atteso 2", code)
			}
		})
	}
}

func TestV4Storyboard(t *testing.T) {
	dir := t.TempDir()
	v4clip(t, dir, "s1.mp4", "testsrc=size=320x180:rate=25", "sine=frequency=440", 3)
	v4clip(t, dir, "s2.mp4", "color=c=black:size=320x180:rate=25", "sine=frequency=440", 3)
	sb := filepath.Join(dir, "spot.json")
	// Storyboard minimo; se il formato non e' accettato il test segnala il contratto da verificare.
	os.WriteFile(sb, []byte(`{"formato":{"aspect":"16:9"},"scene":[{"id":"s1","tipo":"clip","file":"s1.mp4"},{"id":"s2","tipo":"clip","file":"s2.mp4"}]}`), 0o644)
	cmd := exec.Command(binPath, "qc", "spot.json")
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	var se bytes.Buffer
	cmd.Stderr = &se
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("storyboard: %v %s", err, se.String())
	}
	var m struct {
		Mode   string `json:"mode"`
		Scenes []struct {
			ID     string `json:"id"`
			Result string `json:"result"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m.Mode != "storyboard" || len(m.Scenes) != 2 {
		t.Fatalf("%+v", m)
	}
	if m.Scenes[0].Result == "difetto" || m.Scenes[1].Result != "difetto" {
		t.Errorf("scene: %+v", m.Scenes)
	}
}
