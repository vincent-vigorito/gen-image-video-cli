// Package montage monta le clip di uno storyboard con ffmpeg e legge durata e
// formato dei file con ffprobe. Il piano di montaggio è proprio del pacchetto,
// così non dipende dai tipi dello storyboard.
//
// Regole di sicurezza: ffmpeg e ffprobe si lanciano con gli argomenti come
// lista, mai tramite shell; ogni file passa come "file:" più il percorso
// assoluto, quindi un nome che inizia con '-' non diventa un'opzione e un nome
// come "http:..." non diventa un protocollo; gli ingressi leggono solo file
// locali e solo dai formati ammessi (niente playlist che aprono altri file o la
// rete); il percorso dei sottotitoli, l'unico che entra nella stringa dei
// filtri, è protetto con i due livelli di escape di ffmpeg.
package montage

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrInvalid marca gli errori del piano (valori, file mancanti o non adatti):
// per la CLI sono errori d'uso.
var ErrInvalid = errors.New("piano di montaggio non valido")

// Tipi di transizione fra due clip.
const (
	Cut       = "cut"       // taglio netto
	Fade      = "fade"      // dissolvenza incrociata
	Dissolve  = "dissolve"  // dissolvenza a puntini
	FadeBlack = "fadeblack" // dissolvenza passando dal nero
	FadeWhite = "fadewhite" // dissolvenza passando dal bianco
)

// Modi di adattare una clip alla risoluzione finale.
const (
	FitPad  = "pad"  // intera, con bande nere (default)
	FitCrop = "crop" // riempie il quadro tagliando i bordi
)

// Plan è il piano di montaggio.
type Plan struct {
	Width  int    // larghezza finale in pixel, pari
	Height int    // altezza finale in pixel, pari
	FPS    int    // fotogrammi al secondo dell'uscita
	Fit    string // FitPad (default) o FitCrop
	Clips  []Clip // in ordine di montaggio, almeno una
	Music  *Music // musica sotto tutto il montato, facoltativa
	// Subtitles è il file dei sottotitoli da incidere (.ass, .ssa o .srt),
	// facoltativo. Args accetta solo .ass e .ssa: un .srt lo converte Montage.
	// In Montage le righe che iniziano durante una clip con la voce ritardata
	// (VoiceAt, fino all'inizio della clip successiva) si spostano dello
	// stesso tempo, in una copia: il file non cambia.
	Subtitles string
	Output    string // file finale .mp4
	// Root è la cartella che deve contenere Output, facoltativa. Con Root la
	// cartella d'uscita si crea e il file si pubblica solo con operazioni
	// vincolate a Root (os.Root): un link o una cartella sostituita durante il
	// montaggio non portano la scrittura fuori. Senza Root il vincolo è la
	// cartella di Output, aperta una volta al momento della pubblicazione.
	Root string
	// Overwrite false: se Output esiste già il montaggio si rifiuta prima di
	// partire. true: il file esistente viene sostituito solo a montaggio riuscito.
	Overwrite bool
}

// Clip è una scena del montato.
type Clip struct {
	Path  string // video, oppure immagine se Image
	Image bool   // fermo immagine (.png, .jpg, .jpeg, .webp): Duration obbligatoria
	// Duration è la durata della scena nel montato, in secondi: una clip più
	// lunga viene tagliata, una più corta tiene fermo l'ultimo fotogramma.
	// 0 vale solo per i video e solo in Montage, che usa la durata del file.
	Duration  float64
	KeepAudio bool    // usa l'audio della clip; altrimenti la scena è muta
	Voice     string  // file della voce della scena, facoltativo
	VoiceAt   float64 // inizio della voce, in secondi dall'inizio della scena
	// Transition è il passaggio dalla clip precedente; sulla prima si ignora.
	Transition Transition
}

// Transition è il passaggio fra due clip.
type Transition struct {
	Kind     string  // "" o Cut: taglio; Fade, Dissolve, FadeBlack, FadeWhite: dissolvenza
	Duration float64 // secondi di sovrapposizione delle dissolvenze (accorciano il totale)
}

// Music è la musica sotto il montato, ripetuta se più corta.
type Music struct {
	Path   string
	Volume float64 // moltiplicatore del volume, da 0 a 4; 0 vale 1
}

const (
	maxSide    = 7680
	maxFPS     = 120
	maxClips   = 500
	maxVolume  = 4
	sampleRate = 48000
)

