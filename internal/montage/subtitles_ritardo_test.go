package montage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const assRitardo = `[Script Info]
ScriptType: v4.00+

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:00.50,0:00:01.50,Default,,0,0,0,,uno, con virgola
Dialogue: 0,0:00:02.10,0:00:03.00,Default,,0,0,0,,due
`

// CLI-888 caso 1: i sottotitoli seguono il ritardo della voce della clip in cui iniziano.
func TestShiftSubtitlesSegueRitardo(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "s.ass"), filepath.Join(dir, "o.ass")
	os.WriteFile(src, []byte(assRitardo), 0o644)
	clips := []Clip{{Voice: "v.wav", VoiceAt: 0.5}, {Voice: "v.wav", VoiceAt: 0}}
	if !voiceDelayed(clips) {
		t.Fatal("voiceDelayed false")
	}
	// clip 2 inizia a 2 s
	if err := shiftSubtitles(src, dst, []float64{0, 2}, clips); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if !strings.Contains(string(got), "Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,uno, con virgola") {
		t.Errorf("riga 1 non spostata di 0,5:\n%s", got)
	}
	if !strings.Contains(string(got), "Dialogue: 0,0:00:02.10,0:00:03.00,") {
		t.Errorf("riga 2 (clip senza ritardo) toccata:\n%s", got)
	}
	if orig, _ := os.ReadFile(src); string(orig) != assRitardo {
		t.Errorf("file utente modificato")
	}
}

// caso 2: senza ritardi non si sposta nulla.
func TestVoiceDelayedSenzaRitardo(t *testing.T) {
	if voiceDelayed([]Clip{{Voice: "v.wav"}, {Path: "x"}}) {
		t.Errorf("voiceDelayed true senza ritardi")
	}
}
