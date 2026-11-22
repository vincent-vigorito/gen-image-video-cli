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

// Test R-T6 (CLI-951), passo 2: logo, titoli, cartello, musica, volume,
// foglio di contatto, --export. Materiale sintetico, nessuna rete.

type r6Out struct {
	Output    string  `json:"output"`
	DurationS float64 `json:"duration_s"`
	Formats   []struct {
		Output    string  `json:"output"`
		Format    string  `json:"format"`
		DurationS float64 `json:"duration_s"`
	} `json:"formats"`
	Exports []exportItem `json:"exports"`
}

func r6CutOK(t *testing.T, args ...string) (r6Out, string) {
	t.Helper()
	out, se, code := r6Cmd(t, append([]string{"cut"}, args...)...)
	if code != 0 {
		t.Fatalf("cut %v: codice %d\n%s", args, code, se)
	}
	var r r6Out
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return r, se
}

// file del montato: l'uscita singola o l'unico formato.
func (r r6Out) file() string {
	if r.Output != "" {
		return r.Output
	}
	return r.Formats[0].Output
}

func r6Dur(t *testing.T, f string) float64 {
	o, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", f).Output()
	if err != nil {
		t.Fatal(err)
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(string(o)), 64)
	return v
}

// r6Solid sostituisce a.mp4 e b.mp4 con clip blu uniformi (con audio).
func r6Solid(t *testing.T, d string) {
	for _, n := range []string{"a.mp4", "b.mp4"} {
		cutFF(t, "-f", "lavfi", "-i", "color=c=blue:s=160x90:r=25:d=3", "-f", "lavfi", "-i", "sine=frequency=300:duration=3",
			"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", filepath.Join(d, n))
	}
}

func TestR6TitoloSpecialeELogo(t *testing.T) {
	d := r6Dir(t)
	r6Solid(t, d)
	p := r6Board(t, d, `"formati":["16:9"],"logo":{"file":"logo.png","posizione":"alto-destra","larghezza":0.2},"titoli":[{"testo":"a:b,c\\d{e}f%g'h\nj;{\\an1}k","da":0,"durata":2}]`, "")
	r, _ := r6CutOK(t, p)
	// un fotogramma a 1 s: il quarto in alto a destra deve contenere rosso (logo)
	cmd := exec.Command("ffmpeg", "-v", "error", "-ss", "1", "-i", r.file(), "-frames:v", "1",
		"-vf", "crop=iw/2:ih/2:iw/2:0", "-f", "rawvideo", "-pix_fmt", "rgb24", "-")
	var b bytes.Buffer
	cmd.Stdout = &b
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	px, red := b.Bytes(), 0
	for i := 0; i+2 < len(px); i += 3 {
		if px[i] > 180 && px[i+1] < 80 && px[i+2] < 80 {
			red++
		}
	}
	if red < 20 {
		t.Errorf("logo non visibile in alto a destra (%d pixel rossi)", red)
	}
}

func TestR6CartelloAllungaIlMontaggio(t *testing.T) {
	d := r6Dir(t)
	p := r6Board(t, d, `"formati":["16:9"]`, "")
	base, _ := r6CutOK(t, p)
	baseDur := r6Dur(t, base.file()) // il file si riscrive a ogni montaggio
	p = r6Board(t, d, `"formati":["16:9"],"cartello_finale":{"durata":2,"testo":"Fine","sfondo":"#112233"}`, "")
	with, _ := r6CutOK(t, p)
	if diff := r6Dur(t, with.file()) - baseDur; math.Abs(diff-2) > 0.3 {
		t.Errorf("il cartello da 2 s allunga di %.2f s", diff)
	}
}

// ebur128: loudness integrata del file.
func r6LUFS(t *testing.T, f string) float64 {
	o, _ := exec.Command("ffmpeg", "-nostats", "-i", f, "-af", "ebur128", "-f", "null", "-").CombinedOutput()
	m := regexp.MustCompile(`I:\s+(-?[0-9.]+) LUFS`).FindAllStringSubmatch(string(o), -1)
	if len(m) == 0 {
		t.Fatalf("nessuna misura ebur128:\n%s", o)
	}
	v, _ := strconv.ParseFloat(m[len(m)-1][1], 64)
	return v
}

