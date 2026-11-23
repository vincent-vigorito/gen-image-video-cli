package montage

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Controllo tecnico di un file (giv qc; da riusare in giv story run e cut e
// in giv qc --voice): misure e tracce con ffprobe, poi una sola passata di
// analisi di ffmpeg (blackdetect, freezedetect, silencedetect, loudnorm) con
// uscita nulla: nessun file scritto, nessuna rete. Il controllo avvisa e
// basta: non corregge, non rilancia, non spende. Valgono le regole del
// pacchetto: argomenti come lista, file come "file:" più il percorso
// assoluto, solo file locali dai formati ammessi, immagini lette alla lettera.

// Esiti di un controllo, dal migliore al peggiore.
const (
	QCOK      = "ok"
	QCWarning = "avviso"
	QCDefect  = "difetto"
)

// Soglie predefinite del controllo (da tarare sulle clip degli spot: si
// cambiano con QCThresholds).
const (
	DefaultBlackMinS      = 0.5   // tratto nero più corto segnalato, secondi
	DefaultFreezeMinS     = 2.0   // tratto fermo più corto segnalato, secondi
	DefaultFreezeNoiseDB  = -60.0 // differenza fra fotogrammi sotto cui l'immagine è ferma (default di freezedetect)
	DefaultSilenceMinS    = 2.0   // tratto muto più corto segnalato, secondi
	DefaultSilenceNoiseDB = -50.0 // volume sotto cui l'audio è muto
)

const (
	qcBlackPixTh   = 0.10     // pixel nero: luminanza sotto il 10% (default di blackdetect)
	qcDefectShare  = 0.5      // neri, fermi o muti per almeno metà della durata: difetto
	qcMinDurationS = 1.0      // senza durata attesa, sotto 1 s è un avviso
	qcDurationTolS = 0.2      // scarto ammesso dalla durata attesa: 0,2 s o il 2%
	qcDurationTolP = 0.02     //
	qcFPSTol       = 0.1      // scarto ammesso dagli fps attesi
	qcLoudnessTol  = 1.0      // LU di scarto ammessi dal volume atteso
	qcMaxIntervals = 50       // tratti riportati per controllo (il totale resta in value)
	qcLogTail      = 1 << 20  // coda dello stderr della passata di analisi (volume, errori)
	qcMaxLine      = 64 << 10 // righe dello stderr più lunghe ignorate (quelle dei filtri sono corte)
	qcMaxSeconds   = 86400.0  // tetto delle durate impostabili
	qcMaxAtoms     = 1024     // atomi MP4 letti cercando moov
)

// QCThresholds sono le soglie del controllo; un campo a zero vale il suo
// default (Default…).
type QCThresholds struct {
	BlackMinS      float64 `json:"black_min_s"`
	FreezeMinS     float64 `json:"freeze_min_s"`
	FreezeNoiseDB  float64 `json:"freeze_noise_db"`
	SilenceMinS    float64 `json:"silence_min_s"`
	SilenceNoiseDB float64 `json:"silence_noise_db"`
}

// WithDefaults sono le soglie coi campi a zero portati al loro default.
func (th QCThresholds) WithDefaults() QCThresholds {
	def := func(v *float64, d float64) {
		if *v == 0 {
			*v = d
		}
	}
	def(&th.BlackMinS, DefaultBlackMinS)
	def(&th.FreezeMinS, DefaultFreezeMinS)
	def(&th.FreezeNoiseDB, DefaultFreezeNoiseDB)
	def(&th.SilenceMinS, DefaultSilenceMinS)
	def(&th.SilenceNoiseDB, DefaultSilenceNoiseDB)
	return th
}

// Check controlla le soglie (coi default al posto degli zeri): durate più di
// 0 e al più un giorno, livelli in dB da -100 a meno di 0. Fuori: ErrInvalid.
func (th QCThresholds) Check() error {
	th = th.WithDefaults()
	for _, d := range []struct {
		name string
		v    float64
	}{{"tratto nero minimo", th.BlackMinS}, {"tratto fermo minimo", th.FreezeMinS}, {"tratto muto minimo", th.SilenceMinS}} {
		if !finite(d.v) || d.v <= 0 || d.v > qcMaxSeconds {
			return invalid("%s %v: secondi, più di 0 e al più %s", d.name, d.v, num(qcMaxSeconds))
		}
	}
	for _, d := range []struct {
		name string
		v    float64
	}{{"soglia dei tratti fermi", th.FreezeNoiseDB}, {"soglia del muto", th.SilenceNoiseDB}} {
		if !finite(d.v) || d.v < -100 || d.v >= 0 {
			return invalid("%s %v dB: da -100 a meno di 0", d.name, d.v)
		}
	}
	return nil
}

// QCOptions sono le attese e le soglie del controllo di un file. I campi a
// zero (o nil) non chiedono niente: il controllo misura e basta.
type QCOptions struct {
	ExpectDurationS float64  // durata attesa, secondi
	ExpectWidth     int      // misure attese, insieme a ExpectHeight
	ExpectHeight    int      //
	ExpectFPS       float64  // fotogrammi al secondo attesi
	ExpectAudio     *bool    // traccia audio attesa (true) o non attesa (false)
	ExpectLoudness  *float64 // volume integrato atteso, LUFS
	// Still: il file è fermo per natura (scena immagine): niente controllo
	// dei tratti fermi. Un'immagine (.png, .jpg…) lo è sempre.
	Still bool
	// StillTailS sono gli ultimi secondi fermi per natura (il cartello finale
	// di giv story cut): i tratti fermi lì dentro non contano, quelli prima sì.
	StillTailS float64
	// Profiles sono i canali con cui confrontare il file (ParseChannels):
	// un QCChannel ciascuno.
	Profiles   []Profile
	Thresholds QCThresholds
}

// Check controlla attese e soglie: valori fuori dai limiti sono ErrInvalid.
func (o QCOptions) Check() error {
	if !finite(o.ExpectDurationS) || o.ExpectDurationS < 0 || o.ExpectDurationS > qcMaxSeconds {
		return invalid("durata attesa %v: secondi, più di 0 e al più %s", o.ExpectDurationS, num(qcMaxSeconds))
	}
	if (o.ExpectWidth == 0) != (o.ExpectHeight == 0) || o.ExpectWidth < 0 || o.ExpectHeight < 0 || o.ExpectWidth > maxSide || o.ExpectHeight > maxSide {
		return invalid("misure attese %dx%d: larghezza e altezza da 1 a %d", o.ExpectWidth, o.ExpectHeight, maxSide)
	}
	if !finite(o.ExpectFPS) || o.ExpectFPS < 0 || o.ExpectFPS > maxFPS*2 {
		return invalid("fps attesi %v: più di 0 e al più %d", o.ExpectFPS, maxFPS*2)
	}
	if !finite(o.StillTailS) || o.StillTailS < 0 || o.StillTailS > qcMaxSeconds {
		return invalid("coda ferma %v: secondi, da 0 a %s", o.StillTailS, num(qcMaxSeconds))
	}
	if o.ExpectLoudness != nil {
		if err := CheckLoudness(*o.ExpectLoudness); err != nil {
			return err
		}
	}
	return o.Thresholds.Check()
}

// QCInterval è un tratto (nero, fermo o muto) in secondi dall'inizio.
type QCInterval struct {
	Start    float64 `json:"start_s"`
	End      float64 `json:"end_s"`
	Duration float64 `json:"duration_s"`
}

// QCCheck è un controllo: esito, misura, attesa e soglia, tratti trovati.
// Un controllo che non si applica al file (audio di un video muto, tratti
// fermi di un'immagine) ha Skipped e l'esito ok.
type QCCheck struct {
	Name      string       `json:"name"`
	Result    string       `json:"result"` // QCOK, QCWarning, QCDefect
	Value     any          `json:"value,omitempty"`
	Expected  any          `json:"expected,omitempty"`
	Threshold any          `json:"threshold,omitempty"`
	Unit      string       `json:"unit,omitempty"`
	Intervals []QCInterval `json:"intervals,omitempty"`
	Skipped   bool         `json:"skipped,omitempty"`
	Message   string       `json:"message,omitempty"`
}

// QCChannel è il confronto del file col profilo di un canale.
type QCChannel struct {
	Channel string    `json:"channel"`
	Aspect  string    `json:"aspect"`
	Result  string    `json:"result"`
	Checks  []QCCheck `json:"checks"`
}

// QCReport è l'esito del controllo di un file: Result è il peggiore fra i
// controlli e i canali.
type QCReport struct {
	File       string      `json:"file"`
	Result     string      `json:"result"`
	DurationS  float64     `json:"duration_s,omitempty"`
	Width      int         `json:"width,omitempty"`
	Height     int         `json:"height,omitempty"`
	FPS        float64     `json:"fps,omitempty"`
	VideoCodec string      `json:"video_codec,omitempty"`
	AudioCodec string      `json:"audio_codec,omitempty"`
	Checks     []QCCheck   `json:"checks"`
	Channels   []QCChannel `json:"channels,omitempty"`
}

// QCRank è la gravità di un esito: 0 ok, 1 avviso, 2 difetto, -1 altro.
func QCRank(result string) int {
	switch result {
	case QCOK:
		return 0
	case QCWarning:
		return 1
	case QCDefect:
		return 2
	}
	return -1
}

// QCWorst è il peggiore fra gli esiti (QCOK se non ce n'è nessuno).
func QCWorst(results ...string) string {
	worst := QCOK
	for _, r := range results {
		if QCRank(r) > QCRank(worst) {
			worst = r
		}
	}
	return worst
}

// Tally aggiunge a counts gli esiti dei controlli fatti (esclusi quelli
// saltati), dei canali compresi.
func (r *QCReport) Tally(counts map[string]int) {
	add := func(cs []QCCheck) {
		for _, c := range cs {
			if !c.Skipped {
				counts[c.Result]++
			}
		}
	}
	add(r.Checks)
	for _, ch := range r.Channels {
		add(ch.Checks)
	}
}

// Defects sono i controlli con esito difetto, dei canali compresi.
func (r *QCReport) Defects() int {
	c := map[string]int{}
	r.Tally(c)
	return c[QCDefect]
}

// QCArgs sono gli argomenti di ffmpeg della passata di analisi di path
// (info: le sue misure, da Probe): sulla prima traccia video blackdetect e,
// se il file non è fermo per natura, freezedetect; sulla prima traccia audio
// silencedetect e la prima passata di loudnorm. Uscita nulla, i risultati
// sullo stderr (livello info).
func QCArgs(path string, info *Info, o QCOptions) ([]string, error) {
	if info == nil || (info.Video == nil && info.Audio == nil) {
		return nil, invalid("%s: nessuna traccia video o audio da controllare", path)
	}
	th := o.Thresholds.WithDefaults()
	in, err := inputArgs(path, videoFormats+","+audioFormats)
	if err != nil {
		return nil, err
	}
	args := append(analysisArgs(), in...)
	if info.Video != nil {
		vf := fmt.Sprintf("blackdetect=d=%s:pix_th=%s", num(th.BlackMinS), num(qcBlackPixTh))
		if !o.Still {
			vf += fmt.Sprintf(",freezedetect=n=%sdB:d=%s", num(th.FreezeNoiseDB), num(th.FreezeMinS))
		}
		args = append(args, "-map", "0:v:0", "-vf", vf)
	}
	if info.Audio != nil {
		af := fmt.Sprintf("silencedetect=n=%sdB:d=%s,%s", num(th.SilenceNoiseDB), num(th.SilenceMinS), LoudnormFilter(DefaultLoudness, nil))
		args = append(args, "-map", "0:a:0", "-af", af)
	}
	return append(args, nullOut()...), nil
}

// qcStreams sono le tracce del file per tipo.
type qcStreams struct {
	Video, Audio, Subtitle, Other, Cover int
}

// qcStreamArgs sono gli argomenti di ffprobe che elencano le tracce di path.
func qcStreamArgs(path string) ([]string, error) {
	if err := checkPath(path); err != nil {
		return nil, invalid("%v", err)
	}
	f, err := fileArg(path)
	if err != nil {
		return nil, invalid("%s: %v", path, err)
	}
	args := []string{"-hide_banner", "-loglevel", "error"}
	args = append(args, readOpts(path, probeFormats)...)
	return append(args, "-show_entries", "stream=codec_type:stream_disposition=attached_pic", "-of", "json", "-i", f), nil
}

// streams conta le tracce di path con ffprobe.
func (t Tool) streams(ctx context.Context, path string) (qcStreams, error) {
	var n qcStreams
	args, err := qcStreamArgs(path)
	if err != nil {
		return n, err
	}
	bin, err := t.bin("ffprobe", t.FFprobe)
	if err != nil {
		return n, err
	}
	out, err := run(ctx, bin, t.probeTimeout(), args)
	if err != nil {
		return n, fmt.Errorf("%s: %w", path, err)
	}
	var raw struct {
		Streams []struct {
			CodecType   string `json:"codec_type"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return n, fmt.Errorf("%s: risposta di ffprobe illeggibile: %v", path, err)
	}
	for _, s := range raw.Streams {
		switch {
		case s.CodecType == "video" && s.Disposition.AttachedPic != 0:
			n.Cover++
		case s.CodecType == "video":
			n.Video++
		case s.CodecType == "audio":
			n.Audio++
		case s.CodecType == "subtitle":
			n.Subtitle++
		default:
			n.Other++
		}
	}
	return n, nil
}

// qcAnalysis sono i risultati della passata di analisi.
type qcAnalysis struct {
	Black, Freeze, Silence []QCInterval
	Loudness               *Loudness // nil: non misurato (LoudErr)
	Mute                   bool      // audio muto: volume non misurabile
	LoudErr                error
}

// qcFilterLine divide una riga dello stderr di ffmpeg scritta da un filtro
// ("[blackdetect @ 0x…] black_start:0 …") in nome del filtro e testo; le
// righe che non cominciano così (metadati del file, che ffmpeg rientra, e
// tutto il resto) danno un nome vuoto.
func qcFilterLine(line string) (name, rest string) {
	if !strings.HasPrefix(line, "[") {
		return "", ""
	}
	at := strings.Index(line, " @ 0x")
	end := strings.Index(line, "] ")
	if at < 1 || end < at {
		return "", ""
	}
	return line[1:at], line[end+2:]
}

// qcField è il numero che segue key in s ("black_end:1.5", "silence_end: 2 | …").
func qcField(s, key string) (float64, bool) {
	i := strings.Index(s, key)
	if i < 0 {
		return 0, false
	}
	v := strings.TrimSpace(s[i+len(key):])
	if j := strings.IndexAny(v, " |"); j >= 0 {
		v = v[:j]
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || !finite(f) {
		return 0, false
	}
	return f, true
}

// qcSpan è il tratto da start a end entro la durata (0: ignota); false se vuoto.
func qcSpan(start, end, duration float64) (QCInterval, bool) {
	start = math.Max(start, 0)
	if duration > 0 {
		end = math.Min(end, duration)
	}
	if end <= start {
		return QCInterval{}, false
	}
	return QCInterval{Start: qcRound(start, 3), End: qcRound(end, 3), Duration: qcRound(end-start, 3)}, true
}

// qcParser legge lo stderr della passata di analisi (QCArgs) riga per riga,
// man mano che arriva: i tratti neri, fermi e muti si contano tutti, anche
// quando l'uscita supera la coda tenuta per il volume (qcLogTail).
type qcParser struct {
	duration        float64
	a               qcAnalysis
	freeze, silence float64 // inizio del tratto aperto; -1: nessuno
	line            []byte  // riga in corso
	skip            bool    // riga oltre qcMaxLine: ignorata fino all'a capo
}

// newQCParser è il lettore per un file lungo duration secondi (0: ignota).
func newQCParser(duration float64) *qcParser {
	return &qcParser{duration: duration, freeze: -1, silence: -1}
}

// Write divide b in righe (a capo o ritorno carrello) e le legge.
func (p *qcParser) Write(b []byte) (int, error) {
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexAny(b, "\r\n")
		chunk := b
		if i >= 0 {
			chunk = b[:i]
		}
		if !p.skip {
			if len(p.line)+len(chunk) > qcMaxLine {
				p.skip, p.line = true, p.line[:0]
			} else {
				p.line = append(p.line, chunk...)
			}
		}
		if i < 0 {
			break
		}
		if !p.skip {
			p.parse(string(p.line))
		}
		p.line, p.skip = p.line[:0], false
		b = b[i+1:]
	}
	return n, nil
}

// add aggiunge a list il tratto da start a end, se non è vuoto.
func (p *qcParser) add(list *[]QCInterval, start, end float64) {
	if iv, ok := qcSpan(start, end, p.duration); ok {
		*list = append(*list, iv)
	}
}

// parse legge una riga.
func (p *qcParser) parse(line string) {
	name, rest := qcFilterLine(line)
	switch name {
	case "blackdetect":
		s, ok1 := qcField(rest, "black_start:")
		e, ok2 := qcField(rest, "black_end:")
		if ok1 && ok2 {
			p.add(&p.a.Black, s, e)
		}
	case "freezedetect":
		if v, ok := qcField(rest, "freeze_start:"); ok {
			p.freeze = v
		} else if v, ok := qcField(rest, "freeze_end:"); ok && p.freeze >= 0 {
			p.add(&p.a.Freeze, p.freeze, v)
			p.freeze = -1
		}
	case "silencedetect":
		if v, ok := qcField(rest, "silence_start:"); ok {
			p.silence = math.Max(v, 0)
		} else if v, ok := qcField(rest, "silence_end:"); ok && p.silence >= 0 {
			p.add(&p.a.Silence, p.silence, v)
			p.silence = -1
		}
	}
}

// finish chiude la lettura: l'ultima riga, i tratti ancora aperti alla fine
// del file (chiusi alla durata) e, con l'audio, il volume letto da tail (la
// coda dello stderr, dove loudnorm scrive la misura).
func (p *qcParser) finish(tail string, audio bool) qcAnalysis {
	if len(p.line) > 0 && !p.skip {
		p.parse(string(p.line))
	}
	p.line, p.skip = p.line[:0], false
	if p.freeze >= 0 && p.duration > 0 {
		p.add(&p.a.Freeze, p.freeze, p.duration)
	}
	if p.silence >= 0 && p.duration > 0 {
		p.add(&p.a.Silence, p.silence, p.duration)
	}
	p.freeze, p.silence = -1, -1
	a := p.a
	sortIntervals(a.Black)
	if audio {
		a.Loudness, a.LoudErr = ParseLoudnorm(tail)
		a.Mute = errors.Is(a.LoudErr, ErrInvalid)
	}
	return a
}

// parseQCLog legge dallo stderr completo della passata di analisi (QCArgs)
// i tratti neri, fermi e muti e il volume (qcParser); un tratto ancora aperto
// alla fine del file si chiude alla durata.
func parseQCLog(log string, duration float64, audio bool) qcAnalysis {
	p := newQCParser(duration)
	p.Write([]byte(log))
	return p.finish(log, audio)
}

// qcSubtract è a senza i tratti di b (ordinati), tenendo i pezzi lunghi
// almeno min: i tratti fermi senza quelli neri.
func qcSubtract(a, b []QCInterval, min float64) []QCInterval {
	var out []QCInterval
	for _, iv := range a {
		start := iv.Start
		for _, cut := range b {
			if cut.End <= start || cut.Start >= iv.End {
				continue
			}
			if cut.Start-start >= min-1e-9 {
				out = append(out, QCInterval{Start: start, End: cut.Start, Duration: qcRound(cut.Start-start, 3)})
			}
			start = math.Max(start, cut.End)
		}
		if iv.End-start >= min-1e-9 {
			out = append(out, QCInterval{Start: start, End: iv.End, Duration: qcRound(iv.End-start, 3)})
		}
	}
	return out
}

// QC controlla il file path: misure e tracce con ffprobe (info, se già
// lette con Probe; nil: le legge), poi, salvo per le immagini, la passata
// di analisi. Attese e soglie fuori dai limiti, file mancanti o illeggibili
// sono ErrInvalid; ffmpeg o ffprobe assenti ErrMissingTool. Nessun file
// scritto.
func (t Tool) QC(ctx context.Context, path string, info *Info, o QCOptions) (*QCReport, error) {
	if err := o.Check(); err != nil {
		return nil, err
	}
	if err := t.Check(); err != nil {
		return nil, err
	}
	if info == nil {
		var err error
		if info, err = t.Probe(ctx, path); err != nil {
			return nil, err
		}
	}
	if info.Video == nil && info.Audio == nil {
		return nil, invalid("%s: nessuna traccia video o audio da controllare", path)
	}
	streams, err := t.streams(ctx, path)
	if err != nil {
		return nil, err
	}
	image := imagePath(path)
	var a qcAnalysis
	if !image {
		args, err := QCArgs(path, info, o)
		if err != nil {
			return nil, err
		}
		bin, err := t.bin("ffmpeg", t.FFmpeg)
		if err != nil {
			return nil, err
		}
		// i tratti si leggono mentre ffmpeg scrive: la coda serve solo al volume
		p := newQCParser(info.Duration)
		_, stderr, err := runTailTo(ctx, bin, t.timeout(), args, qcLogTail, p)
		if err != nil {
			return nil, fmt.Errorf("%s: analisi: %w", path, err)
		}
		a = p.finish(string(stderr), info.Audio != nil)
	}
	r := &QCReport{File: path, DurationS: qcRound(info.Duration, 3)}
	if v := info.Video; v != nil {
		r.Width, r.Height, r.FPS, r.VideoCodec = v.Width, v.Height, qcRound(v.FPS, 3), v.Codec
	}
	if au := info.Audio; au != nil {
		r.AudioCodec = au.Codec
	}
	q := qcFile{info: info, image: image, still: image || o.Still, a: &a, o: o, th: o.Thresholds.WithDefaults()}
	r.Checks = []QCCheck{q.duration(), q.size(), q.fps(), q.audio(), q.black(), q.freeze(), q.silence(), q.loudness(), q.peak(), qcStreamCheck(streams)}
	results := []string{}
	for _, c := range r.Checks {
		results = append(results, c.Result)
	}
	if len(o.Profiles) > 0 {
		fast, fastErr := qcMoovFirst(path, info)
		for _, p := range o.Profiles {
			ch := q.channel(p, fast, fastErr)
			r.Channels = append(r.Channels, ch)
			results = append(results, ch.Result)
		}
	}
	r.Result = QCWorst(results...)
	return r, nil
}

// qcFile è un file misurato, per i singoli controlli.
type qcFile struct {
	info  *Info
	image bool // immagine: solo misure e tracce
	still bool // fermo per natura: niente tratti fermi
	a     *qcAnalysis
	o     QCOptions
	th    QCThresholds
}

func qcRound(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}

func skipped(name, msg string) QCCheck {
	return QCCheck{Name: name, Result: QCOK, Skipped: true, Message: msg}
}

func (q qcFile) duration() QCCheck {
	if q.image {
		return skipped("duration", "immagine: nessuna durata")
	}
	c := QCCheck{Name: "duration", Result: QCOK, Unit: "s"}
	d := qcRound(q.info.Duration, 3)
	c.Value = d
	switch e := q.o.ExpectDurationS; {
	case d <= 0:
		c.Result, c.Message = QCDefect, "durata non misurata"
	case e > 0:
		tol := qcRound(math.Max(qcDurationTolS, qcDurationTolP*e), 3)
		c.Expected, c.Threshold = e, tol
		if d < e-tol {
			c.Result, c.Message = QCDefect, fmt.Sprintf("troppo corto: %s s invece di %s s", num(d), num(e))
		} else if d > e+tol {
			c.Result, c.Message = QCWarning, fmt.Sprintf("più lungo dell'atteso: %s s invece di %s s", num(d), num(e))
		}
	case d < qcMinDurationS:
		c.Threshold = qcMinDurationS
		c.Result, c.Message = QCWarning, fmt.Sprintf("molto corto: %s s", num(d))
	}
	return c
}

func (q qcFile) size() QCCheck {
	v := q.info.Video
	if v == nil {
		return skipped("size", "nessuna traccia video")
	}
	c := QCCheck{Name: "size", Result: QCOK, Value: fmt.Sprintf("%dx%d", v.Width, v.Height)}
	switch {
	case v.Width <= 0 || v.Height <= 0:
		c.Result, c.Message = QCDefect, "misure non lette"
	case q.o.ExpectWidth > 0:
		want := fmt.Sprintf("%dx%d", q.o.ExpectWidth, q.o.ExpectHeight)
		c.Expected = want
		if v.Width != q.o.ExpectWidth || v.Height != q.o.ExpectHeight {
			c.Result, c.Message = QCDefect, fmt.Sprintf("misure %dx%d invece di %s", v.Width, v.Height, want)
		}
	}
	return c
}

func (q qcFile) fps() QCCheck {
	v := q.info.Video
	switch {
	case v == nil:
		return skipped("fps", "nessuna traccia video")
	case q.image:
		return skipped("fps", "immagine: nessun fotogramma al secondo")
	}
	fps := qcRound(v.FPS, 3)
	c := QCCheck{Name: "fps", Result: QCOK, Value: fps, Unit: "fps"}
	switch e := q.o.ExpectFPS; {
	case fps <= 0:
		c.Result, c.Message = QCWarning, "fotogrammi al secondo non misurati"
	case e > 0:
		c.Expected, c.Threshold = e, qcFPSTol
		if math.Abs(fps-e) > qcFPSTol {
			c.Result, c.Message = QCDefect, fmt.Sprintf("%s fps invece di %s", num(fps), num(e))
		}
	}
	return c
}

func (q qcFile) audio() QCCheck {
	if q.image {
		return skipped("audio", "immagine: nessun audio")
	}
	present := q.info.Audio != nil
	c := QCCheck{Name: "audio", Result: QCOK, Value: present}
	switch e := q.o.ExpectAudio; {
	case e != nil:
		c.Expected = *e
		if present && !*e {
			c.Result, c.Message = QCDefect, "traccia audio presente, attesa nessuna"
		} else if !present && *e {
			c.Result, c.Message = QCDefect, "nessuna traccia audio, attesa"
		}
	case !present:
		c.Result, c.Message = QCWarning, "nessuna traccia audio"
	}
	return c
}

// spans è il controllo dei tratti iv (neri, fermi o muti: what) lunghi
// almeno min: avviso se ce n'è almeno uno, difetto se coprono almeno metà
// della durata.
func (q qcFile) spans(name, what string, iv []QCInterval, min float64) QCCheck {
	c := QCCheck{Name: name, Result: QCOK, Unit: "s", Threshold: min}
	total := 0.0
	for _, i := range iv {
		total += i.Duration
	}
	c.Value = qcRound(total, 3)
	if len(iv) == 0 {
		return c
	}
	c.Result = QCWarning
	share := 0.0
	if d := q.info.Duration; d > 0 {
		share = total / d
	}
	if share >= qcDefectShare-1e-9 {
		c.Result = QCDefect
	}
	tratti := "tratti"
	if len(iv) == 1 {
		tratti = "tratto"
	}
	c.Message = fmt.Sprintf("%s per %s s (%d%% della durata), %d %s da almeno %s s", what, num(qcRound(total, 3)), int(math.Round(share*100)), len(iv), tratti, num(min))
	if len(iv) > qcMaxIntervals {
		c.Message += fmt.Sprintf("; elencati i primi %d", qcMaxIntervals)
		iv = iv[:qcMaxIntervals]
	}
	c.Intervals = iv
	return c
}

func (q qcFile) black() QCCheck {
	switch {
	case q.info.Video == nil:
		return skipped("black", "nessuna traccia video")
	case q.image:
		return skipped("black", "immagine")
	}
	return q.spans("black", "nero", q.a.Black, q.th.BlackMinS)
}

func (q qcFile) freeze() QCCheck {
	switch {
	case q.info.Video == nil:
		return skipped("freeze", "nessuna traccia video")
	case q.still:
		return skipped("freeze", "immagine: ferma per natura")
	}
	iv := qcSubtract(q.a.Freeze, q.a.Black, q.th.FreezeMinS)
	tail := q.o.StillTailS
	if tail > 0 {
		// la coda ferma voluta (cartello finale) va fino in fondo al file
		iv = qcSubtract(iv, []QCInterval{{Start: math.Max(0, q.info.Duration-tail), End: math.Inf(1)}}, q.th.FreezeMinS)
	}
	c := q.spans("freeze", "fermo", iv, q.th.FreezeMinS)
	if tail > 0 {
		note := fmt.Sprintf("esclusi gli ultimi %s s (cartello finale)", num(tail))
		if c.Message == "" {
			c.Message = note
		} else {
			c.Message += "; " + note
		}
	}
	return c
}

func (q qcFile) silence() QCCheck {
	switch {
	case q.image:
		return skipped("silence", "immagine: nessun audio")
	case q.info.Audio == nil:
		return skipped("silence", "nessuna traccia audio")
	}
	return q.spans("silence", "muto", q.a.Silence, q.th.SilenceMinS)
}

// loudnessCheck è il volume integrato contro want (nil: nessuna attesa).
func (q qcFile) loudnessCheck(want *float64, hint string) QCCheck {
	switch {
	case q.image:
		return skipped("loudness", "immagine: nessun audio")
	case q.info.Audio == nil:
		return skipped("loudness", "nessuna traccia audio")
	}
	c := QCCheck{Name: "loudness", Result: QCOK, Unit: "LUFS"}
	switch {
	case q.a.Mute:
		c.Result, c.Message = QCWarning, "audio muto: volume non misurabile"
		return c
	case q.a.Loudness == nil:
		msg := "volume non misurato"
		if q.a.LoudErr != nil {
			msg += ": " + q.a.LoudErr.Error()
		}
		c.Result, c.Message = QCWarning, msg
		return c
	}
	i := qcRound(q.a.Loudness.I, 2)
	c.Value = i
	if want != nil {
		c.Expected, c.Threshold = *want, qcLoudnessTol
		if math.Abs(i-*want) > qcLoudnessTol+1e-9 {
			c.Result, c.Message = QCWarning, fmt.Sprintf("volume %s LUFS invece di %s%s", num(i), num(*want), hint)
		}
	}
	return c
}

func (q qcFile) loudness() QCCheck { return q.loudnessCheck(q.o.ExpectLoudness, "") }

func (q qcFile) peak() QCCheck {
	switch {
	case q.image:
		return skipped("peak", "immagine: nessun audio")
	case q.info.Audio == nil:
		return skipped("peak", "nessuna traccia audio")
	case q.a.Mute:
		return skipped("peak", "audio muto")
	case q.a.Loudness == nil:
		return skipped("peak", "picco non misurato")
	}
	tp := qcRound(q.a.Loudness.TP, 2)
	c := QCCheck{Name: "peak", Result: QCOK, Value: tp, Threshold: loudTP, Unit: "dBTP"}
	switch {
	case tp > 0:
		c.Result, c.Message = QCDefect, fmt.Sprintf("saturazione: picco vero %s dBTP, sopra 0", num(tp))
	case tp > loudTP+1e-9:
		c.Result, c.Message = QCWarning, fmt.Sprintf("picco vero %s dBTP, sopra %s", num(tp), num(loudTP))
	}
	return c
}

// qcStreamCheck segnala le tracce in più: un'altra traccia video o audio
// (si usa la prima), sottotitoli, dati. La copertina allegata (attached_pic,
// come nelle clip di xAI) si conta in value ma non è un avviso.
func qcStreamCheck(n qcStreams) QCCheck {
	c := QCCheck{Name: "streams", Result: QCOK,
		Value: map[string]int{"video": n.Video, "audio": n.Audio, "subtitle": n.Subtitle, "other": n.Other, "cover": n.Cover}}
	var extra []string
	if n.Video > 1 {
		extra = append(extra, fmt.Sprintf("%d tracce video (si usa la prima)", n.Video))
	}
	if n.Audio > 1 {
		extra = append(extra, fmt.Sprintf("%d tracce audio (si usa la prima)", n.Audio))
	}
	if n.Subtitle > 0 {
		extra = append(extra, fmt.Sprintf("%d di sottotitoli", n.Subtitle))
	}
	if n.Other > 0 {
		extra = append(extra, fmt.Sprintf("%d di dati o allegati", n.Other))
	}
	if len(extra) > 0 {
		c.Result, c.Message = QCWarning, "tracce in più: "+strings.Join(extra, ", ")
	}
	return c
}

// mp4Container dice se il formato letto da ffprobe è MP4 o MOV.
func mp4Container(format string) bool {
	for _, f := range strings.Split(format, ",") {
		if f == "mp4" || f == "mov" {
			return true
		}
	}
	return false
}

// qcMoovFirst dice se in un MP4 l'indice (moov) viene prima dei dati (mdat):
// l'inizio rapido dei canali. Legge solo le intestazioni degli atomi in
// cima al file; un file che non è MP4 o MOV è un errore.
func qcMoovFirst(path string, info *Info) (bool, error) {
	if !mp4Container(info.Format) {
		return false, fmt.Errorf("contenitore %s, non MP4", info.Format)
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return false, errors.New("non è un file normale")
	}
	var off int64
	hdr := make([]byte, 16)
	for range qcMaxAtoms {
		if _, err := f.ReadAt(hdr[:8], off); err != nil {
			return false, errors.New("atomi MP4 illeggibili")
		}
		size, head := int64(binary.BigEndian.Uint32(hdr[:4])), int64(8)
		if size == 1 {
			if _, err := f.ReadAt(hdr[8:16], off+8); err != nil {
				return false, errors.New("atomi MP4 illeggibili")
			}
			big := binary.BigEndian.Uint64(hdr[8:16])
			if big > math.MaxInt64 {
				return false, errors.New("atomo MP4 troppo grande")
			}
			size, head = int64(big), 16
		}
		switch string(hdr[4:8]) {
		case "moov":
			return true, nil
		case "mdat":
			return false, nil
		}
		if size == 0 || size < head || off > math.MaxInt64-size {
			break
		}
		off += size
	}
	return false, errors.New("indice moov non trovato")
}

// channel confronta il file col profilo p: misure, fps, durata, codec,
// inizio rapido (fast, fastErr: qcMoovFirst) e volume.
func (q qcFile) channel(p Profile, fast bool, fastErr error) QCChannel {
	ch := QCChannel{Channel: p.Channel, Aspect: p.Aspect.String()}
	want := fmt.Sprintf("%dx%d", p.Width, p.Height)
	v := q.info.Video

	size := QCCheck{Name: "size", Result: QCOK, Expected: want}
	switch {
	case v == nil || v.Width <= 0 || v.Height <= 0:
		size.Result, size.Message = QCDefect, "nessuna traccia video misurata"
	case v.Width == p.Width && v.Height == p.Height:
		size.Value = want
	case v.Width*p.Height == v.Height*p.Width:
		size.Value = fmt.Sprintf("%dx%d", v.Width, v.Height)
		size.Result, size.Message = QCWarning, fmt.Sprintf("formato giusto, misura diversa da %s (giv export la porta alla misura)", want)
	default:
		size.Value = fmt.Sprintf("%dx%d", v.Width, v.Height)
		size.Result, size.Message = QCDefect, fmt.Sprintf("formato diverso da %s del canale (%s)", p.Aspect, want)
	}

	fps := QCCheck{Name: "fps", Result: QCOK, Unit: "fps", Expected: map[string]int{"min": p.MinFPS, "max": p.MaxFPS}}
	switch {
	case v == nil || q.image || v.FPS <= 0:
		fps.Result, fps.Message = QCWarning, "fotogrammi al secondo non misurati"
	default:
		f := qcRound(v.FPS, 3)
		fps.Value = f
		if f < float64(p.MinFPS) || f > float64(p.MaxFPS) {
			fps.Result, fps.Message = QCDefect, fmt.Sprintf("%s fps, il canale vuole da %d a %d", num(f), p.MinFPS, p.MaxFPS)
		}
	}

	dur := QCCheck{Name: "duration", Result: QCOK, Unit: "s", Value: qcRound(q.info.Duration, 3),
		Expected: map[string]float64{"min_s": p.MinS, "max_s": p.MaxS}}
	if err := p.CheckDuration(q.info.Duration); err != nil {
		dur.Result, dur.Message = QCDefect, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ")
	}

	vc := QCCheck{Name: "video_codec", Result: QCOK, Expected: "h264"}
	if v != nil {
		vc.Value = v.Codec
	}
	if v == nil || v.Codec != "h264" {
		vc.Result, vc.Message = QCWarning, "il profilo del canale è H.264 (giv export lo converte)"
	}

	ac := QCCheck{Name: "audio_codec", Result: QCOK, Expected: "aac"}
	switch au := q.info.Audio; {
	case au == nil:
		ac = skipped("audio_codec", "nessuna traccia audio")
	case au.Codec != "aac":
		ac.Value = au.Codec
		ac.Result, ac.Message = QCWarning, "il profilo del canale è AAC (giv export lo converte)"
	default:
		ac.Value = au.Codec
	}

	fs := QCCheck{Name: "faststart", Result: QCOK, Value: fast}
	switch {
	case fastErr != nil:
		fs.Value = nil
		fs.Result, fs.Message = QCWarning, "inizio rapido non controllato: "+fastErr.Error()
	case !fast:
		fs.Result, fs.Message = QCWarning, "indice (moov) in coda: il video parte solo a scaricamento finito (giv export lo mette in testa)"
	}

	target := DefaultLoudness
	loud := q.loudnessCheck(&target, " (convenzione dei social; giv export lo normalizza)")

	ch.Checks = []QCCheck{size, fps, dur, vc, ac, fs, loud}
	results := make([]string, len(ch.Checks))
	for i, c := range ch.Checks {
		results[i] = c.Result
	}
	ch.Result = QCWorst(results...)
	return ch
}

// sortIntervals ordina i tratti per inizio (blackdetect li scrive già in
// ordine; qcSubtract li vuole ordinati).
func sortIntervals(iv []QCInterval) {
	sort.Slice(iv, func(i, j int) bool { return iv[i].Start < iv[j].Start })
}