var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// IsFade dice se la transizione è una dissolvenza.
func (t Transition) IsFade() bool { return t.Kind != "" && t.Kind != Cut }

func (t Transition) check() error {
	switch t.Kind {
	case "", Cut:
		return nil
	case Fade, Dissolve, FadeBlack, FadeWhite:
		if !finite(t.Duration) || t.Duration <= 0 {
			return fmt.Errorf("la dissolvenza vuole una durata maggiore di 0, non %v", t.Duration)
		}
		return nil
	}
	return fmt.Errorf("transizione sconosciuta %q (valide: %s, %s, %s, %s, %s)", t.Kind, Cut, Fade, Dissolve, FadeBlack, FadeWhite)
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// checkStatic controlla i valori che non dipendono dai file; con durations
// richiede anche le durate delle clip.
func (p Plan) checkStatic(durations bool) error {
	if p.Width < 16 || p.Width > maxSide || p.Width%2 != 0 || p.Height < 16 || p.Height > maxSide || p.Height%2 != 0 {
		return invalid("risoluzione %dx%d: larghezza e altezza pari, da 16 a %d", p.Width, p.Height, maxSide)
	}
	if p.FPS < 1 || p.FPS > maxFPS {
		return invalid("fps %d: da 1 a %d", p.FPS, maxFPS)
	}
	if p.Fit != "" && p.Fit != FitPad && p.Fit != FitCrop {
		return invalid("adattamento %q: valori ammessi %s, %s", p.Fit, FitPad, FitCrop)
	}
	if len(p.Clips) == 0 {
		return invalid("nessuna clip")
	}
	if len(p.Clips) > maxClips {
		return invalid("%d clip: al massimo %d", len(p.Clips), maxClips)
	}
	for i, c := range p.Clips {
		if err := checkPath(c.Path); err != nil {
			return invalid("clip %d: %v", i+1, err)
		}
		if c.Image && !imageExt[strings.ToLower(filepath.Ext(c.Path))] {
			return invalid("clip %d: immagine %s: estensioni ammesse .png, .jpg, .jpeg, .webp", i+1, c.Path)
		}
		if !finite(c.Duration) || c.Duration < 0 {
			return invalid("clip %d: durata %v", i+1, c.Duration)
		}
		if c.Duration == 0 && (durations || c.Image) {
			return invalid("clip %d: manca la durata", i+1)
		}
		if c.Image && c.KeepAudio {
			return invalid("clip %d: un'immagine non ha audio da tenere", i+1)
		}
		if c.Voice != "" {
			if err := checkPath(c.Voice); err != nil {
				return invalid("clip %d: voce: %v", i+1, err)
			}
		}
		if !finite(c.VoiceAt) || c.VoiceAt < 0 {
			return invalid("clip %d: inizio della voce %v", i+1, c.VoiceAt)
		}
		if i > 0 {
			if err := c.Transition.check(); err != nil {
				return invalid("clip %d: %v", i+1, err)
			}
		}
	}
	if p.Music != nil {
		if err := checkPath(p.Music.Path); err != nil {
			return invalid("musica: %v", err)
		}
		if !finite(p.Music.Volume) || p.Music.Volume < 0 || p.Music.Volume > maxVolume {
			return invalid("volume della musica %v: da 0 a %d", p.Music.Volume, maxVolume)
		}
	}
	if p.Subtitles != "" {
		if err := checkPath(p.Subtitles); err != nil {
			return invalid("sottotitoli: %v", err)
		}
		switch strings.ToLower(filepath.Ext(p.Subtitles)) {
		case ".ass", ".ssa", ".srt":
		default:
			return invalid("sottotitoli %s: estensioni ammesse .ass, .ssa, .srt", p.Subtitles)
		}
	}
	if err := checkPath(p.Output); err != nil {
		return invalid("uscita: %v", err)
	}
	if strings.ToLower(filepath.Ext(p.Output)) != ".mp4" {
		return invalid("uscita %s: il file finale è un .mp4", p.Output)
	}
	out, err := filepath.Abs(p.Output)
	if err != nil {
		return invalid("uscita: %v", err)
	}
	if p.Root != "" {
		if err := checkPath(p.Root); err != nil {
			return invalid("cartella d'uscita: %v", err)
		}
		if _, err := relInside(p.Root, p.Output); err != nil {
			return invalid("uscita %s: %v", p.Output, err)
		}
	}
	for _, in := range p.inputs() {
		if a, err := filepath.Abs(in); err == nil && a == out {
			return invalid("l'uscita %s è anche un file d'ingresso", p.Output)
		}
	}
	return nil
}

// inputs elenca tutti i file letti dal montaggio.
func (p Plan) inputs() []string {
	var in []string
	for _, c := range p.Clips {
		in = append(in, c.Path)
		if c.Voice != "" {
			in = append(in, c.Voice)
		}
	}
	if p.Music != nil {
		in = append(in, p.Music.Path)
	}
	if p.Subtitles != "" {
		in = append(in, p.Subtitles)
	}
	return in
}

// checkPath rifiuta percorsi vuoti e con caratteri di controllo (a capo, NUL…).
func checkPath(p string) error {
	if p == "" {
		return errors.New("percorso vuoto")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("percorso %q con caratteri di controllo", p)
		}
	}
	return nil
}