func TestR6VolumeFinaleEntro1LU(t *testing.T) {
	d := r6Dir(t)
	p := r6Board(t, d, `"formati":["16:9"],"volume":-16`, "")
	r, _ := r6CutOK(t, p)
	if got := r6LUFS(t, r.file()); math.Abs(got+16) > 1 {
		t.Errorf("volume finale %.2f LUFS, chiesto -16 (±1)", got)
	}
}

// meanVol: mean_volume (dB) della banda attorno a freq nel tratto [from, from+len).
func r6Band(t *testing.T, f string, from, length float64, freq int) float64 {
	o, _ := exec.Command("ffmpeg", "-nostats", "-ss", strconv.FormatFloat(from, 'f', 2, 64), "-t", strconv.FormatFloat(length, 'f', 2, 64),
		"-i", f, "-af", "bandpass=f="+strconv.Itoa(freq)+":width_type=h:w=60,volumedetect", "-f", "null", "-").CombinedOutput()
	m := regexp.MustCompile(`mean_volume: (-?[0-9.]+) dB`).FindStringSubmatch(string(o))
	if m == nil {
		t.Fatalf("volumedetect:\n%s", o)
	}
	v, _ := strconv.ParseFloat(m[1], 64)
	return v
}

func TestR6MusicaSottoLaVoce(t *testing.T) {
	d := r6Dir(t)
	// clip mute con sola traccia video non serve: voce 1000 Hz sulla prima scena, musica 300 Hz sempre
	cutFF(t, "-f", "lavfi", "-i", "sine=frequency=1000:duration=2", filepath.Join(d, "voce.wav"))
	cutFF(t, "-f", "lavfi", "-i", "sine=frequency=300:duration=5", filepath.Join(d, "musica.wav"))
	scenes := `"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":2,"voce":"voce.wav"},{"id":"s2","tipo":"clip","file":"b.mp4","durata":2}]`
	// le clip originali hanno audio a 300 Hz: le sostituisco con clip senza audio per isolare la musica
	for _, n := range []string{"a.mp4", "b.mp4"} {
		cutFF(t, "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25:duration=3", "-c:v", "libx264", "-pix_fmt", "yuv420p", filepath.Join(d, n))
	}
	level := func(sotto bool) (voce, dopo float64) {
		p := r6Board(t, d, `"formati":["16:9"],"musica":{"file":"musica.wav","sotto_voce":`+strconv.FormatBool(sotto)+`}`, scenes)
		r, _ := r6CutOK(t, p)
		return r6Band(t, r.file(), 0.3, 1.4, 300), r6Band(t, r.file(), 2.3, 1.4, 300)
	}
	v, a := level(true)
	if !(v < a-3) {
		t.Errorf("musica sotto voce %.1f dB, fuori %.1f dB: attesa almeno 3 dB più bassa", v, a)
	}
}

func TestR6FoglioContatto(t *testing.T) {
	d := r6Dir(t)
	p := r6Board(t, d, `"formati":["16:9"]`, "")
	r6CutOK(t, p)
	if st, err := os.Stat(filepath.Join(d, "foglio-contatto.jpg")); err != nil || st.Size() == 0 {
		t.Fatalf("foglio-contatto.jpg non scritto: %v", err)
	}
	os.Remove(filepath.Join(d, "foglio-contatto.jpg"))
	b, _ := os.ReadFile(p)
	b = bytes.Replace(b, []byte(`"scene"`), []byte(`"foglio_contatto":false,"scene"`), 1)
	os.WriteFile(p, b, 0o644)
	r6CutOK(t, p)
	if _, err := os.Stat(filepath.Join(d, "foglio-contatto.jpg")); err == nil {
		t.Error("foglio_contatto:false ma il file è stato scritto")
	}
}

func TestR6Export(t *testing.T) {
	d := r6Dir(t)
	p := r6Board(t, d, `"formati":["9:16"]`, `"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":3},{"id":"s2","tipo":"clip","file":"b.mp4","durata":2}]`)
	r, se := r6CutOK(t, "--export", "tiktok", p)
	if len(r.Exports) != 1 || r.Exports[0].Channel != "tiktok" {
		t.Fatalf("exports: %+v\n%s", r.Exports, se)
	}
	if _, err := os.Stat(r.Exports[0].File); err != nil {
		t.Errorf("file dell'export mancante: %v", err)
	}
	// formato del canale non montato: codice 2 prima del montaggio
	if _, _, code := r6Cmd(t, "cut", "--export", "youtube", p); code != 2 {
		t.Errorf("export con formato non montato: codice %d, atteso 2", code)
	}
}
