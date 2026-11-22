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
	"os"
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
	Fit    string // FitPad (default), FitCrop o FitBlur
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
	// RootDir è Root già aperta dal chiamante, facoltativa: la pubblicazione
	// usa questa invece di riaprire Root per nome (giv story cut la apre prima
	// di leggere lo storyboard e la tiene fino alla pubblicazione).
	RootDir *os.Root
	// Overwrite false: se Output esiste già il montaggio si rifiuta prima di
	// partire. true: il file esistente viene sostituito solo a montaggio riuscito.
	Overwrite bool

	// Sovrimpressioni e audio (giv story cut, 0.11.0), tutti facoltativi:
	// senza, gli argomenti sono quelli di prima. Si posano dopo il formato,
	// dentro i margini sicuri.
	Logo    *Logo    // sulle scene (To 0: fino alla fine delle scene); SrcW e SrcH li misura Montage
	Titles  []Title  // titoli, coi tempi della loro clip
	EndCard *EndCard // cartello finale accodato a taglio netto, muto sotto la musica
	Fonts   []string // file dei font dei testi (.ttf, .otf, .ttc)
	Margins *Margins // margini sicuri; nil: SafeMargins del formato
	// Duck abbassa la musica sotto le voci; MusicFade sono i secondi di
	// dissolvenza finale della musica (come Mix).
	Duck      bool
	MusicFade float64
	Loudness  float64 // volume finale in LUFS (CheckLoudness); 0: com'è

	// preparati da Montage: file .ass dei testi, cartella dei font, misura
	// della prima passata del volume
	texts, fontsDir string
	measured        *Loudness
}

// Title è un titolo del montaggio: Start ed End sono secondi dall'inizio
// della clip numero Clip (da 1), o del montato con Clip 0.
type Title struct {
	Text
	Clip int
}

// extended dice se il piano usa sovrimpressioni o audio della 0.11.0.
func (p Plan) extended() bool {
	return p.Logo != nil || len(p.Titles) > 0 || p.EndCard != nil || p.Duck || p.MusicFade != 0 || p.Loudness != 0
}

// checkExtended controlla i valori delle sovrimpressioni e dell'audio che non
// dipendono dai file (le misure del logo e i tempi li controlla Montage).
func (p Plan) checkExtended() error {
	if p.Logo != nil {
		if err := checkPath(p.Logo.Path); err != nil {
			return invalid("logo: %v", err)
		}
	}
	if c := p.EndCard; c != nil {
		if c.Image != "" {
			if err := checkPath(c.Image); err != nil {
				return invalid("cartello finale: immagine: %v", err)
			}
		}
		if c.Logo != nil {
			if err := checkPath(c.Logo.Path); err != nil {
				return invalid("cartello finale: logo: %v", err)
			}
		}
		if !finite(c.Duration) || c.Duration <= 0 || c.Duration > maxCardSeconds {
			return invalid("cartello finale: durata %v s: da 0 (escluso) a %d s", c.Duration, maxCardSeconds)
		}
	}
	for i, t := range p.Titles {
		if t.Clip < 0 || t.Clip > len(p.Clips) {
			return invalid("titolo %d: clip %d inesistente", i+1, t.Clip)
		}
		if !finite(t.Start) || !finite(t.End) || t.Start < 0 || t.End <= t.Start {
			return invalid("titolo %d: tempi da %v a %v s non validi", i+1, t.Start, t.End)
		}
		if err := checkText(fmt.Sprintf("titolo %d", i+1), t.Text.Text); err != nil {
			return err
		}
	}
	for _, f := range p.Fonts {
		if err := checkPath(f); err != nil {
			return invalid("font: %v", err)
		}
	}
	if p.Margins != nil {
		if err := p.Margins.Check(); err != nil {
			return invalid("margini: %v", err)
		}
	}
	if p.Duck && p.Music == nil {
		return invalid("musica sotto la voce senza musica")
	}
	if !finite(p.MusicFade) || p.MusicFade < 0 || (p.MusicFade > 0 && p.Music == nil) {
		return invalid("dissolvenza della musica %v s: da 0 in su, e solo con la musica", p.MusicFade)
	}
	if p.Loudness != 0 {
		if err := CheckLoudness(p.Loudness); err != nil {
			return err
		}
	}
	return nil
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
	// Taglio e velocità di un video (non delle immagini): del file si usa il
	// pezzo da TrimStart a TrimEnd secondi (TrimEnd 0: fino alla fine), a
	// velocità Speed (da MinSpeed a MaxSpeed; 0 vale 1), audio compreso; la
	// durata 0 in Montage è quella del pezzo divisa per Speed.
	TrimStart float64
	TrimEnd   float64
	Speed     float64
	// Focus è il punto da tenere quando FitCrop taglia; nil: il centro.
	Focus *Focus
}