// relInside è il percorso di out relativo a root; errore se, a lettera,
// esce da root o coincide con root.
func relInside(root, out string) (string, error) {
	r, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	o, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(r, o)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("fuori dalla cartella %s", root)
	}
	return rel, nil
}

// fadeIn è la durata della dissolvenza che porta alla clip i (0 se taglio).
func (p Plan) fadeIn(i int) float64 {
	if i == 0 || !p.Clips[i].Transition.IsFade() {
		return 0
	}
	return p.Clips[i].Transition.Duration
}

// Timeline restituisce l'istante d'inizio di ogni clip nel montato e la durata
// totale: le dissolvenze sovrappongono due clip e accorciano il totale della
// loro durata. Richiede le durate di tutte le clip.
func Timeline(p Plan) (starts []float64, total float64, err error) {
	if err := p.checkStatic(true); err != nil {
		return nil, 0, err
	}
	n := len(p.Clips)
	starts = make([]float64, n)
	for i := range p.Clips {
		in, out := p.fadeIn(i), 0.0
		if i+1 < n {
			out = p.fadeIn(i + 1)
		}
		if in+out >= p.Clips[i].Duration {
			return nil, 0, invalid("clip %d: dura %s s, troppo poco per le dissolvenze (%s s in entrata, %s s in uscita)",
				i+1, num(p.Clips[i].Duration), num(in), num(out))
		}
		if i > 0 {
			starts[i] = starts[i-1] + p.Clips[i-1].Duration - in
		}
	}
	total = starts[n-1] + p.Clips[n-1].Duration
	return starts, total, nil
}

// num scrive un numero per ffmpeg: punto decimale, al più 6 decimali.
func num(f float64) string {
	return strconv.FormatFloat(math.Round(f*1e6)/1e6, 'f', -1, 64)
}

// fileArg è il percorso come lo legge ffmpeg: "file:" più il percorso assoluto.
func fileArg(p string) (string, error) {
	a, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return "file:" + a, nil
}

