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

// Test R-T5 (CLI-947): giv sheet, giv frame, giv gif. Materiale sintetico, binario vero.

func r5Clip(t *testing.T, path, size string, dur int) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size="+size+":rate=25:duration="+strconv.Itoa(dur),
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+strconv.Itoa(dur), "-shortest", "-pix_fmt", "yuv420p", "-y", "--", path).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
}

func r5Dir(t *testing.T) string {
	d := t.TempDir()
	r5Clip(t, filepath.Join(d, "in.mp4"), "160x120", 5)
	return d
}

// r5Run lancia giv in dir; restituisce stdout, stderr, codice.
func r5Run(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), se.String(), code
}

// r5Ok lancia giv e restituisce il percorso assoluto del file unico del manifest.
func r5Ok(t *testing.T, dir string, args ...string) string {
	t.Helper()
	so, se, code := r5Run(t, dir, args...)
	if code != 0 {
		t.Fatalf("giv %v: codice %d\n%s\n%s", args, code, so, se)
	}
	var m struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(so), &m); err != nil || len(m.Files) != 1 {
		t.Fatalf("giv %v: manifest: %v\n%s", args, err, so)
	}
	p := m.Files[0].Path
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	if st, err := os.Stat(p); err != nil || st.Size() == 0 {
		t.Fatalf("giv %v: file %s mancante o vuoto", args, p)
	}
	return p
}

type r5Probe struct {
	W, H   int
	Frames int
	FPS    float64
	Dur    float64
}

