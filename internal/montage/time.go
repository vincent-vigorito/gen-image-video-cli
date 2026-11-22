package montage

import (
	"fmt"
	"math"
)

// Operazioni sul tempo di un video (giv post trim, speed, freeze): un file in
// ingresso, un .mp4 in uscita ricodificato come il montaggio (H.264, AAC 48
// kHz stereo); si usano solo la prima traccia video e la prima audio.

// Limiti delle operazioni sul tempo.
const (
	MinSpeed          = 0.25 // fattore di velocità più basso (4 volte più lento)
	MaxSpeed          = 4    // fattore di velocità più alto
	MaxFreezeDuration = 60   // secondi al più di un fermo immagine
)

// Trim è il taglio di un video da Start a End secondi.
type Trim struct {
	Start float64 // secondi dall'inizio, ≥ 0
	End   float64 // secondi dall'inizio, > Start e non oltre la durata
}

// Check controlla il taglio sulla durata del video (ErrInvalid).
func (t Trim) Check(duration float64) error {
	if !finite(t.Start) || !finite(t.End) {
		return invalid("--start e --end devono essere numeri")
	}
	if duration <= 0 {
		return invalid("durata del video ignota: non posso tagliarlo")
	}
	if t.Start < 0 {
		return invalid("--start %s: dev'essere 0 o più", num(t.Start))
	}
	if t.Start >= duration {
		return invalid("--start %s: il video dura %s s", num(t.Start), num(duration))
	}
	if t.End <= t.Start {
		return invalid("--end %s: dev'essere dopo --start %s", num(t.End), num(t.Start))
	}
	if t.End > duration+0.001 {
		return invalid("--end %s: il video dura %s s", num(t.End), num(duration))
	}
	return nil
}

// Args sono gli argomenti di ffmpeg del taglio di src (info: le sue misure)
// in out: lettura da Start, durata End-Start, ricodifica.
func (t Trim) Args(src string, info *Info, out string) ([]string, error) {
	if err := checkVideo(info); err != nil {
		return nil, err
	}
	if err := t.Check(info.Duration); err != nil {
		return nil, err
	}
	in, err := inputArgs(src, videoFormats, "-ss", num(t.Start))
	if err != nil {
		return nil, err
	}
	args := append(ffArgs(), in...)
	args = append(args, "-t", num(t.End-t.Start), "-map", "0:v:0")
	args = append(args, h264Args()...)
	if info.Audio != nil {
		args = append(args, "-map", "0:a:0")
		args = append(args, aacArgs()...)
	}
	return mp4File(args, out)
}

// Speed è il cambio di velocità: Factor 2 dimezza la durata, 0.5 la
// raddoppia; l'audio segue alla stessa velocità, senza cambiare tono.
type Speed struct {
	Factor float64 // da MinSpeed a MaxSpeed
}

// Check controlla il fattore (ErrInvalid).
func (s Speed) Check() error {
	if !finite(s.Factor) || s.Factor < MinSpeed || s.Factor > MaxSpeed {
		return invalid("--factor %s: serve un numero da %s a %s (2 = due volte più veloce)", num(s.Factor), num(MinSpeed), num(MaxSpeed))
	}
	return nil
}

// Args sono gli argomenti di ffmpeg del cambio di velocità di src in out:
// tempi del video divisi per Factor alla stessa frequenza dei fotogrammi,
// audio con atempo (in passi fra 0.5 e 2, che ogni ffmpeg accetta).
func (s Speed) Args(src string, info *Info, out string) ([]string, error) {
	if err := checkVideo(info); err != nil {
		return nil, err
	}
	if err := s.Check(); err != nil {
		return nil, err
	}
	in, err := inputArgs(src, videoFormats)
	if err != nil {
		return nil, err
	}
	vf := "[0:v:0]setpts=(PTS-STARTPTS)/" + num(s.Factor)
	if fps := info.Video.FPS; fps > 0 {
		vf += ",fps=" + num(fps)
	}
	graph := vf + "[v]"
	if info.Audio != nil {
		graph += ";[0:a:0]asetpts=PTS-STARTPTS," + atempo(s.Factor) + "[a]"
	}
	args := append(ffArgs(), in...)
	args = append(args, "-filter_complex", graph, "-map", "[v]")
	args = append(args, h264Args()...)
	if info.Audio != nil {
		args = append(args, "-map", "[a]")
		args = append(args, aacArgs()...)
	}
	return mp4File(args, out)
}

