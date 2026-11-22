package montage

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Missaggio dell'audio (giv post mix; da riusare in giv export e story cut):
// voci fuori campo e musica sopra l'audio di base, musica abbassata sotto le
// voci (sidechaincompress), volume finale normalizzato in due passate con
// loudnorm. Valgono le regole del pacchetto: i file sono ingressi di ffmpeg
// ("file:" più il percorso assoluto, solo i formati ammessi), nel grafo dei
// filtri solo indici, etichette fisse e numeri controllati.

// Volume finale in LUFS integrati (EBU R128), nei limiti di loudnorm.
const (
	DefaultLoudness = -14.0 // convenzione dei social: nessuna fonte ufficiale
	MinLoudness     = -70.0
	MaxLoudness     = -5.0
)

// Valori fissi della normalizzazione e dell'abbassamento della musica.
const (
	loudTP  = -1.0 // picco vero massimo, dBTP
	loudLRA = 11.0 // escursione chiesta a loudnorm, LU (il suo default)
	// escursione massima chiesta nella seconda passata: loudnorm resta lineare
	// se la richiesta non è sotto quella misurata (20: il tetto dei ffmpeg vecchi)
	maxLRA = 20.0
	// la musica si comprime 8:1 quando le voci superano -30 dBFS: circa 9 dB
	// in meno sotto una voce normale, e torna su in 0,4 s
	duckFilter = "sidechaincompress=threshold=0.03:ratio=8:attack=20:release=400"
)

// Voice è una voce fuori campo: un file audio che parte At secondi dopo
// l'inizio del video (la prima traccia audio del file).
type Voice struct {
	Path string
	At   float64
}

// Mix è il missaggio dell'audio di un video.
type Mix struct {
	// Music è la musica, ripetuta e tagliata sulla durata del video, col suo
	// volume (moltiplicatore da 0 a 4, 0 vale 1); nil: nessuna musica.
	Music *Music
	// Duck abbassa la musica mentre parlano le voci (serve musica e almeno
	// una voce).
	Duck bool
	// MusicFade sono i secondi di dissolvenza in chiusura della musica, fino
	// alla durata del video; 0: nessuna.
	MusicFade float64
	// Voices sono le voci fuori campo, sommate all'audio di base; ognuna
	// parte entro la durata del video e si taglia alla fine.
	Voices []Voice
	// Loudness è il volume finale in LUFS, da MinLoudness a MaxLoudness;
	// 0: il volume resta com'è.
	Loudness float64
}

// Loudness è la misura della prima passata di loudnorm.
type Loudness struct {
	I      float64 // volume integrato, LUFS
	TP     float64 // picco vero, dBTP
	LRA    float64 // escursione, LU
	Thresh float64 // soglia, LUFS
	Offset float64 // correzione per la seconda passata, LU
}

// CheckLoudness controlla un volume finale in LUFS (da MinLoudness a MaxLoudness).
func CheckLoudness(lufs float64) error {
	if !finite(lufs) || lufs < MinLoudness || lufs > MaxLoudness {
		return invalid("volume finale %v LUFS: da %v a %v (es. %v)", lufs, MinLoudness, MaxLoudness, DefaultLoudness)
	}
	return nil
}

// Check controlla il missaggio per un video lungo total secondi.
func (m Mix) Check(total float64) error {
	if !finite(total) || total <= 0 {
		return invalid("durata del video non misurata")
	}
	if m.Music == nil && len(m.Voices) == 0 && m.Loudness == 0 {
		return invalid("niente da fare: né musica, né voce, né volume finale")
	}
	if m.Music != nil {
		if err := checkPath(m.Music.Path); err != nil {
			return invalid("musica: %v", err)
		}
		if v := m.Music.Volume; !finite(v) || v < 0 || v > maxVolume {
			return invalid("volume della musica %v: da 0 a %d", v, maxVolume)
		}
	}
	if !finite(m.MusicFade) || m.MusicFade < 0 || m.MusicFade > total {
		return invalid("dissolvenza della musica %v s: da 0 alla durata del video (%s s)", m.MusicFade, num(total))
	}
	if m.MusicFade > 0 && m.Music == nil {
		return invalid("dissolvenza della musica senza musica")
	}
	if m.Duck && (m.Music == nil || len(m.Voices) == 0) {
		return invalid("musica abbassata sotto la voce: servono la musica e almeno una voce")
	}
	if len(m.Voices) > maxClips {
		return invalid("al più %d voci", maxClips)
	}
	for i, v := range m.Voices {
		if err := checkPath(v.Path); err != nil {
			return invalid("voce %d: %v", i+1, err)
		}
		if !finite(v.At) || v.At < 0 || v.At >= total {
			return invalid("voce %d: inizio %v s fuori dal video (da 0 a meno di %s s)", i+1, v.At, num(total))
		}
	}
	if m.Loudness != 0 {
		if err := CheckLoudness(m.Loudness); err != nil {
			return err
		}
	}
	return nil
}