func r5Info(t *testing.T, path string) r5Probe {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries",
		"stream=width,height,r_frame_rate,nb_read_frames:format=duration", "-of", "json", "--", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var j struct {
		Streams []struct {
			W      int    `json:"width"`
			H      int    `json:"height"`
			Rate   string `json:"r_frame_rate"`
			Frames string `json:"nb_read_frames"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &j); err != nil || len(j.Streams) != 1 {
		t.Fatalf("ffprobe %s: %v %s", path, err, out)
	}
	s := j.Streams[0]
	p := r5Probe{W: s.W, H: s.H}
	p.Frames, _ = strconv.Atoi(s.Frames)
	p.Dur, _ = strconv.ParseFloat(j.Format.Duration, 64)
	if a, b, ok := strings.Cut(s.Rate, "/"); ok {
		x, _ := strconv.ParseFloat(a, 64)
		y, _ := strconv.ParseFloat(b, 64)
		if y > 0 {
			p.FPS = x / y
		}
	}
	return p
}

func TestR5SheetFileGrid(t *testing.T) {
	d := r5Dir(t)
	// 5 s, un riquadro al secondo: 5 riquadri; --cols 3 → 3 colonne x 2 righe (4:3 → ~2:1 largo/alto... 3*4 : 2*3)
	p := r5Ok(t, d, "sheet", "--video", "in.mp4", "--every", "1", "--cols", "3", "--out", "o")
	a := r5Info(t, p)
	if a.W <= 0 || a.H <= 0 || a.Frames != 1 {
		t.Fatalf("sheet --every: %+v", a)
	}
	if got, want := float64(a.W)/float64(a.H), (3.0*4)/(2.0*3); math.Abs(got-want) > want*0.12 {
		t.Errorf("--every 1 --cols 3: proporzioni %.2f (%dx%d), attese ~%.2f (3 colonne, 2 righe)", got, a.W, a.H, want)
	}
	// --frames 8 --cols 4: 4 colonne, 2 righe
	p = r5Ok(t, d, "sheet", "--video", "in.mp4", "--frames", "8", "--cols", "4", "--out", "o")
	b := r5Info(t, p)
	pb := p
	if got, want := float64(b.W)/float64(b.H), (4.0*4)/(2.0*3); math.Abs(got-want) > want*0.12 {
		t.Errorf("--frames 8 --cols 4: proporzioni %.2f (%dx%d), attese ~%.2f", got, b.W, b.H, want)
	}
	// stessa griglia con --labels: stesse misure (etichette sovraimpresse), contenuto diverso
	p = r5Ok(t, d, "sheet", "--video", "in.mp4", "--frames", "8", "--cols", "4", "--labels", "--out", "o")
	c := r5Info(t, p)
	if c.W != b.W || c.H != b.H {
		t.Errorf("--labels: %dx%d, senza etichette %dx%d (attese stesse misure)", c.W, c.H, b.W, b.H)
	}
	x, _ := os.ReadFile(pb)
	y, _ := os.ReadFile(p)
	if pb == p || bytes.Equal(x, y) {
		t.Errorf("--labels: il foglio è identico a quello senza etichette")
	}
}

func TestR5SheetCartellaEStoryboard(t *testing.T) {
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, "clips"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.mp4", "b.mp4", "c.mp4"} {
		r5Clip(t, filepath.Join(d, "clips", f), "160x90", 2)
		r5Clip(t, filepath.Join(d, f), "160x90", 2)
	}
	if a := r5Info(t, r5Ok(t, d, "sheet", "clips", "--frames", "2", "--cols", "2", "--out", "o")); a.W <= 0 || a.H <= 0 {
		t.Errorf("sheet cartella: %+v", a)
	}
	sb := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},
"scene":[{"id":"s1","tipo":"clip","file":"a.mp4","durata":2},{"id":"s2","tipo":"clip","file":"b.mp4","durata":2},{"id":"s3","tipo":"clip","file":"c.mp4","durata":2}]}`
	if err := os.WriteFile(filepath.Join(d, "sb.json"), []byte(sb), 0o644); err != nil {
		t.Fatal(err)
	}
	st := r5Info(t, r5Ok(t, d, "sheet", "sb.json", "--frames", "2", "--cols", "2", "--labels", "--out", "o2"))
	if st.W <= 0 || st.H <= 0 {
		t.Errorf("sheet storyboard: %+v", st)
	}
	// una riga per scena: 3 righe x 2 colonne di 16:9 → più alto che largo/2 (almeno 3 righe di riquadri)
	if float64(st.H) < float64(st.W)/2*(9.0/16)*3*0.9 {
		t.Errorf("sheet storyboard: %dx%d, attese 3 righe (una per scena)", st.W, st.H)
	}
}

func TestR5Frame(t *testing.T) {
	d := r5Dir(t)
	for _, args := range [][]string{
		{"frame", "--video", "in.mp4", "--at", "2", "--width", "80", "--out", "o"},
		{"frame", "--video", "in.mp4", "--last", "--width", "80", "--out", "o"},
	} {
		p := r5Ok(t, d, args...)
		if !strings.HasSuffix(p, ".png") {
			t.Errorf("giv %v: %s non è un png", args, p)
		}
		if a := r5Info(t, p); a.W != 80 || a.H != 60 {
			t.Errorf("giv %v: %dx%d, attesa 80x60", args, a.W, a.H)
		}
	}
	// senza --width: misura del video
	if a := r5Info(t, r5Ok(t, d, "frame", "--video", "in.mp4", "--at", "1", "--out", "o")); a.W != 160 || a.H != 120 {
		t.Errorf("frame senza --width: %dx%d, atteso 160x120", a.W, a.H)
	}
}

func TestR5Gif(t *testing.T) {
	d := r5Dir(t)
	p := r5Ok(t, d, "gif", "--video", "in.mp4", "--start", "1", "--end", "3", "--width", "80", "--fps", "10", "--out", "o")
	if !strings.HasSuffix(p, ".gif") {
		t.Errorf("%s non è una gif", p)
	}
	a := r5Info(t, p)
	if a.W != 80 || a.H != 60 {
		t.Errorf("gif: %dx%d, attesa 80x60", a.W, a.H)
	}
	if math.Abs(a.FPS-10) > 0.5 {
		t.Errorf("gif: %.2f fps, attesi 10", a.FPS)
	}
	if math.Abs(a.Dur-2) > 0.2 || a.Frames < 19 || a.Frames > 21 {
		t.Errorf("gif: durata %.2f s, %d fotogrammi; attesi 2 s e ~20", a.Dur, a.Frames)
	}
}

func TestR5RifiutiCodice2(t *testing.T) {
	d := r5Dir(t)
	if err := os.MkdirAll(filepath.Join(d, "vuota"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "vuota", "note.txt"), []byte("x"), 0o644)
	cases := [][]string{
		{"frame", "--video", "in.mp4", "--at", "9"},
		{"frame", "--video", "in.mp4", "--at", "-1"},
		{"frame", "--video", "in.mp4", "--at", "1", "--last"},
		{"frame", "--video", "in.mp4"},
		{"frame", "--video", "in.mp4", "--at", "1", "--width", "0"},
		{"frame", "--video", "in.mp4", "--at", "1", "--width", "-5"},
		{"gif", "--video", "in.mp4", "--start", "1", "--end", "9"},
		{"gif", "--video", "in.mp4", "--start", "7", "--end", "8"},
		{"gif", "--video", "in.mp4", "--start", "3", "--end", "2"},
		{"gif", "--video", "in.mp4", "--fps", "0"},
		{"gif", "--video", "in.mp4", "--fps", "-3"},
		{"gif", "--video", "in.mp4", "--fps", "500"},
		{"gif", "--video", "in.mp4", "--width", "0"},
		{"gif", "--video", "in.mp4", "--width", "-2"},
		{"sheet", "in.mp4", "--cols", "0"},
		{"sheet", "in.mp4", "--cols", "-1"},
		{"sheet", "in.mp4", "--cols", "500"},
		{"sheet", "in.mp4", "--every", "0"},
		{"sheet", "in.mp4", "--frames", "0"},
		{"sheet", "in.mp4", "--every", "1", "--frames", "4"},
		{"sheet", "vuota"},
	}
	for _, c := range cases {
		so, se, code := r5Run(t, d, append(c, "--out", "o")...)
		if code != 2 {
			t.Errorf("giv %v: codice %d, atteso 2\n%s%s", c, code, so, se)
		}
	}
	if _, err := os.Stat(filepath.Join(d, "o")); err == nil {
		t.Error("i rifiuti hanno creato la cartella d'uscita")
	}
}

func TestR5DryRunNienteFile(t *testing.T) {
	d := r5Dir(t)
	for _, c := range [][]string{
		{"sheet", "in.mp4", "--frames", "4"},
		{"frame", "--video", "in.mp4", "--at", "1"},
		{"gif", "--video", "in.mp4", "--start", "1", "--end", "2"},
	} {
		so, se, code := r5Run(t, d, append(c, "--out", "o", "--dry-run")...)
		if code != 0 || !json.Valid([]byte(so)) {
			t.Fatalf("%v dry-run: codice %d\n%s%s", c, code, so, se)
		}
	}
	if es, err := os.ReadDir(filepath.Join(d, "o")); err == nil && len(es) > 0 {
		t.Error("--dry-run ha scritto file")
	}
}