// retimed dice se la clip ha taglio o velocità.
func (c Clip) retimed() bool {
	return c.TrimStart != 0 || c.TrimEnd != 0 || (c.Speed != 0 && c.Speed != 1)
}

// speed è la velocità effettiva della clip.
func (c Clip) speed() float64 {
	if c.Speed == 0 {
		return 1
	}
	return c.Speed
}

// checkRetime controlla taglio, velocità e fuoco della clip.
func (c Clip) checkRetime() error {
	if c.Focus != nil {
		if err := c.Focus.Check(); err != nil {
			return err
		}
	}
	if !c.retimed() {
		return nil
	}
	if c.Image {
		return errors.New("taglio e velocità valgono per i video, non per le immagini")
	}
	if !finite(c.TrimStart) || c.TrimStart < 0 {
		return fmt.Errorf("inizio del taglio %v: secondi da 0 in su", c.TrimStart)
	}
	if !finite(c.TrimEnd) || c.TrimEnd < 0 || (c.TrimEnd > 0 && c.TrimEnd <= c.TrimStart) {
		return fmt.Errorf("fine del taglio %v: secondi dopo l'inizio (%s)", c.TrimEnd, num(c.TrimStart))
	}
	if c.Speed != 0 {
		if err := (Speed{Factor: c.Speed}).Check(); err != nil {
			return errors.New(strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
		}
	}
	return nil
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
	if p.Fit != "" && p.Fit != FitPad && p.Fit != FitCrop && p.Fit != FitBlur {
		return invalid("adattamento %q: valori ammessi %s, %s, %s", p.Fit, FitPad, FitCrop, FitBlur)
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
		if err := c.checkRetime(); err != nil {
			return invalid("clip %d: %v", i+1, err)
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
	if p.extended() {
		if err := p.checkExtended(); err != nil {
			return err
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
	if p.Logo != nil {
		in = append(in, p.Logo.Path)
	}
	if c := p.EndCard; c != nil {
		if c.Image != "" {
			in = append(in, c.Image)
		}
		if c.Logo != nil {
			in = append(in, c.Logo.Path)
		}
	}
	return append(in, p.Fonts...)
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
	// imagePipes leggono un'immagine dal contenuto, sempre un file solo:
	// image2, che con un nome come foto%d.png legge una sequenza di altri
	// file, non è mai fra i formati scelti da soli (solo literalImage)
	imagePipes   = "png_pipe,jpeg_pipe,webp_pipe"
	probeFormats = videoFormats + "," + audioFormats + "," + imagePipes
)

// inputOpts sono le opzioni messe davanti a ogni -i: solo file locali, solo i
// formati indicati.
func inputOpts(formats string) []string {
	return []string{"-protocol_whitelist", "file", "-format_whitelist", formats}
}

// literalImage sono le opzioni d'ingresso di un'immagine (.png, .jpg, .jpeg,
// .webp) in ffmpeg e ffprobe: un file solo letto alla lettera, mai una
// sequenza (un nome con %d non fa leggere altri file).
func literalImage() []string {
	return []string{"-protocol_whitelist", "file", "-f", "image2", "-pattern_type", "none"}
}

// imagePath: il file ha l'estensione di un'immagine (imageExt) e si legge
// con literalImage.
func imagePath(path string) bool {
	return imageExt[strings.ToLower(filepath.Ext(path))]
}

// readOpts sono le opzioni di lettura di path coi formati ammessi, in ffmpeg
// e in ffprobe. Un'immagine si legge sempre alla lettera, mai come sequenza:
// dove le immagini sono ammesse (imageFormats, probeFormats, outputFormats)
// un file con l'estensione di un'immagine va con literalImage, gli altri solo
// coi formati che leggono il contenuto (image2 non si sceglie mai da solo).
func readOpts(path, formats string) []string {
	if !strings.Contains(formats, imagePipes) {
		return inputOpts(formats) // video e audio: nessun formato d'immagine
	}
	if imagePath(path) {
		return literalImage()
	}
	if formats == imageFormats {
		return inputOpts(imagePipes)
	}
	return inputOpts(formats)
}

const audioFmt = "aresample=48000,aformat=sample_fmts=fltp:channel_layouts=stereo"

// Args costruisce gli argomenti di ffmpeg (senza il nome del programma) per il
// piano, senza eseguire nulla: tutte le clip con la durata, sottotitoli solo
// .ass o .ssa. Con Overwrite false passa -n, così ffmpeg stesso non scrive su
// un file esistente. Con titoli, cartello o volume finale servono i file e
// la misura che prepara Montage (texts, fontsDir, measured).
func Args(p Plan) ([]string, error) {
	return buildArgs(p, false)
}

// loudnessArgs sono gli argomenti della passata di misura del volume finale:
// solo l'audio del piano, misurato da loudnorm, senza scrivere file.
func loudnessArgs(p Plan) ([]string, error) {
	if p.Loudness == 0 {
		return nil, invalid("misura del volume senza volume finale")
	}
	return buildArgs(p, true)
}

// buildArgs è Args; con analyze solo l'audio, verso loudnorm e nessun file.
func buildArgs(p Plan, analyze bool) ([]string, error) {
	starts, total, err := Timeline(p)
	if err != nil {
		return nil, err
	}
	if p.Subtitles != "" {
		if ext := strings.ToLower(filepath.Ext(p.Subtitles)); ext != ".ass" && ext != ".ssa" {
			return nil, invalid("sottotitoli %s: Args vuole un .ass o .ssa (un .srt lo converte Montage)", p.Subtitles)
		}
	}
	scenesEnd := total // fine delle scene; il cartello viene dopo
	if p.EndCard != nil {
		total += p.EndCard.Duration
	}
	if len(p.Titles) > 0 || (p.EndCard != nil && (p.EndCard.Text != "" || p.EndCard.Subtext != "")) {
		if p.texts == "" {
			return nil, invalid("titoli e testi del cartello senza il file .ass (li prepara Montage)")
		}
	}
	if p.Loudness != 0 && !analyze && p.measured == nil {
		return nil, invalid("volume finale senza la misura (la prepara Montage)")
	}
	args := []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "error"}
	if analyze {
		args = analysisArgs()
	} else if p.Overwrite {
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
			err = addInput(c.Path, append([]string{"-loop", "1", "-framerate", strconv.Itoa(p.FPS), "-t", num(c.Duration)},
				literalImage()...)...)
		} else if c.TrimStart > 0 || c.TrimEnd > 0 {
			// il pezzo della clip: -ss e -t sull'ingresso valgono per video e audio
			opts := []string{"-ss", num(c.TrimStart)}
			if c.TrimEnd > 0 {
				opts = append(opts, "-t", num(c.TrimEnd-c.TrimStart))
			}
			err = addInput(c.Path, append(opts, inputOpts(videoFormats)...)...)
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
		next++
	}
	// immagini delle sovrimpressioni: logo, sfondo e logo del cartello
	image := func(path string) (string, error) {
		if err := addInput(path, readOpts(path, imageFormats)...); err != nil {
			return "", err
		}
		next++
		return strconv.Itoa(next-1) + ":v", nil
	}
	var logoIn, cardImg, cardLogo string
	if p.Logo != nil {
		if logoIn, err = image(p.Logo.Path); err != nil {
			return nil, fmt.Errorf("logo: %w", err)
		}
	}
	if c := p.EndCard; c != nil {
		if c.Image != "" {
			if cardImg, err = image(c.Image); err != nil {
				return nil, fmt.Errorf("cartello finale: %w", err)
			}
		}
		if c.Logo != nil {
			if cardLogo, err = image(c.Logo.Path); err != nil {
				return nil, fmt.Errorf("cartello finale: logo: %w", err)
			}
		}
	}

	w, h, fps := p.Width, p.Height, p.FPS
	var fit string
	if p.Fit == FitCrop {
		fit = fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase:force_divisible_by=2:out_color_matrix=bt709:out_range=tv,crop=%d:%d", w, h, w, h)
	} else {
		fit = fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2:out_color_matrix=bt709:out_range=tv,pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black", w, h, w, h)
	}
	// catene del grafo, nell'ordine; con analyze restano solo quelle audio
	var g []string
	vid := func(s ...string) {
		if !analyze {
			g = append(g, s...)
		}
	}
	aud := func(s ...string) { g = append(g, s...) }
	for i, c := range p.Clips {
		d := num(c.Duration)
		// velocità prima del formato: tempi del video divisi per Speed
		var pre string
		if sp := c.speed(); sp != 1 {
			pre = "setpts=(PTS-STARTPTS)/" + num(sp) + ","
		}
		// formato comune, poi durata esatta: l'ultimo fotogramma si allunga se
		// la clip è corta e il taglio toglie l'eccesso; fps alla fine, perché
		// tpad e trim perdono la frequenza che xfade vuole uguale sui due lati.
		tail := fmt.Sprintf("format=yuv420p,setpts=PTS-STARTPTS,tpad=stop_mode=clone:stop_duration=%s,trim=duration=%s,setpts=PTS-STARTPTS,fps=%d[v%d]", d, d, fps, i)
		switch {
		case p.Fit == FitBlur:
			// due rami (Reframe.Graph): fondo sfocato e video intero
			in := fmt.Sprintf("%d:v:0", i)
			if pre != "" {
				vid(fmt.Sprintf("[%d:v:0]%s[s%d]", i, strings.TrimSuffix(pre, ","), i))
				in = fmt.Sprintf("s%d", i)
			}
			rg, err := Reframe{Width: w, Height: h, Fit: FitBlur}.Graph(in, fmt.Sprintf("r%d", i))
			if err != nil {
				return nil, fmt.Errorf("clip %d: %w", i+1, err)
			}
			vid(rg, fmt.Sprintf("[r%d]%s", i, tail))
		case p.Fit == FitCrop && c.Focus != nil && *c.Focus != FocusCenter:
			vid(fmt.Sprintf("[%d:v:0]%s%s,setsar=1,%s", i, pre, cropChain(w, h, *c.Focus), tail))
		default:
			vid(fmt.Sprintf("[%d:v:0]%s%s,setsar=1,%s", i, pre, fit, tail))
		}
		if c.KeepAudio {
			var tempo string
			if sp := c.speed(); sp != 1 {
				tempo = "asetpts=PTS-STARTPTS," + atempo(sp) + ","
			}
			aud(fmt.Sprintf("[%d:a:0]%s,%sapad,atrim=duration=%s,asetpts=PTS-STARTPTS[a%d]", i, audioFmt, tempo, d, i))
		} else {
			aud(fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s[a%d]", sampleRate, d, i))
		}
	}
	vcur, acur := "v0", "a0"
	for i := 1; i < n; i++ {
		t := p.Clips[i].Transition
		vj, aj := fmt.Sprintf("vj%d", i), fmt.Sprintf("aj%d", i)
		if t.IsFade() {
			vid(fmt.Sprintf("[%s][v%d]xfade=transition=%s:duration=%s:offset=%s[%s]", vcur, i, t.Kind, num(t.Duration), num(starts[i]), vj))
			aud(fmt.Sprintf("[%s][a%d]acrossfade=d=%s[%s]", acur, i, num(t.Duration), aj))
		} else {
			vid(fmt.Sprintf("[%s][v%d]concat=n=2:v=1:a=0,fps=%d[%s]", vcur, i, fps, vj))
			aud(fmt.Sprintf("[%s][a%d]concat=n=2:v=0:a=1[%s]", acur, i, aj))
		}
		vcur, acur = vj, aj
	}
	margins := SafeMargins(w, h)
	if p.Margins != nil {
		margins = *p.Margins
	}
	if c := p.EndCard; c != nil {
		// il cartello dopo l'ultima scena, a taglio netto, muto sotto la musica
		f, err := EndCardFilter(*c, cardImg, cardLogo, "vcard", w, h, float64(fps), margins)
		if err != nil {
			return nil, err
		}
		vid(f, fmt.Sprintf("[%s][vcard]concat=n=2:v=1:a=0,fps=%d[vjcard]", vcur, fps))
		aud(fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s[acard]", sampleRate, num(c.Duration)),
			fmt.Sprintf("[%s][acard]concat=n=2:v=0:a=1[ajcard]", acur))
		vcur, acur = "vjcard", "ajcard"
	}
	if p.Duck || p.MusicFade > 0 {
		// missaggio di giv post mix: voci, musica abbassata sotto le voci,
		// dissolvenza finale della musica
		m := Mix{Music: p.Music, Duck: p.Duck, MusicFade: p.MusicFade}
		var vin []int
		for i, c := range p.Clips {
			if voiceIn[i] >= 0 {
				m.Voices = append(m.Voices, Voice{Path: c.Voice, At: starts[i] + c.VoiceAt})
				vin = append(vin, voiceIn[i])
			}
		}
		mg, err := m.Filters("["+acur+"]", total, vin, musicIn, "aout")
		if err != nil {
			return nil, invalid("%v", err)
		}
		aud(mg...)
		acur = "aout"
	} else {
		mix := []string{"[" + acur + "]"}
		for i := range p.Clips {
			if voiceIn[i] < 0 {
				continue
			}
			at := int64(math.Round((starts[i] + p.Clips[i].VoiceAt) * sampleRate))
			aud(fmt.Sprintf("[%d:a:0]%s,adelay=delays=%dS:all=1[voce%d]", voiceIn[i], audioFmt, at, i))
			mix = append(mix, fmt.Sprintf("[voce%d]", i))
		}
		if musicIn >= 0 {
			vol := p.Music.Volume
			if vol == 0 {
				vol = 1
			}
			aud(fmt.Sprintf("[%d:a:0]%s,volume=%s,atrim=duration=%s[musica]", musicIn, audioFmt, num(vol), num(total)))
			mix = append(mix, "[musica]")
		}
		if len(mix) > 1 {
			aud(fmt.Sprintf("%samix=inputs=%d:duration=first:dropout_transition=0:normalize=0[aout]", strings.Join(mix, ""), len(mix)))
			acur = "aout"
		}
	}
	if p.Loudness != 0 {
		aud(fmt.Sprintf("[%s]%s[anorm]", acur, LoudnormFilter(p.Loudness, p.measured)))
		acur = "anorm"
	}
	if analyze {
		args = append(args, "-filter_complex", strings.Join(g, ";"), "-map", "["+acur+"]")
		return append(args, nullOut()...), nil
	}
	if p.Logo != nil {
		l := *p.Logo
		if l.To == 0 && p.EndCard != nil {
			l.To = scenesEnd // il cartello ha il suo logo
		}
		f, err := LogoFilter(vcur, logoIn, "vlogo", l, w, h, margins)
		if err != nil {
			return nil, err
		}
		vid(f)
		vcur = "vlogo"
	}
	if p.Subtitles != "" {
		abs, err := filepath.Abs(p.Subtitles)
		if err != nil {
			return nil, invalid("sottotitoli: %v", err)
		}
		vid(fmt.Sprintf("[%s]ass=filename=%s[vout]", vcur, filterValue(abs)))
		vcur = "vout"
	}
	if p.texts != "" {
		f, err := ASSFilter(p.texts, p.fontsDir)
		if err != nil {
			return nil, err
		}
		vid(fmt.Sprintf("[%s]%s[vtesti]", vcur, f))
		vcur = "vtesti"
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

// ProbeArgs costruisce gli argomenti di ffprobe per leggere durata e formato
// (un'immagine alla lettera, readOpts).
func ProbeArgs(path string) ([]string, error) {
	return probeArgsFormats(path, probeFormats)
}