// mixLabel accetta un'etichetta del grafo dei filtri senza parentesi: lettere
// minuscole, cifre e '_'.
func mixLabel(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// mixBase accetta l'audio di base come "[etichetta]" o "[N:a:M]".
func mixBase(s string) bool {
	if len(s) < 3 || s[0] != '[' || s[len(s)-1] != ']' {
		return false
	}
	inner := s[1 : len(s)-1]
	if mixLabel(inner) {
		return true
	}
	p := strings.Split(inner, ":")
	if len(p) != 3 || p[1] != "a" {
		return false
	}
	for _, n := range []string{p[0], p[2]} {
		if _, err := strconv.ParseUint(n, 10, 16); err != nil {
			return false
		}
	}
	return true
}

// Filters sono le catene del grafo dei filtri (da unire con ";") che
// mescolano l'audio base con le voci e la musica di m e mettono il risultato,
// lungo total secondi, nell'etichetta out (senza parentesi). base è un audio
// del grafo ("[0:a:0]", "[aj3]"), vuoto per il silenzio; voiceIn[i] è
// l'indice dell'ingresso di m.Voices[i] e musicIn quello della musica
// (ignorato senza musica), da aprire con MusicInputOpts. Le etichette interne
// cominciano con "mix". m.Loudness non entra qui: il volume finale va dopo,
// con LoudnormFilter.
func (m Mix) Filters(base string, total float64, voiceIn []int, musicIn int, out string) ([]string, error) {
	if err := m.Check(total); err != nil {
		return nil, err
	}
	if len(voiceIn) != len(m.Voices) {
		return nil, errors.New("missaggio: indici delle voci non allineati")
	}
	if !mixLabel(out) || (base != "" && !mixBase(base)) {
		return nil, errors.New("missaggio: etichetta del grafo non valida")
	}
	if m.Music != nil && musicIn < 0 {
		return nil, errors.New("missaggio: manca l'ingresso della musica")
	}
	d := num(total)
	var g []string
	if base == "" {
		g = append(g, fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s[mixbase]", sampleRate, d))
	} else {
		g = append(g, fmt.Sprintf("%s%s,apad,atrim=duration=%s,asetpts=PTS-STARTPTS[mixbase]", base, audioFmt, d))
	}
	mix := []string{"[mixbase]"}
	var voices []string
	for i, v := range m.Voices {
		if voiceIn[i] < 0 {
			return nil, errors.New("missaggio: indice di una voce non valido")
		}
		at := int64(math.Round(v.At * sampleRate))
		g = append(g, fmt.Sprintf("[%d:a:0]%s,adelay=delays=%dS:all=1,apad,atrim=duration=%s[mixvoce%d]", voiceIn[i], audioFmt, at, d, i))
		voices = append(voices, fmt.Sprintf("[mixvoce%d]", i))
	}
	key := ""
	if len(voices) > 0 {
		bus := voices[0]
		if len(voices) > 1 {
			g = append(g, fmt.Sprintf("%samix=inputs=%d:duration=first:dropout_transition=0:normalize=0[mixvoci]", strings.Join(voices, ""), len(voices)))
			bus = "[mixvoci]"
		}
		if m.Duck {
			g = append(g, bus+"asplit=2[mixvocisu][mixchiave]")
			bus, key = "[mixvocisu]", "[mixchiave]"
		}
		mix = append(mix, bus)
	}
	if m.Music != nil {
		vol := m.Music.Volume
		if vol == 0 {
			vol = 1
		}
		c := fmt.Sprintf("[%d:a:0]%s,volume=%s,atrim=duration=%s,asetpts=PTS-STARTPTS", musicIn, audioFmt, num(vol), d)
		if m.MusicFade > 0 {
			c += fmt.Sprintf(",afade=t=out:st=%s:d=%s", num(total-m.MusicFade), num(m.MusicFade))
		}
		if key != "" {
			g = append(g, c+"[mixmusica0]", "[mixmusica0]"+key+duckFilter+"[mixmusica]")
		} else {
			g = append(g, c+"[mixmusica]")
		}
		mix = append(mix, "[mixmusica]")
	}
	if len(mix) == 1 {
		g = append(g, "[mixbase]anull["+out+"]")
	} else {
		g = append(g, fmt.Sprintf("%samix=inputs=%d:duration=first:dropout_transition=0:normalize=0[%s]", strings.Join(mix, ""), len(mix), out))
	}
	return g, nil
}

// MusicInputOpts sono le opzioni da mettere davanti al -i della musica: si
// ripete senza fine, Filters la taglia sulla durata.
func MusicInputOpts() []string { return []string{"-stream_loop", "-1"} }

// clamp porta v fra lo e hi.
func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// LoudnormFilter è il filtro loudnorm per il volume finale target (già
// controllato con CheckLoudness). Con measured nil è quello della prima
// passata, che scrive la misura sullo stderr (ParseLoudnorm); altrimenti
// quello della seconda, lineare quando la misura lo permette, seguito dal
// ritorno a 48 kHz (loudnorm lavora a 192 kHz).
func LoudnormFilter(target float64, measured *Loudness) string {
	if measured == nil {
		return fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=%s:print_format=json", num(target), num(loudTP), num(loudLRA))
	}
	m := measured
	lra := clamp(math.Ceil(m.LRA)+1, loudLRA, maxLRA)
	return fmt.Sprintf("loudnorm=I=%s:TP=%s:LRA=%s:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:offset=%s:linear=true,aresample=%d",
		num(target), num(loudTP), num(lra),
		num(clamp(m.I, -99, 0)), num(clamp(m.TP, -99, 99)), num(clamp(m.LRA, 0, 99)),
		num(clamp(m.Thresh, -99, 0)), num(clamp(m.Offset, -99, 99)), sampleRate)
}

// ParseLoudnorm legge la misura della prima passata di loudnorm dallo stderr
// di ffmpeg (l'ultimo blocco JSON con "input_i"). Un audio muto, senza volume
// misurabile, è ErrInvalid: non c'è niente da normalizzare.
func ParseLoudnorm(stderr string) (*Loudness, error) {
	i := strings.LastIndex(stderr, `"input_i"`)
	if i < 0 {
		return nil, errors.New("loudnorm: misura assente nell'uscita di ffmpeg")
	}
	start := strings.LastIndex(stderr[:i], "{")
	end := strings.Index(stderr[i:], "}")
	if start < 0 || end < 0 {
		return nil, errors.New("loudnorm: misura incompleta nell'uscita di ffmpeg")
	}
	var raw struct {
		I      string `json:"input_i"`
		TP     string `json:"input_tp"`
		LRA    string `json:"input_lra"`
		Thresh string `json:"input_thresh"`
		Offset string `json:"target_offset"`
	}
	if err := json.Unmarshal([]byte(stderr[start:i+end+1]), &raw); err != nil {
		return nil, fmt.Errorf("loudnorm: misura illeggibile: %v", err)
	}
	var vals [5]float64
	for k, s := range []string{raw.I, raw.TP, raw.LRA, raw.Thresh, raw.Offset} {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("loudnorm: valore %q non numerico", s)
		}
		vals[k] = v
	}
	l := &Loudness{I: vals[0], TP: vals[1], LRA: vals[2], Thresh: vals[3], Offset: vals[4]}
	if !finite(l.I) || l.I < MinLoudness {
		return nil, invalid("audio muto (volume %s LUFS): niente da normalizzare", strings.TrimSpace(raw.I))
	}
	for _, v := range vals[1:] {
		if !finite(v) {
			return nil, errors.New("loudnorm: misura non valida")
		}
	}
	return l, nil
}

// analysisArgs è l'inizio comune (ffArgs) delle passate di analisi: livello
// info, perché loudnorm scrive la misura sullo stderr a quel livello.
func analysisArgs() []string {
	a := ffArgs()
	for i := 0; i+1 < len(a); i++ {
		if a[i] == "-loglevel" {
			a[i+1] = "info"
		}
	}
	return a
}

// nullOut sono gli ultimi argomenti di una passata di analisi: nessun file.
func nullOut() []string { return []string{"-f", "null", "-"} }

// LoudnessArgs sono gli argomenti di ffmpeg della prima passata di loudnorm
// sulla prima traccia audio di path (video o audio), per il volume finale
// target: nessun file scritto, la misura va letta con Work.MeasureLoudness.
func LoudnessArgs(path string, target float64) ([]string, error) {
	if err := CheckLoudness(target); err != nil {
		return nil, err
	}
	in, err := inputArgs(path, videoFormats+","+audioFormats)
	if err != nil {
		return nil, err
	}
	args := append(analysisArgs(), in...)
	args = append(args, "-map", "0:a:0", "-af", LoudnormFilter(target, nil))
	return append(args, nullOut()...), nil
}

// MeasureLoudness lancia una prima passata di loudnorm (LoudnessArgs,
// MixAnalyzeArgs o un grafo che finisce con LoudnormFilter(target, nil) e
// un'uscita nulla, con analysisArgs) e ne legge la misura.
func (w *Work) MeasureLoudness(args []string) (*Loudness, error) {
	errOut, err := w.Analyze(args)
	if err != nil {
		return nil, err
	}
	return ParseLoudnorm(errOut)
}

// mixGraph sono gli ingressi (video, voci, musica, in quest'ordine) e il
// grafo del missaggio di video: l'audio mescolato è nell'etichetta mixout.
func mixGraph(video string, info *Info, m Mix) ([]string, []string, error) {
	if info == nil || info.Video == nil {
		return nil, nil, invalid("%s: nessuna traccia video", video)
	}
	if err := m.Check(info.Duration); err != nil {
		return nil, nil, err
	}
	if info.Audio == nil && m.Music == nil && len(m.Voices) == 0 {
		return nil, nil, invalid("%s: nessuna traccia audio da normalizzare", video)
	}
	in, err := inputArgs(video, videoFormats)
	if err != nil {
		return nil, nil, err
	}
	voiceIn := make([]int, len(m.Voices))
	for i, v := range m.Voices {
		a, err := inputArgs(v.Path, audioFormats)
		if err != nil {
			return nil, nil, err
		}
		in = append(in, a...)
		voiceIn[i] = i + 1
	}
	musicIn := -1
	if m.Music != nil {
		a, err := inputArgs(m.Music.Path, audioFormats, MusicInputOpts()...)
		if err != nil {
			return nil, nil, err
		}
		in = append(in, a...)
		musicIn = len(m.Voices) + 1
	}
	base := ""
	if info.Audio != nil {
		base = "[0:a:0]"
	}
	g, err := m.Filters(base, info.Duration, voiceIn, musicIn, "mixout")
	if err != nil {
		return nil, nil, err
	}
	return in, g, nil
}

// MixAnalyzeArgs sono gli argomenti di ffmpeg della prima passata di giv post
// mix: lo stesso missaggio di MixArgs misurato da loudnorm, senza scrivere
// file (serve m.Loudness).
func MixAnalyzeArgs(video string, info *Info, m Mix) ([]string, error) {
	if m.Loudness == 0 {
		return nil, invalid("misura del volume senza volume finale")
	}
	in, g, err := mixGraph(video, info, m)
	if err != nil {
		return nil, err
	}
	g = append(g, "[mixout]"+LoudnormFilter(m.Loudness, nil)+"[mixaout]")
	args := append(analysisArgs(), in...)
	args = append(args, "-filter_complex", strings.Join(g, ";"), "-map", "[mixaout]")
	return append(args, nullOut()...), nil
}

// MixArgs sono gli argomenti di ffmpeg che scrivono in out (.mp4) video col
// missaggio di m: info sono le misure di video; con m.Loudness serve measured,
// la misura della prima passata (MixAnalyzeArgs). Il video si copia se è
// H.264, altrimenti si ricodifica in H.264.
func MixArgs(video string, info *Info, m Mix, measured *Loudness, out string) ([]string, error) {
	if m.Loudness != 0 && measured == nil {
		return nil, errors.New("missaggio: manca la misura del volume (prima passata)")
	}
	in, g, err := mixGraph(video, info, m)
	if err != nil {
		return nil, err
	}
	aout := "mixout"
	if m.Loudness != 0 {
		g = append(g, "[mixout]"+LoudnormFilter(m.Loudness, measured)+"[mixaout]")
		aout = "mixaout"
	}
	vmap, vcodec := "0:v:0", []string{"-c:v", "copy"}
	if info.Video.Codec != "h264" {
		g = append(g, "[0:v:0]scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p[mixv]")
		vmap, vcodec = "[mixv]", h264Args()
	}
	o, err := mp4Out(out)
	if err != nil {
		return nil, err
	}
	args := append(ffArgs(), in...)
	args = append(args, "-filter_complex", strings.Join(g, ";"), "-map", vmap, "-map", "["+aout+"]")
	args = append(args, vcodec...)
	args = append(args, aacArgs()...)
	return append(args, o...), nil
}

// RunMix scrive in out (in Op.Build: lo staged) video col missaggio di m: con
// m.Loudness prima la passata di misura, poi quella che scrive. Restituisce
// la misura del volume prima della normalizzazione (nil senza m.Loudness).
func (w *Work) RunMix(video string, info *Info, m Mix, out string) (*Loudness, error) {
	var measured *Loudness
	if m.Loudness != 0 {
		args, err := MixAnalyzeArgs(video, info, m)
		if err != nil {
			return nil, err
		}
		if measured, err = w.MeasureLoudness(args); err != nil {
			return nil, err
		}
	}
	args, err := MixArgs(video, info, m, measured, out)
	if err != nil {
		return nil, err
	}
	return measured, w.FFmpeg(args)
}
