package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gen-image-video-cli/internal/montage"
)

// Test R-T0 (CLI-937): base comune dei comandi locali, via un'operazione di prova.

func init() {
	registerPost(postOp{Name: "provar0", Usage: "operazione di prova dei test", Run: func(args []string) error {
		fs, lf := newLocalFlags("post provar0", "giv post provar0")
		if err := parseLocal(fs, args); err != nil {
			return err
		}
		src, err := lf.source()
		if err != nil {
			return err
		}
		return runLocal(lf, localOp{Command: "post provar0", Inputs: map[string]string{"video": src.Path},
			Name: localName(src.Path, "provar0"), Ext: ".mp4", Params: map[string]any{"k": 1},
			Build: func(w *montage.Work, staged string) error {
				return w.FFmpeg([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-n", "-i", "file:" + mustAbs(src.Path), "-c", "copy", "-f", "mp4", "file:" + staged})
			}})
	}})
}

func mustAbs(p string) string { a, _ := filepath.Abs(p); return a }

func r0Video(t *testing.T, path string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-shortest", "-pix_fmt", "yuv420p", "-y", "--", path).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
}

// r0Post lancia giv post provar0 in-process nella cartella corrente, catturando stdout.
func r0Post(t *testing.T, args ...string) (stdout string, code int) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	err := cmdPost(append([]string{"provar0"}, args...))
	w.Close()
	os.Stdout = old
	stdout = <-done
	code = 0
	if err != nil {
		code, _ = exitCode(err)
	}
	return
}

func r0Files(t *testing.T, dir string) []string {
	var n []string
	filepath.Walk(dir, func(p string, i os.FileInfo, err error) error {
		if err == nil && !i.IsDir() {
			n = append(n, p)
		}
		return nil
	})
	return n
}

func TestR0DryRunNienteFile(t *testing.T) {
	d := t.TempDir()
	t.Chdir(d)
	r0Video(t, "in.mp4")
	stdout, code := r0Post(t, "--video", "in.mp4", "--out", "o", "--dry-run")
	if code != 0 {
		t.Fatalf("codice %d", code)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("stdout non è il solo JSON: %v\n%s", err, stdout)
	}
	if m["dry_run"] != true || m["command"] != "post provar0" {
		t.Fatalf("piano: %v", m)
	}
	if _, e := os.Stat("o"); e == nil {
		t.Fatal("--dry-run ha creato la cartella d'uscita")
	}
	if _, e := os.Stat("giv-log.jsonl"); e == nil {
		t.Fatal("--dry-run ha scritto il registro")
	}
}

func TestR0EsecuzioneManifestELog(t *testing.T) {
	d := t.TempDir()
	t.Chdir(d)
	r0Video(t, "in.mp4")
	stdout, code := r0Post(t, "--video", "in.mp4", "--out", "o")
	if code != 0 {
		t.Fatalf("codice %d: %s", code, stdout)
	}
	var m localManifest
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("stdout: %v\n%s", err, stdout)
	}
	if len(m.Files) != 1 || m.CostUSD != 0 || m.Provider != "local" || m.Model != "ffmpeg" {
		t.Fatalf("manifest: %+v", m)
	}
	if st, err := os.Stat(m.Files[0].Path); err != nil || st.Size() == 0 {
		t.Fatalf("uscita non scritta: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(r0Read(t, "giv-log.jsonl")), "\n")
	if len(lines) != 1 {
		t.Fatalf("righe di log: %d", len(lines))
	}
	var e map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil || e["cost_usd"] != float64(0) || e["provider"] != "local" || e["command"] != "post provar0" {
		t.Fatalf("riga di log: %v %v", e, err)
	}
	stdout, _ = runGivIn(t, d, "log")
	if !strings.Contains(stdout, "post provar0") {
		t.Fatalf("giv log non la mostra: %s", stdout)
	}
}

func r0Read(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func runGivIn(t *testing.T, dir string, args ...string) (string, error) {
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	var so bytes.Buffer
	cmd.Stdout = &so
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	err := cmd.Run()
	return so.String(), err
}

// L'ingresso non si sovrascrive: --out = cartella dell'ingresso e --name = suo nome.
func TestR0IngressoNonSovrascritto(t *testing.T) {
	d := t.TempDir()
	t.Chdir(d)
	r0Video(t, "in.mp4")
	before := r0Read(t, "in.mp4")
	if _, code := r0Post(t, "--video", "in.mp4", "--out", ".", "--name", "in"); code != 2 {
		t.Fatalf("uscita = ingresso: codice %d, atteso 2", code)
	}
	if r0Read(t, "in.mp4") != before {
		t.Fatal("ingresso modificato")
	}
}

// --name con cartelle o ../ (uscita fuori da --out) = codice 2, nessun file.
func TestR0UscitaFuoriCartellaCLI(t *testing.T) {
	d := t.TempDir()
	t.Chdir(d)
	r0Video(t, "in.mp4")
	for _, n := range []string{"../fuori", "a/b", "/tmp/x", ".."} {
		if _, code := r0Post(t, "--video", "in.mp4", "--out", "o", "--name", n); code != 2 {
			t.Fatalf("--name %q: codice %d, atteso 2", n, code)
		}
	}
	if fs := r0Files(t, "o"); len(fs) != 0 {
		t.Fatalf("scritti file: %v", fs)
	}
}

// Un ingresso che comincia con '-' non è un'opzione.
func TestR0VideoConTrattino(t *testing.T) {
	d := t.TempDir()
	t.Chdir(d)
	r0Video(t, "-x.mp4")
	stdout, code := r0Post(t, "--video", "-x.mp4", "--out", "o")
	if code != 0 {
		t.Fatalf("codice %d: %s", code, stdout)
	}
	if len(r0Files(t, "o")) != 1 {
		t.Fatalf("file: %v", r0Files(t, "o"))
	}
}
