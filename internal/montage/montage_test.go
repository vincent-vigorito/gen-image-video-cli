package montage

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func needFFmpeg(t *testing.T) {
	t.Helper()
	for _, b := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skip(b + " non disponibile")
		}
	}
}

func ff(t *testing.T, args ...string) {
	t.Helper()
	full := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)
	if out, err := exec.Command("ffmpeg", full...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

// synthVideo genera una clip di testsrc2 con audio sine.
func synthVideo(t *testing.T, path string, size string, fps int, dur float64) {
	t.Helper()
	d := num(dur)
	ff(t, "-f", "lavfi", "-i", "testsrc2=size="+size+":rate="+itoa(fps)+":duration="+d,
		"-f", "lavfi", "-i", "sine=frequency=300:duration="+d,
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", path)
}

func itoa(i int) string { return num(float64(i)) }

func synthAudio(t *testing.T, path string, freq int, dur float64) {
	t.Helper()
	ff(t, "-f", "lavfi", "-i", "sine=frequency="+itoa(freq)+":duration="+num(dur), path)
}

// rms restituisce l'energia dell'audio in finestre da 0,1 s.
func rms(t *testing.T, path string) []float64 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-vn", "-ac", "1", "-ar", "8000", "-f", "s16le", "-").Output()
	if err != nil {
		t.Fatal(err)
	}
	n := len(out) / 2
	var res []float64
	for s := 0; s+800 <= n; s += 800 {
		var sum float64
		for i := s; i < s+800; i++ {
			v := float64(int16(binary.LittleEndian.Uint16(out[2*i:])))
			sum += v * v
		}
		res = append(res, math.Sqrt(sum/800))
	}
	return res
}

func basePlan(t *testing.T, dir string) Plan {
	a, b, c := filepath.Join(dir, "a.mp4"), filepath.Join(dir, "b.mp4"), filepath.Join(dir, "c.mp4")
	synthVideo(t, a, "160x90", 24, 2)
	synthVideo(t, b, "120x120", 30, 2)
	synthVideo(t, c, "90x160", 15, 2)
	return Plan{Width: 128, Height: 72, FPS: 25, Output: filepath.Join(dir, "out.mp4"),
		Clips: []Clip{{Path: a, Duration: 2, KeepAudio: true}, {Path: b, Duration: 1.5, KeepAudio: true}, {Path: c, Duration: 2, KeepAudio: true}}}
}

func TestMontageTreClipFormatiDiversi(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	p := basePlan(t, dir)
	res, err := Tool{}.Montage(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	info, err := Tool{}.Probe(context.Background(), p.Output)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(info.Duration-5.5) > 0.1 || math.Abs(res.Duration-5.5) > 0.1 {
		t.Errorf("durata %v (attesa 5.5), result %v", info.Duration, res.Duration)
	}
	if info.Video == nil || info.Audio == nil {
		t.Fatalf("tracce: %+v", info)
	}
	if info.Video.Width != 128 || info.Video.Height != 72 || math.Abs(info.Video.FPS-25) > 0.01 || info.Video.Codec != "h264" {
		t.Errorf("video %+v", info.Video)
	}
	if info.Audio.Codec != "aac" || info.Audio.SampleRate != 48000 || info.Audio.Channels != 2 {
		t.Errorf("audio %+v", info.Audio)
	}
	if !strings.Contains(info.Format, "mp4") {
		t.Errorf("formato %q", info.Format)
	}
}

func TestMontageVoceMusicaDissolvenza(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	p := basePlan(t, dir)
	voice := filepath.Join(dir, "voce.wav")
	music := filepath.Join(dir, "musica.wav")
	synthAudio(t, voice, 1000, 0.6)
	synthAudio(t, music, 200, 1)
	for i := range p.Clips {
		p.Clips[i].KeepAudio = false
	}
	// voce sulla seconda scena (parte a 2 s), 0,5 s dentro: attesa a 2,5 s
	p.Clips[1].Voice, p.Clips[1].VoiceAt = voice, 0.5
	if _, err := (Tool{}).Montage(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	r := rms(t, p.Output)
	first := -1
	for i, v := range r {
		if v > 500 {
			first = i
			break
		}
	}
	if first < 24 || first > 26 {
		t.Errorf("voce inizia nella finestra %d (attesa ~25, 2,5 s)", first)
	}
	if r[5] > 100 {
		t.Errorf("prima della voce c'e' rumore: %v", r[5])
	}

	// musica: presente per tutta la durata, anche se la scena e' muta
	p.Clips[1].Voice = ""
	p.Music = &Music{Path: music, Volume: 1}
	p.Overwrite = true
	if _, err := (Tool{}).Montage(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	r = rms(t, p.Output)
	if len(r) < 50 || r[2] < 500 || r[len(r)-3] < 500 {
		t.Errorf("musica non presente su tutto il montato: %d finestre", len(r))
	}

	// dissolvenze: 5.5 - 0.5 - 0.5 = 4.5
	p.Music = nil
	p.Clips[1].Transition = Transition{Kind: Fade, Duration: 0.5}
	p.Clips[2].Transition = Transition{Kind: FadeBlack, Duration: 0.5}
	if _, total, err := Timeline(p); err != nil || math.Abs(total-4.5) > 1e-6 {
		t.Errorf("Timeline total %v err %v", total, err)
	}
	if _, err := (Tool{}).Montage(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	info, err := Tool{}.Probe(context.Background(), p.Output)
	if err != nil || math.Abs(info.Duration-4.5) > 0.1 {
		t.Errorf("durata con dissolvenze %v err %v", info, err)
	}
}

func TestArgsSenzaEsecuzione(t *testing.T) {
	dir := t.TempDir()
	hostile := filepath.Join(dir, "-evil.mp4")
	sub := filepath.Join(dir, "s'ub,[x];.ass")
	p := Plan{Width: 128, Height: 72, FPS: 25, Output: filepath.Join(dir, "out.mp4"), Subtitles: sub,
		Clips: []Clip{{Path: hostile, Duration: 2}, {Path: "-rel.mp4", Duration: 2}}}
	args, err := Args(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 10 {
		t.Fatalf("args %v", args)
	}
	ins := 0
	for i, a := range args {
		if a == "-i" {
			ins++
			if !strings.HasPrefix(args[i+1], "file:/") {
				t.Errorf("ingresso non protetto: %q", args[i+1])
			}
		}
		if strings.HasPrefix(a, "-") && strings.Contains(a, "evil") || a == "-rel.mp4" {
			t.Errorf("nome file come opzione: %q", a)
		}
	}
	if ins != 2 {
		t.Errorf("ingressi %d", ins)
	}
	joined := strings.Join(args, "\x00")
	var graph string
	for i, a := range args {
		if a == "-filter_complex" {
			graph = args[i+1]
		}
	}
	if graph == "" || !strings.Contains(graph, "ass=filename=") {
		t.Fatalf("manca il filtro ass: %q", joined)
	}
	if strings.Contains(graph, "s'ub,[x];") {
		t.Errorf("percorso sottotitoli non protetto nel grafo: %s", graph)
	}
	if !strings.Contains(graph, `\,`) || !strings.Contains(graph, `\[x\]`) || !strings.Contains(graph, `\;`) {
		t.Errorf("escape mancanti: %s", graph)
	}
	// senza Overwrite: -n; con: -y
	if !contains(args, "-n") || contains(args, "-y") {
		t.Errorf("sovrascrittura: %v", args)
	}
	p.Overwrite = true
	args, _ = Args(p)
	if !contains(args, "-y") || contains(args, "-n") {
		t.Errorf("Overwrite: %v", args)
	}
	// srt non accettato da Args
	p.Subtitles = filepath.Join(dir, "x.srt")
	if _, err := Args(p); !errors.Is(err, ErrInvalid) {
		t.Errorf("srt in Args: %v", err)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestMontageRifiuti(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	clip := filepath.Join(dir, "a.mp4")
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		synthVideo(t, clip, "160x90", 24, 1)
	} else {
		os.WriteFile(clip, []byte("x"), 0o600)
	}
	plan := func() Plan {
		return Plan{Width: 128, Height: 72, FPS: 25, Output: filepath.Join(dir, "out.mp4"), Clips: []Clip{{Path: clip, Duration: 1}}}
	}

	// ffmpeg assente
	if _, err := (Tool{FFmpeg: "/nonesiste/ffmpeg", FFprobe: "/nonesiste/ffprobe"}).Montage(ctx, plan()); !errors.Is(err, ErrMissingTool) {
		t.Errorf("ffmpeg assente: %v", err)
	}
	if err := (Tool{FFmpeg: "/nonesiste/ffmpeg"}).Check(); !errors.Is(err, ErrMissingTool) {
		t.Errorf("Check: %v", err)
	}

	// clip mancante
	p := plan()
	p.Clips[0].Path = filepath.Join(dir, "manca.mp4")
	_, err := Tool{}.Montage(ctx, p)
	if !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrMissingTool) {
		t.Errorf("clip mancante: %v", err)
	}
	if err != nil && errors.Is(err, ErrInvalid) && !strings.Contains(err.Error(), "manca.mp4") {
		t.Errorf("errore poco chiaro: %v", err)
	}

	// nome ostile: a capo nel percorso
	p = plan()
	p.Clips[0].Path = clip + "\n-i /etc/passwd"
	if _, err := (Tool{}).Montage(ctx, p); err == nil {
		t.Errorf("nome con a capo accettato")
	}
	if _, err := Args(p); !errors.Is(err, ErrInvalid) {
		t.Errorf("Args nome ostile: %v", err)
	}

	// sottotitoli inesistenti
	p = plan()
	p.Subtitles = filepath.Join(dir, "manca.ass")
	if _, err := (Tool{}).Montage(ctx, p); err == nil {
		t.Errorf("sottotitoli inesistenti accettati")
	}
	if _, err := os.Stat(p.Output); err == nil {
		t.Errorf("uscita scritta nonostante il rifiuto")
	}

	// uscita esistente senza Overwrite
	p = plan()
	os.WriteFile(p.Output, []byte("vecchio"), 0o600)
	if _, err := (Tool{}).Montage(ctx, p); err == nil || (!errors.Is(err, ErrExists) && !errors.Is(err, ErrMissingTool)) {
		t.Errorf("uscita esistente: %v", err)
	}
	if b, _ := os.ReadFile(p.Output); string(b) != "vecchio" {
		t.Errorf("file esistente toccato")
	}
}

func TestMontageSottotitoliAssEHostile(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	p := basePlan(t, dir)
	p.Clips = p.Clips[:1]
	sub := filepath.Join(dir, "s'ub,[x];.ass")
	ass := "[Script Info]\nScriptType: v4.00+\nPlayResX: 128\nPlayResY: 72\n\n[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour\nStyle: Default,Arial,20,&H00FFFFFF\n\n[Events]\nFormat: Layer, Start, End, Style, Text\nDialogue: 0,0:00:00.00,0:00:01.00,Default,Ciao\n"
	if err := os.WriteFile(sub, []byte(ass), 0o600); err != nil {
		t.Fatal(err)
	}
	p.Subtitles = sub
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := (Tool{}).Montage(ctx, p); err != nil {
		t.Fatal(err)
	}
	info, err := Tool{}.Probe(ctx, p.Output)
	if err != nil || math.Abs(info.Duration-2) > 0.1 {
		t.Errorf("%v %v", info, err)
	}
}