// atempo è la catena di filtri atempo per il fattore f: passi da 0.5 a 2.
func atempo(f float64) string {
	s := ""
	for f < 0.5 || f > 2 {
		step := 2.0
		if f < 0.5 {
			step = 0.5
		}
		s += "atempo=" + num(step) + ","
		f /= step
	}
	return s + "atempo=" + num(f)
}

// Freeze è un fermo immagine: il fotogramma al secondo At resta fermo per
// Duration secondi, poi il video riprende; l'audio fa una pausa di silenzio
// della stessa durata. Tempi arrotondati al fotogramma.
type Freeze struct {
	At       float64 // secondi dall'inizio, ≥ 0 e prima della fine
	Duration float64 // secondi, > 0 e al più MaxFreezeDuration
}

// Check controlla il fermo sulla durata del video (ErrInvalid).
func (f Freeze) Check(duration float64) error {
	if !finite(f.At) || !finite(f.Duration) {
		return invalid("--at e --duration devono essere numeri")
	}
	if f.Duration <= 0 || f.Duration > MaxFreezeDuration {
		return invalid("--duration %s: serve un numero di secondi maggiore di 0 e al più %d", num(f.Duration), MaxFreezeDuration)
	}
	if duration <= 0 {
		return invalid("durata del video ignota: non posso fermare un fotogramma")
	}
	if f.At < 0 || f.At >= duration {
		return invalid("--at %s: serve un istante da 0 a meno di %s s (la durata del video)", num(f.At), num(duration))
	}
	return nil
}

// frames sono il fotogramma da fermare e quante volte ripeterlo, alla
// frequenza fps.
func (f Freeze) frames(fps, duration float64) (start, loop int) {
	start = int(math.Floor(f.At*fps + 1e-6))
	if last := int(math.Ceil(duration*fps)) - 1; start > last && last >= 0 {
		start = last
	}
	loop = int(math.Round(f.Duration * fps))
	if loop < 1 {
		loop = 1
	}
	return start, loop
}

// Args sono gli argomenti di ffmpeg del fermo immagine di src in out: il
// fotogramma ripetuto col filtro loop (che sposta da sé i tempi dei
// fotogrammi dopo il fermo); audio tagliato nello stesso punto con il
// silenzio in mezzo.
func (f Freeze) Args(src string, info *Info, out string) ([]string, error) {
	if err := checkVideo(info); err != nil {
		return nil, err
	}
	if err := f.Check(info.Duration); err != nil {
		return nil, err
	}
	fps := info.Video.FPS
	if fps <= 0 || !finite(fps) {
		return nil, invalid("frequenza dei fotogrammi del video ignota: non posso fermare un fotogramma")
	}
	start, loop := f.frames(fps, info.Duration)
	in, err := inputArgs(src, videoFormats)
	if err != nil {
		return nil, err
	}
	graph := fmt.Sprintf("[0:v:0]fps=%s,loop=loop=%d:size=1:start=%d[v]", num(fps), loop, start)
	if info.Audio != nil {
		at, hold := float64(start)/fps, float64(loop)/fps
		graph += fmt.Sprintf(";[0:a:0]%s,asplit=2[a1][a2];[a1]atrim=end=%s,asetpts=PTS-STARTPTS[p1];"+
			"[a2]atrim=start=%s,asetpts=PTS-STARTPTS[p2];anullsrc=r=%d:cl=stereo,atrim=duration=%s,%s[z];"+
			"[p1][z][p2]concat=n=3:v=0:a=1[a]",
			audioFmt, num(at), num(at), sampleRate, num(hold), audioFmt)
	}
	args := append(ffArgs(), in...)
	args = append(args, "-filter_complex", graph, "-map", "[v]")
	args = append(args, h264Args()...)
	if info.Audio != nil {
		args = append(args, "-map", "[a]")
		args = append(args, aacArgs()...)
	}
	return mp4File(args, out)
}

// checkVideo vuole le misure di un file con una traccia video.
func checkVideo(info *Info) error {
	if info == nil || info.Video == nil {
		return invalid("nessuna traccia video")
	}
	return nil
}

// mp4File aggiunge ad args gli ultimi argomenti di un'uscita .mp4 (mp4Out).
func mp4File(args []string, out string) ([]string, error) {
	o, err := mp4Out(out)
	if err != nil {
		return nil, err
	}
	return append(args, o...), nil
}
