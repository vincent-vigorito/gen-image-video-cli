package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/provider"
)

// Base comune dei comandi sulle clip (O-T1): un comando finto "extend" con
// provider finto "xai" passa da controlli, --dry-run, --max-cost, manifest e registro.

var fakeSpec = clipSpec{Op: "extend", Providers: []string{"xai"}}

func fakeTable() []caps.Op {
	return []caps.Op{{
		Op: "extend", Model: "m", Flags: []string{"video", "duration", "prompt"},
		Required: []string{"video", "prompt"}, Durations: []int{2, 3, 4, 5, 6, 7, 8, 9, 10},
		DefaultDuration: 6, SourceMax: 15, Source: "listino finto",
		Price: func(r provider.ClipRequest) (float64, string, string) {
			if r.Source.Duration <= 0 {
				return 0, "", "durata del sorgente non nota"
			}
			return 0.1 * float64(r.Duration), "0.1 $/s", ""
		},
	}, {
		Op: "avatar", Model: "m", Flags: []string{"image", "audio"}, Required: []string{"image", "audio"},
		Price: func(provider.ClipRequest) (float64, string, string) { return 1, "", "" },
	}}
}

// clipClient è un provider finto che fa le clip.
type clipClient struct {
	calls int
	fail  error
	req   provider.ClipRequest
}

func (c *clipClient) Name() string { return "xai" }
func (c *clipClient) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (c *clipClient) GenerateImage(context.Context, provider.ImageRequest) (*provider.Result, error) {
	return nil, errors.New("non usato")
}
func (c *clipClient) GenerateVideo(context.Context, provider.VideoRequest) (*provider.Result, error) {
	return nil, errors.New("non usato")
}
func (c *clipClient) GenerateClip(_ context.Context, req provider.ClipRequest) (*provider.Result, error) {
	c.calls++
	c.req = req
	provider.ClipJobStarted("xai", provider.Job{ID: "job-1"}, req)
	if c.fail != nil {
		return nil, httpx.Job("job-1", c.fail)
	}
	return &provider.Result{Media: []provider.Media{{Mime: "video/mp4", Data: []byte("mp4")}}, JobID: "job-1"}, nil
}

var mp4Head = []byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'm', 'p', '4', '2', 0, 0, 0, 0, 'm', 'p', '4', '2', 0, 0, 0, 0}

// clipEnv prepara cartella di lavoro, tabella finta e ffprobe finto (durata dur secondi).
func clipEnv(t *testing.T, dur string) {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", t.TempDir())
	old := clipOpTables["xai"]
	clipOpTables["xai"] = fakeTable
	oldTool := mediaTool
	script := `#!/bin/sh
for a; do p="$a"; done
case "$p" in
*.wav) echo '{"format":{"format_name":"wav","duration":"3.0"},"streams":[{"codec_type":"audio","codec_name":"pcm","sample_rate":"16000","channels":1}]}';;
*) echo '{"format":{"format_name":"mov,mp4","duration":"` + dur + `"},"streams":[{"codec_type":"video","codec_name":"h264","width":1280,"height":720,"avg_frame_rate":"24/1"}]}';;
esac
`
	bin := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mediaTool = montage.Tool{FFprobe: bin}
	t.Cleanup(func() { clipOpTables["xai"] = old; mediaTool = oldTool })
	os.WriteFile("clip.mp4", mp4Head, 0o644)
}

func runClipArgs(t *testing.T, c *clipClient, args ...string) (string, error) {
	t.Helper()
	o, err := parseClipFlags(fakeSpec, args)
	if err != nil {
		return "", err
	}
	o.client = c
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err = runClip(fakeSpec, o)
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String(), err
}

func wantUsage(t *testing.T, name string, c *clipClient, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: atteso rifiuto", name)
		return
	}
	if code, _ := exitCode(err); code != 2 {
		t.Errorf("%s: code=%d (%v), atteso 2", name, code, err)
	}
	if c.calls != 0 {
		t.Errorf("%s: %d chiamate al provider, attese 0", name, c.calls)
	}
}

func TestClipValidoManifestERegistro(t *testing.T) {
	clipEnv(t, "8.0")
	c := &clipClient{}
	out, err := runClipArgs(t, c, "--video", "clip.mp4", "--duration", "5", "--out", "o", "--max-cost", "1", "continua la scena")
	if err != nil || c.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, c.calls)
	}
	var man map[string]any
	if err := json.Unmarshal([]byte(out), &man); err != nil {
		t.Fatalf("manifest: %v %q", err, out)
	}
	if man["command"] != "extend" || man["provider"] != "xai" || man["model"] != "m" || man["cost_est_usd"] != 0.5 {
		t.Errorf("manifest=%v", man)
	}
	in, _ := man["inputs"].(map[string]any)
	v, _ := in["video"].(map[string]any)
	if v["path"] != "clip.mp4" || v["sha256"] == "" || v["sha256"] == nil {
		t.Errorf("inputs.video=%v", in)
	}
	if c.req.Source.Duration != 8 || c.req.Source.Height != 720 || c.req.Duration != 5 {
		t.Errorf("richiesta=%+v", c.req)
	}
	for _, f := range []string{logFile, jobsFile} {
		b, _ := os.ReadFile(f)
		if !strings.Contains(string(b), "job-1") && f == jobsFile || f == logFile && !strings.Contains(string(b), `"command":"extend"`) {
			t.Errorf("%s=%q", f, b)
		}
	}
}