// filterValue protegge un valore messo come opzione dentro la stringa dei
// filtri. Primo livello (opzioni del filtro): il valore fra apici, con l'apice
// chiuso, scritto come \' e riaperto. Secondo livello (grafo dei filtri): una
// barra inversa davanti a \ ' [ ] , ; così niente chiude il filtro o ne apre un
// altro.
func filterValue(s string) string {
	q := "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	var b strings.Builder
	for _, r := range q {
		if strings.ContainsRune(`\'[],;`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// formati ammessi in lettura (nomi dei demuxer di ffmpeg)
const (
	videoFormats = "mov,mp4,matroska,webm,avi,mpegts"
	audioFormats = "wav,mp3,mov,mp4,m4a,ogg,flac,aac,matroska,webm"
	probeFormats = videoFormats + "," + audioFormats + ",image2,png_pipe,jpeg_pipe,webp_pipe"
)

// inputOpts sono le opzioni messe davanti a ogni -i: solo file locali, solo i
// formati indicati.
func inputOpts(formats string) []string {
	return []string{"-protocol_whitelist", "file", "-format_whitelist", formats}
}

const audioFmt = "aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo"

// Args costruisce gli argomenti di ffmpeg (senza il nome del programma) per il
// piano, senza eseguire nulla: tutte le clip con la durata, sottotitoli solo
// .ass o .ssa. Con Overwrite false passa -n, così ffmpeg stesso non scrive su
// un file esistente.
func Args(p Plan) ([]string, error) {
	starts, total, err := Timeline(p)
	if err != nil {
		return nil, err
	}
	if p.Subtitles != "" {
		if ext := strings.ToLower(filepath.Ext(p.Subtitles)); ext != ".ass" && ext != ".ssa" {
			return nil, invalid("sottotitoli %s: Args vuole un .ass o .ssa (un .srt lo converte Montage)", p.Subtitles)
		}
	}
	args := []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "error"}
	if p.Overwrite {
		args = append(args, "-y")
	} else {
		args = append(args, "-n")
	}
	addInput := func(path string, opts ...string) error {
		f, err := fileArg(path)
		if err != nil {
			return invalid("%s: %v", path, err)
		}
		args = append(args, opts...)
		args = append(args, "-i", f)
		return nil
	}
	for i, c := range p.Clips {
		var err error
		if c.Image {
			err = addInput(c.Path, "-loop", "1", "-framerate", strconv.Itoa(p.FPS), "-t", num(c.Duration),
				"-protocol_whitelist", "file", "-f", "image2", "-pattern_type", "none")
		} else {
			err = addInput(c.Path, inputOpts(videoFormats)...)
		}
		if err != nil {
			return nil, fmt.Errorf("clip %d: %w", i+1, err)
		}
	}
	n := len(p.Clips)
	next := n
	voiceIn := make([]int, n)
	for i, c := range p.Clips {
		voiceIn[i] = -1
		if c.Voice != "" {
			if err := addInput(c.Voice, inputOpts(audioFormats)...); err != nil {
				return nil, fmt.Errorf("clip %d: voce: %w", i+1, err)
			}
			voiceIn[i] = next
			next++
		}
	}
	musicIn := -1
	if p.Music != nil {
		if err := addInput(p.Music.Path, append([]string{"-stream_loop", "-1"}, inputOpts(audioFormats)...)...); err != nil {
			return nil, fmt.Errorf("musica: %w", err)
		}
		musicIn = next
	}

	w, h, fps := p.Width, p.Height, p.FPS
	var fit string
	if p.Fit == FitCrop {
		fit = fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase:force_divisible_by=2:out_color_matrix=bt709:out_range=tv,crop=%d:%d", w, h, w, h)
	} else {
		fit = fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2:out_color_matrix=bt709:out_range=tv,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black", w, h, w, h)
	}
	var g []string
	for i, c := range p.Clips {
		d := num(c.Duration)
		// formato comune, poi durata esatta: l'ultimo fotogramma si allunga se
		// la clip è corta e il taglio toglie l'eccesso; fps alla fine, perché
		// tpad e trim perdono la frequenza che xfade vuole uguale sui due lati.
		g = append(g, fmt.Sprintf("[%d:v:0]%s,setsar=1,format=yuv420p,setpts=PTS-STARTPTS,tpad=stop_mode=clone:stop_duration=%s,trim=duration=%s,setpts=PTS-STARTPTS,fps=%d[v%d]",
			i, fit, d, d, fps, i))
		if c.KeepAudio {
			g = append(g, fmt.Sprintf("[%d:a:0]%s,apad,atrim=duration=%s,asetpts=PTS-STARTPTS[a%d]", i, audioFmt, d, i))
		} else {
			g = append(g, fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s[a%d]", sampleRate, d, i))
		}
	}
	vcur, acur := "v0", "a0"
	for i := 1; i < n; i++ {
		t := p.Clips[i].Transition
		vj, aj := fmt.Sprintf("vj%d", i), fmt.Sprintf("aj%d", i)
		if t.IsFade() {
			g = append(g, fmt.Sprintf("[%s][v%d]xfade=transition=%s:duration=%s:offset=%s[%s]", vcur, i, t.Kind, num(t.Duration), num(starts[i]), vj))
			g = append(g, fmt.Sprintf("[%s][a%d]acrossfade=d=%s[%s]", acur, i, num(t.Duration), aj))
		} else {
			g = append(g, fmt.Sprintf("[%s][v%d]concat=n=2:v=1:a=0,fps=%d[%s]", vcur, i, fps, vj))
			g = append(g, fmt.Sprintf("[%s][a%d]concat=n=2:v=0:a=1[%s]", acur, i, aj))
		}
		vcur, acur = vj, aj
	}
	mix := []string{"[" + acur + "]"}
	for i := range p.Clips {
		if voiceIn[i] < 0 {
			continue
		}
		at := int64(math.Round((starts[i] + p.Clips[i].VoiceAt) * sampleRate))
		g = append(g, fmt.Sprintf("[%d:a:0]%s,adelay=delays=%dS:all=1[voce%d]", voiceIn[i], audioFmt, at, i))
		mix = append(mix, fmt.Sprintf("[voce%d]", i))
	}
	if musicIn >= 0 {
		vol := p.Music.Volume
		if vol == 0 {
			vol = 1
		}
		g = append(g, fmt.Sprintf("[%d:a:0]%s,volume=%s,atrim=duration=%s[musica]", musicIn, audioFmt, num(vol), num(total)))
		mix = append(mix, "[musica]")
	}
	if len(mix) > 1 {
		g = append(g, fmt.Sprintf("%samix=inputs=%d:duration=first:dropout_transition=0:normalize=0[aout]", strings.Join(mix, ""), len(mix)))
		acur = "aout"
	}
	if p.Subtitles != "" {
		abs, err := filepath.Abs(p.Subtitles)
		if err != nil {
			return nil, invalid("sottotitoli: %v", err)
		}
		g = append(g, fmt.Sprintf("[%s]ass=filename=%s[vout]", vcur, filterValue(abs)))
		vcur = "vout"
	}
	out, err := fileArg(p.Output)
	if err != nil {
		return nil, invalid("uscita: %v", err)
	}
	args = append(args,
		"-filter_complex", strings.Join(g, ";"),
		"-map", "["+vcur+"]", "-map", "["+acur+"]",
		"-c:v", "libx264", "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p", "-r", strconv.Itoa(fps),
		"-color_range", "tv", "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709",
		"-c:a", "aac", "-b:a", "192k", "-ar", strconv.Itoa(sampleRate), "-ac", "2",
		"-movflags", "+faststart", "-f", "mp4", out)
	return args, nil
}

// LastFrameArgs costruisce gli argomenti di ffmpeg che scrivono in out
// (.png o .jpg) l'ultimo fotogramma della clip, senza eseguire nulla.
func LastFrameArgs(clip, out string, overwrite bool) ([]string, error) {
	if err := checkPath(clip); err != nil {
		return nil, invalid("clip: %v", err)
	}
	if err := checkPath(out); err != nil {
		return nil, invalid("uscita: %v", err)
	}
	codec := []string{"-c:v", "png"}
	switch strings.ToLower(filepath.Ext(out)) {
	case ".png":
	case ".jpg", ".jpeg":
		codec = []string{"-c:v", "mjpeg", "-q:v", "2"}
	default:
		return nil, invalid("uscita %s: il fotogramma è un .png o un .jpg", out)
	}
	in, err := fileArg(clip)
	if err != nil {
		return nil, invalid("clip: %v", err)
	}
	o, err := fileArg(out)
	if err != nil {
		return nil, invalid("uscita: %v", err)
	}
	if in == o {
		return nil, invalid("l'uscita %s è la clip stessa", out)
	}
	args := []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "error"}
	if overwrite {
		args = append(args, "-y")
	} else {
		args = append(args, "-n")
	}
	// dall'ultimo secondo, ogni fotogramma riscrive il file: resta l'ultimo
	args = append(args, "-sseof", "-1")
	args = append(args, inputOpts(videoFormats)...)
	args = append(args, "-i", in, "-map", "0:v:0", "-update", "1")
	args = append(args, codec...)
	args = append(args, "-f", "image2", o)
	return args, nil
}

// ProbeArgs costruisce gli argomenti di ffprobe per leggere durata e formato.
func ProbeArgs(path string) ([]string, error) {
	if err := checkPath(path); err != nil {
		return nil, invalid("%v", err)
	}
	f, err := fileArg(path)
	if err != nil {
		return nil, invalid("%s: %v", path, err)
	}
	args := []string{"-hide_banner", "-loglevel", "error"}
	args = append(args, inputOpts(probeFormats)...)
	args = append(args, "-show_entries",
		"format=format_name,duration:stream=index,codec_type,codec_name,width,height,avg_frame_rate,r_frame_rate,sample_rate,channels,duration",
		"-of", "json", "-i", f)
	return args, nil
}
