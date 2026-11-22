package montage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Test R-T0 (CLI-937): base comune dei comandi locali. Materiale sintetico.

func r0Video(t *testing.T, path string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=160x120:rate=10:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-shortest", "-pix_fmt", "yuv420p", "-y", "--", path).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg non disponibile: %v %s", err, out)
	}
}

func copyOp(src string) func(w *Work, staged string) error {
	return func(w *Work, staged string) error {
		in, err := inputArgs(src, videoFormats)
		if err != nil {
			return err
		}
		o, err := mp4Out(staged)
		if err != nil {
			return err
		}
		args := append(ffArgs(), in...)
		args = append(args, "-c", "copy")
		return w.FFmpeg(append(args, o...))
	}
}

// Un'uscita fuori dalla cartella indicata è rifiutata (ErrInvalid), senza scrivere.
func TestR0UscitaFuoriCartella(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "in.mp4")
	r0Video(t, src)
	root := filepath.Join(d, "out")
	for _, out := range []string{filepath.Join(d, "fuori.mp4"), filepath.Join(root, "..", "fuori2.mp4")} {
		_, err := Tool{}.RunOp(context.Background(), Op{Inputs: []string{src}, Output: out, Root: root, Build: copyOp(src)})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("uscita %s: atteso ErrInvalid, ottenuto %v", out, err)
		}
		if _, e := os.Stat(out); e == nil {
			t.Fatalf("scritto %s", out)
		}
	}
}

// L'ingresso non viene mai sovrascritto, anche con uscita = ingresso.
func TestR0IngressoMaiSovrascritto(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "in.mp4")
	r0Video(t, src)
	before, _ := os.ReadFile(src)
	res, err := Tool{}.RunOp(context.Background(), Op{Inputs: []string{src}, Output: src, Root: d, Build: copyOp(src)})
	if err != nil {
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("errore inatteso: %v", err)
		}
	} else if res.Output == src {
		t.Fatalf("uscita = ingresso pubblicata")
	}
	after, _ := os.ReadFile(src)
	if !bytes.Equal(before, after) {
		t.Fatal("ingresso modificato")
	}
}

// Un percorso che comincia con '-' non diventa un'opzione di ffmpeg.
func TestR0PercorsoConTrattino(t *testing.T) {
	d := t.TempDir()
	t.Chdir(d)
	r0Video(t, "-x.mp4")
	res, err := Tool{}.RunOp(context.Background(), Op{Inputs: []string{"-x.mp4"}, Output: "out/-y.mp4", Root: "out", Build: copyOp("-x.mp4")})
	if err != nil {
		t.Fatalf("RunOp con ingresso '-x.mp4': %v", err)
	}
	if _, e := os.Stat(res.Output); e != nil {
		t.Fatal(e)
	}
}

// Testo con i caratteri speciali di ffmpeg e .ass: reso alla lettera, il filtro non dipende dal testo.
func TestR0TestoAllaLettera(t *testing.T) {
	special := "a:b,c\\d{e}f%g'h\"i\nj\\N{\\an1}k;l"
	data, err := ASS(160, 120, SafeMargins(160, 120), []Text{{Text: special, Start: 0, End: 1, Pos: Center, Style: DefaultTextStyle(120)}})
	if err != nil {
		t.Fatal(err)
	}
	var dialogue []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "Dialogue:") {
			dialogue = append(dialogue, l)
		}
	}
	if len(dialogue) != 1 {
		t.Fatalf("un testo con a capo deve restare un solo Dialogue, ottenuti %d", len(dialogue))
	}
	txt := dialogue[0][strings.LastIndex(dialogue[0], ",,")+2:]
	if strings.Contains(strings.ReplaceAll(strings.ReplaceAll(txt, `\{`, ""), `\}`, ""), "{") {
		t.Fatalf("graffa non protetta: %q", txt)
	}
	if strings.Contains(txt, "\n") || !strings.Contains(txt, `\N`) {
		t.Fatalf("a capo non reso come \\N: %q", txt)
	}
	// il comando (filtro) non dipende dal testo
	f1, err1 := ASSFilter("/tmp/w/a.ass", "")
	f2, err2 := ASSFilter("/tmp/w/a.ass", "")
	if err1 != nil || err2 != nil || f1 != f2 || strings.Contains(f1, "%g") {
		t.Fatalf("filtro: %q %v", f1, err1)
	}

	// resa vera con ffmpeg/libass: riesce, e il file scritto è un video
	d := t.TempDir()
	src := filepath.Join(d, "in.mp4")
	r0Video(t, src)
	res, err := Tool{}.RunOp(context.Background(), Op{Inputs: []string{src}, Output: filepath.Join(d, "o", "t.mp4"), Root: filepath.Join(d, "o"),
		Build: func(w *Work, staged string) error {
			ass, err := w.WriteFile("t.ass", data)
			if err != nil {
				return err
			}
			flt, err := ASSFilter(ass, "")
			if err != nil {
				return err
			}
			in, _ := inputArgs(src, videoFormats)
			o, _ := mp4Out(staged)
			args := append(ffArgs(), in...)
			args = append(args, "-vf", flt, "-an")
			args = append(args, h264Args()...)
			return w.FFmpeg(append(args, o...))
		}})
	if err != nil {
		t.Fatalf("resa con testo speciale: %v", err)
	}
	if res.Info == nil || res.Info.Video == nil {
		t.Fatal("uscita senza video")
	}
}