func TestClipDryRunSenzaChiamate(t *testing.T) {
	clipEnv(t, "8.0")
	c := &clipClient{}
	out, err := runClipArgs(t, c, "--dry-run", "--video", "clip.mp4", "--duration", "5", "x")
	if err != nil || c.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, c.calls)
	}
	var p map[string]any
	json.Unmarshal([]byte(out), &p)
	if p["dry_run"] != true || p["command"] != "extend" || p["cost_est_usd"] != 0.5 {
		t.Errorf("piano=%v", p)
	}
	if _, err := os.Stat(logFile); err == nil {
		t.Error("dry-run ha scritto il registro")
	}
}

func TestClipAudioValidoEFileAccettati(t *testing.T) {
	clipEnv(t, "8.0")
	os.WriteFile("v.wav", append([]byte("RIFF\x24\x00\x00\x00WAVE"), make([]byte, 40)...), 0o644)
	os.WriteFile("i.png", []byte("\x89PNG\r\n\x1a\n"), 0o644)
	c := &clipClient{}
	o, _ := parseClipFlags(clipSpec{Op: "avatar", Providers: []string{"xai"}}, []string{"--dry-run", "--image", "i.png", "--audio", "v.wav"})
	if _, err := prepareClip(clipSpec{Op: "avatar", Providers: []string{"xai"}}, o); err != nil {
		t.Errorf("wav valido rifiutato: %v", err)
	}
	_ = c
}

func TestClipRifiutiCodice2SenzaChiamate(t *testing.T) {
	clipEnv(t, "20.0") // sorgente oltre SourceMax=15
	os.WriteFile("short.mp4", mp4Head, 0o644)
	os.WriteFile("foto.png", []byte("\x89PNG\r\n\x1a\n"), 0o644)
	os.WriteFile("v.mp3", []byte("ID3\x03\x00\x00\x00\x00\x00\x00"), 0o644)
	big, _ := os.Create("big.mp4")
	big.Write(mp4Head)
	big.Truncate(provider.MaxVideoBytes + 1)
	big.Close()
	cases := map[string][]string{
		"file inesistente":      {"--video", "nonc'e.mp4", "x"},
		"tipo sbagliato":        {"--video", "foto.png", "x"},
		"oltre 50 MiB":          {"--video", "big.mp4", "x"},
		"url http":              {"--video", "http://example.test/a.mp4", "x"},
		"sorgente troppo lungo": {"--video", "clip.mp4", "--duration", "5", "x"},
		"flag non ammesso":      {"--video", "short.mp4", "--resolution", "720p", "x"},
		"audio non ammesso":     {"--video", "short.mp4", "--audio", "v.mp3", "x"},
		"manca il prompt":       {"--video", "short.mp4"},
		"manca il video":        {"x"},
		"durata non ammessa":    {"--video", "short.mp4", "--duration", "99", "x"},
		"from non valido":       {"--video", "short.mp4", "--from", "a b", "x"},
	}
	for name, args := range cases {
		c := &clipClient{}
		_, err := runClipArgs(t, c, args...)
		wantUsage(t, name, c, err)
	}
}

func TestClipMaxCost(t *testing.T) {
	clipEnv(t, "8.0")
	c := &clipClient{}
	_, err := runClipArgs(t, c, "--video", "clip.mp4", "--duration", "5", "--max-cost", "0.49", "x")
	wantUsage(t, "oltre il tetto", c, err)
	// senza misure (ffprobe assente) la stima non c'è: con tetto rifiuto, senza passa
	mediaTool = montage.Tool{FFprobe: "/nonesiste/ffprobe"}
	_, err = runClipArgs(t, c, "--video", "clip.mp4", "--duration", "5", "--max-cost", "5", "x")
	wantUsage(t, "non stimabile", c, err)
	if _, err = runClipArgs(t, c, "--video", "clip.mp4", "--duration", "5", "--out", "o", "x"); err != nil || c.calls != 1 {
		t.Errorf("senza tetto: err=%v calls=%d", err, c.calls)
	}
}

func TestClipJobFallitoNelRegistro(t *testing.T) {
	clipEnv(t, "8.0")
	c := &clipClient{fail: errors.New("boom")}
	_, err := runClipArgs(t, c, "--video", "clip.mp4", "--duration", "5", "x")
	if err == nil || c.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, c.calls)
	}
	b, _ := os.ReadFile(logFile)
	if !strings.Contains(string(b), `"command":"extend"`) || !strings.Contains(string(b), "job-1") {
		t.Errorf("registro=%q", b)
	}
	if j, _ := os.ReadFile(jobsFile); !strings.Contains(string(j), "job-1") {
		t.Errorf("giv-jobs=%q", j)
	}
}

// i comandi esistenti non cambiano: nessun campo nuovo vuoto nel manifest di video/image.
func TestManifestSenzaCampiClipNuovi(t *testing.T) {
	b, _ := json.Marshal(manifest{Provider: "p", Model: "m", Prompt: "x"})
	for _, k := range []string{`"video"`, `"audio"`, `"from"`, `"inputs"`, `"command"`} {
		if strings.Contains(string(b), k) {
			t.Errorf("manifest vuoto contiene %s: %s", k, b)
		}
	}
}
