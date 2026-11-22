package montage

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// maxSubtitles è il tetto del file dei sottotitoli da spostare (gli .ass
// possono contenere i font).
const maxSubtitles = 64 << 20

// voiceDelayed dice se qualche clip con la voce la fa partire dopo l'inizio.
func voiceDelayed(clips []Clip) bool {
	for _, c := range clips {
		if c.Voice != "" && c.VoiceAt > 0 {
			return true
		}
	}
	return false
}

// shiftSubtitles copia in dst i sottotitoli .ass o .ssa di src spostando ogni
// riga Dialogue del VoiceAt della clip in cui inizia (l'ultima clip con
// l'inizio non oltre quello della riga); src non cambia.
func shiftSubtitles(src, dst string, starts []float64, clips []Clip) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSubtitles+1))
	if err != nil {
		return err
	}
	if len(data) > maxSubtitles {
		return invalid("sottotitoli %s: oltre %d MiB", src, maxSubtitles>>20)
	}
	startCS := make([]int64, len(starts))
	delayCS := make([]int64, len(clips))
	for i := range clips {
		startCS[i] = centis(starts[i])
		if clips[i].Voice != "" {
			delayCS[i] = centis(clips[i].VoiceAt)
		}
	}
	lines := bytes.Split(data, []byte("\n"))
	events := false
	si, ei, nf := 1, 2, 10 // ordine dei campi di default: Layer, Start, End, …, Text
	for n, raw := range lines {
		line := strings.TrimSuffix(string(raw), "\r")
		line = strings.TrimLeft(strings.TrimPrefix(line, "\ufeff"), " \t")
		head := strings.TrimSpace(line)
		if strings.HasPrefix(head, "[") {
			events = strings.EqualFold(head, "[events]")
			continue
		}
		if !events {
			continue
		}
		if rest, ok := strings.CutPrefix(head, "Format:"); ok {
			fields := strings.Split(rest, ",")
			nf, si, ei = len(fields), -1, -1
			for i, f := range fields {
				switch strings.ToLower(strings.TrimSpace(f)) {
				case "start":
					si = i
				case "end":
					ei = i
				}
			}
			if si < 0 || ei < 0 {
				return invalid("sottotitoli %s: riga %d: Format senza Start o End", src, n+1)
			}
			continue
		}
		rest, ok := strings.CutPrefix(line, "Dialogue:")
		if !ok {
			continue
		}
		fields := strings.SplitN(rest, ",", nf)
		if len(fields) <= max(si, ei) {
			return invalid("sottotitoli %s: riga %d: Dialogue con meno campi del Format", src, n+1)
		}
		start, err1 := parseASSTime(fields[si])
		end, err2 := parseASSTime(fields[ei])
		if err1 != nil || err2 != nil {
			return invalid("sottotitoli %s: riga %d: tempi non validi", src, n+1)
		}
		k := 0
		for i := range startCS {
			if startCS[i] <= start {
				k = i
			}
		}
		if delayCS[k] == 0 {
			continue
		}
		fields[si], fields[ei] = formatASSTime(start+delayCS[k]), formatASSTime(end+delayCS[k])
		out := "Dialogue:" + strings.Join(fields, ",")
		if strings.HasSuffix(string(raw), "\r") {
			out += "\r"
		}
		lines[n] = []byte(out)
	}
	return os.WriteFile(dst, bytes.Join(lines, []byte("\n")), 0o600)
}

func centis(sec float64) int64 { return int64(math.Round(sec * 100)) }

// parseASSTime legge un tempo H:MM:SS.cc in centesimi di secondo.
func parseASSTime(s string) (int64, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("tempo %q", s)
	}
	h, err1 := strconv.ParseUint(parts[0], 10, 32)
	m, err2 := strconv.ParseUint(parts[1], 10, 32)
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil || !finite(sec) || sec < 0 {
		return 0, fmt.Errorf("tempo %q", s)
	}
	return int64(h)*360000 + int64(m)*6000 + centis(sec), nil
}

func formatASSTime(cs int64) string {
	return fmt.Sprintf("%d:%02d:%02d.%02d", cs/360000, cs/6000%60, cs/100%60, cs%100)
}
