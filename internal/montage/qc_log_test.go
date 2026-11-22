package montage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// CLI-983: giv qc non perde difetti quando l'uscita di ffmpeg supera 1 MiB.

const qcBlackLine = "[blackdetect @ 0x1] black_start:0 black_end:6 black_duration:6\n"

// riempimento: righe di avanzamento qualsiasi, oltre n byte.
func qcFiller(n int) string {
	return strings.Repeat("frame=  100 fps= 25 q=-0.0 size=N/A time=00:00:04.00 bitrate=N/A speed=1x\n", n/80+1)
}

func TestQCLogLungoDifettoIniziale(t *testing.T) {
	filler := qcFiller(2 << 20)
	tail := filler[len(filler)-(1<<20):]
	p := newQCParser(10)
	p.Write([]byte(qcBlackLine))
	// a pezzi da 4 KiB: le righe si spezzano a metà
	for b := []byte(filler); len(b) > 0; {
		n := 4096
		if n > len(b) {
			n = len(b)
		}
		p.Write(b[:n])
		b = b[n:]
	}
	a := p.finish(tail, false)
	if len(a.Black) != 1 || a.Black[0].Start != 0 || a.Black[0].End != 6 {
		t.Fatalf("tratto nero perso con uscita > 1 MiB: %+v", a.Black)
	}
}

// Con un ffmpeg finto che scrive 2 MiB dopo il difetto: Tool.QC lo riporta come difetto.
func TestQCLogLungoToolDifetto(t *testing.T) {
	dir := t.TempDir()
	filler := filepath.Join(dir, "filler.txt")
	if err := os.WriteFile(filler, []byte(qcFiller(2<<20)), 0o644); err != nil {
		t.Fatal(err)
	}
	loud := `{"input_i":"-20.0","input_tp":"-5.0","input_lra":"3.0","input_thresh":"-30.0","output_i":"-14","output_tp":"-1","output_lra":"3","output_thresh":"-24","normalization_type":"dynamic","target_offset":"0.0"}`
	script := "#!/bin/sh\nprintf '%s' '" + strings.TrimSuffix(qcBlackLine, "\n") + "' >&2\necho >&2\ncat '" + filler + "' >&2\necho '" + loud + "' >&2\n"
	fake := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "m.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=10",
		"-f", "lavfi", "-i", "sine=duration=10", "-shortest", "-pix_fmt", "yuv420p", "-y", media).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg: %v %s", err, out)
	}
	info := &Info{Format: "mov,mp4", Duration: 10, Video: &VideoStream{Codec: "h264", Width: 320, Height: 180, FPS: 25}, Audio: &AudioStream{Codec: "aac", SampleRate: 48000, Channels: 2}}
	tool := Tool{FFmpeg: fake}
	rep, err := tool.QC(context.Background(), media, info, QCOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var black *QCCheck
	for i := range rep.Checks {
		if rep.Checks[i].Name == "black" {
			black = &rep.Checks[i]
		}
	}
	if black == nil || black.Result != QCDefect {
		t.Fatalf("black: %+v, atteso difetto (60%% della durata)", black)
	}
	if rep.Defects() < 1 || rep.Result != QCDefect {
		t.Errorf("rapporto: result %q, difetti %d", rep.Result, rep.Defects())
	}
}

// Un'uscita normale (nessun tratto) resta pulita.
func TestQCLogPulito(t *testing.T) {
	a := parseQCLog(qcFiller(1000), 10, false)
	if len(a.Black)+len(a.Freeze)+len(a.Silence) != 0 {
		t.Errorf("tratti inventati: %+v", a)
	}
}
