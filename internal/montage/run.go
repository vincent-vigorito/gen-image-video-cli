package montage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrMissingTool: ffmpeg o ffprobe non trovati.
var ErrMissingTool = errors.New("ffmpeg non disponibile")

// ErrExists: il file finale esiste già e il piano non chiede di sovrascriverlo.
var ErrExists = errors.New("il file d'uscita esiste già")

// Tetti di tempo di default.
const (
	DefaultTimeout = 30 * time.Minute // un intero Montage: letture, conversione e montaggio
	ProbeTimeout   = time.Minute      // una lettura con ffprobe, un LastFrame
)

// Tool lancia ffmpeg e ffprobe.
type Tool struct {
	FFmpeg  string        // percorso di ffmpeg; vuoto: cercato nel PATH
	FFprobe string        // percorso di ffprobe; vuoto: cercato nel PATH
	Timeout time.Duration // tetto di un intero Montage; 0: DefaultTimeout
}

// Info è il risultato di Probe.
type Info struct {
	Format   string  // nomi del formato secondo ffprobe (es. "mov,mp4,m4a,3gp,3g2,mj2")
	Duration float64 // secondi; 0 se ignota (es. immagini)
	Video    *VideoStream
	Audio    *AudioStream
}

// VideoStream descrive la prima traccia video.
type VideoStream struct {
	Codec  string
	Width  int
	Height int
	FPS    float64
}

// AudioStream descrive la prima traccia audio.
type AudioStream struct {
	Codec      string
	SampleRate int
	Channels   int
}

// Result è l'esito di Montage.
type Result struct {
	Output   string   // file finale scritto
	Duration float64  // durata attesa dal piano
	Info     *Info    // formato del file scritto, letto con ffprobe
	Warnings []string // avvisi (clip più corte del dichiarato, voci tagliate…)
}

func (t Tool) bin(name, configured string) (string, error) {
	if configured == "" {
		configured = name
	}
	p, err := exec.LookPath(configured)
	if err != nil {
		return "", fmt.Errorf("%w: %s non trovato: installalo (es. apt install ffmpeg) per montare le scene", ErrMissingTool, configured)
	}
	return p, nil
}

// Check verifica che ffmpeg e ffprobe ci siano.
func (t Tool) Check() error {
	if _, err := t.bin("ffmpeg", t.FFmpeg); err != nil {
		return err
	}
	_, err := t.bin("ffprobe", t.FFprobe)
	return err
}

// tailBuffer tiene gli ultimi max byte scritti (lo stderr di ffmpeg).
type tailBuffer struct {
	buf []byte
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

// limitBuffer tiene lo stdout fino a max byte: oltre, la scrittura fallisce,
// la pipe si chiude e il programma si ferma.
type limitBuffer struct {
	buf      bytes.Buffer
	max      int
	exceeded bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.max {
		b.exceeded = true
		return 0, errors.New("uscita troppo lunga")
	}
	return b.buf.Write(p)
}

// maxStdout è il tetto dello stdout letto (la risposta JSON di ffprobe).
const maxStdout = 1 << 20

// run lancia il programma con gli argomenti come lista (niente shell), senza
// stdin, entro il tetto di tempo (il più stretto fra timeout e la scadenza di
// ctx); restituisce lo stdout, al più maxStdout byte.
func run(ctx context.Context, bin string, timeout time.Duration, args []string) ([]byte, error) {
	out, _, err := runTail(ctx, bin, timeout, args, 4096)
	return out, err
}

// runTail è run che restituisce anche gli ultimi tail byte dello stderr (le
// passate di analisi di ffmpeg, come loudnorm, scrivono lì il risultato).
func runTail(ctx context.Context, bin string, timeout time.Duration, args []string, tail int) ([]byte, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 5 * time.Second
	out := &limitBuffer{max: maxStdout}
	errBuf := &tailBuffer{max: tail}
	cmd.Stdout = out
	cmd.Stderr = errBuf
	err := cmd.Run()
	name := filepath.Base(bin)
	if ctx.Err() == context.DeadlineExceeded {
		return nil, nil, fmt.Errorf("%s: interrotto, superato il tetto di tempo: %w", name, context.DeadlineExceeded)
	}
	if ctx.Err() != nil {
		return nil, nil, fmt.Errorf("%s: %w", name, ctx.Err())
	}
	if out.exceeded {
		return nil, nil, fmt.Errorf("%s: uscita oltre %d byte, interrotto", name, maxStdout)
	}
	if err != nil {
		msg := strings.TrimSpace(string(errBuf.buf))
		if len(msg) > 4096 {
			msg = msg[len(msg)-4096:]
		}
		if msg == "" {
			return nil, nil, fmt.Errorf("%s: %v", name, err)
		}
		return nil, nil, fmt.Errorf("%s: %v: %s", name, err, msg)
	}
	return out.buf.Bytes(), errBuf.buf, nil
}

func (t Tool) timeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return DefaultTimeout
}

func (t Tool) probeTimeout() time.Duration {
	if t.Timeout > 0 && t.Timeout < ProbeTimeout {
		return t.Timeout
	}
	return ProbeTimeout
}

// regularFile verifica che il percorso sia un file normale esistente.
func regularFile(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("file mancante: %s", path)
		}
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s non è un file", path)
	}
	return nil
}

// Probe legge durata e formato di un file con ffprobe.
func (t Tool) Probe(ctx context.Context, path string) (*Info, error) {
	args, err := ProbeArgs(path)
	if err != nil {
		return nil, err
	}
	return t.probeArgs(ctx, path, args)
}

// probeArgs è Probe con gli argomenti di ffprobe già costruiti.
func (t Tool) probeArgs(ctx context.Context, path string, args []string) (*Info, error) {
	if err := regularFile(path); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	bin, err := t.bin("ffprobe", t.FFprobe)
	if err != nil {
		return nil, err
	}
	out, err := run(ctx, bin, t.probeTimeout(), args)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var raw struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType    string `json:"codec_type"`
			CodecName    string `json:"codec_name"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			AvgFrameRate string `json:"avg_frame_rate"`
			RFrameRate   string `json:"r_frame_rate"`
			SampleRate   string `json:"sample_rate"`
			Channels     int    `json:"channels"`
			Duration     string `json:"duration"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("%s: risposta di ffprobe illeggibile: %v", path, err)
	}
	info := &Info{Format: raw.Format.FormatName, Duration: parseFloat(raw.Format.Duration)}
	for _, s := range raw.Streams {
		switch s.CodecType {
		case "video":
			if info.Video == nil {
				fps := parseRate(s.AvgFrameRate)
				if fps == 0 {
					fps = parseRate(s.RFrameRate)
				}
				info.Video = &VideoStream{Codec: s.CodecName, Width: s.Width, Height: s.Height, FPS: fps}
				if info.Duration == 0 {
					info.Duration = parseFloat(s.Duration)
				}
			}
		case "audio":
			if info.Audio == nil {
				sr, _ := strconv.Atoi(s.SampleRate)
				info.Audio = &AudioStream{Codec: s.CodecName, SampleRate: sr, Channels: s.Channels}
				if info.Duration == 0 {
					info.Duration = parseFloat(s.Duration)
				}
			}
		}
	}
	return info, nil
}

func parseFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || !finite(f) || f < 0 {
		return 0
	}
	return f
}

// parseRate legge una frequenza "30000/1001" o "30".
func parseRate(s string) float64 {
	n, d, ok := strings.Cut(s, "/")
	if !ok {
		return parseFloat(s)
	}
	num, den := parseFloat(n), parseFloat(d)
	if den == 0 {
		return 0
	}
	return num / den
}

// checkOutput applica la regola di sovrascrittura prima di lanciare ffmpeg.
func checkOutput(out string, overwrite bool) error {
	st, err := os.Lstat(out)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("%w: l'uscita %s è una cartella", ErrInvalid, out)
	}
	if !overwrite {
		return fmt.Errorf("%w: %s (chiedi la sovrascrittura per sostituirlo)", ErrExists, out)
	}
	return nil
}

// sameAsInput rifiuta un'uscita che esiste ed è lo stesso file di un ingresso
// (anche attraverso link o cartelle con un altro nome).
func sameAsInput(out string, inputs []string) error {
	ost, err := os.Stat(out)
	if err != nil {
		return nil
	}
	for _, in := range inputs {
		if ist, err := os.Stat(in); err == nil && os.SameFile(ost, ist) {
			return fmt.Errorf("%w: l'uscita %s è lo stesso file dell'ingresso %s", ErrInvalid, out, in)
		}
	}
	return nil
}

// stagingDir crea la cartella privata (0700, nella cartella temporanea del
// sistema) dove ffmpeg scrive: nessun altro utente può sostituirvi i file.
func stagingDir(prefix string) (string, error) {
	return TempDir(prefix)
}

// TempDir crea una cartella privata (0700) nella cartella temporanea del
// sistema, solo se quella e le cartelle sopra sono protette (tempBase): in
// una TMPDIR condivisa senza protezione un altro utente potrebbe rinominare
// la cartella privata e metterne un'altra al suo posto.
func TempDir(prefix string) (string, error) {
	tmp := os.TempDir()
	base, err := tempBase(tmp)
	if err != nil {
		return "", fmt.Errorf("cartella temporanea %s: %w: imposta TMPDIR su una cartella tua", tmp, err)
	}
	return os.MkdirTemp(base, prefix)
}

// ctxReader interrompe la lettura alla scadenza o all'annullamento di ctx.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// publish copia il file preparato nella cartella privata al nome finale, con
// sole operazioni vincolate a una radice aperta una volta (os.Root): opened se
// il chiamante l'ha già aperta (root ne è il percorso), altrimenti root, o la
// cartella di out se root è vuota. Nessun passo segue un link o una cartella
// sostituita che porti fuori dalla radice. La copia va sempre in un
// temporaneo nuovo accanto (O_EXCL, tenuto aperto), entro la scadenza di ctx:
// il nome finale compare solo col file completo. Senza overwrite il
// temporaneo si collega al nome finale con un link fisso, che fallisce se il
// nome esiste (anche come link simbolico): un file comparso nel frattempo non
// viene mai sostituito; dove i link fissi non ci sono si crea il nome finale
// con O_EXCL e ci si copia. Con overwrite la rinomina sostituisce la voce
// della cartella senza seguire link.
func publish(ctx context.Context, src, root string, opened *os.Root, out string, overwrite bool) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if root == "" {
		root = filepath.Dir(out)
		if err := os.MkdirAll(root, 0o755); err != nil {
			return err
		}
	}
	rel, err := relInside(root, out)
	if err != nil {
		return fmt.Errorf("%w: uscita %s: %v", ErrInvalid, out, err)
	}
	r := opened
	if r == nil {
		if r, err = os.OpenRoot(root); err != nil {
			return err
		}
		defer r.Close()
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := r.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("cartella d'uscita di %s: %w", out, err)
		}
	}
	exists := func() error {
		return fmt.Errorf("%w: %s (chiedi la sovrascrittura per sostituirlo)", ErrExists, out)
	}
	if !overwrite {
		if _, err := r.Lstat(rel); err == nil {
			return exists()
		}
	}
	f, name, err := createTemp(r, rel)
	if err != nil {
		return fmt.Errorf("scrittura di %s: %w", out, err)
	}
	_, err = io.Copy(f, ctxReader{ctx, in})
	if err == nil {
		err = f.Chmod(0o644)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && overwrite {
		err = r.Rename(name, rel)
	}
	if err == nil && !overwrite {
		err = r.Link(name, rel)
		switch {
		case errors.Is(err, fs.ErrExist):
			r.Remove(name)
			return exists()
		case err != nil:
			err = copyNew(ctx, r, name, rel) // cartella senza link fissi
		}
		r.Remove(name)
		if errors.Is(err, fs.ErrExist) {
			return exists()
		}
	}
	if err != nil {
		r.Remove(name)
		return fmt.Errorf("scrittura di %s: %w", out, err)
	}
	return nil
}

// copyNew copia il temporaneo name nel nome finale rel creato con O_EXCL
// (mai un file che c'è già), entro la scadenza di ctx; a errore lo toglie.
func copyNew(ctx context.Context, r *os.Root, name, rel string) error {
	in, err := r.Open(name)
	if err != nil {
		return err
	}
	defer in.Close()
	f, err := r.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, ctxReader{ctx, in})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		r.Remove(rel)
	}
	return err
}

// createTemp crea nella radice, accanto a rel, un temporaneo nuovo (O_EXCL,
// 0600) per la pubblicazione.
func createTemp(r *os.Root, rel string) (*os.File, string, error) {
	dir, base := filepath.Split(rel)
	for range 10 {
		name := filepath.Join(dir, "."+base+".part-"+strconv.FormatUint(rand.Uint64(), 36))
		f, err := r.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, fs.ErrExist) {
			return f, name, err
		}
	}
	return nil, "", fmt.Errorf("temporaneo accanto a %s: nomi esauriti", rel)
}

// probeErr distingue un file illeggibile (errore del piano) da tetto di tempo
// e annullamento, che restano tali.
func probeErr(what string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", what, err)
	}
	if errors.Is(err, ErrInvalid) {
		return fmt.Errorf("%s: %w", what, err)
	}
	return fmt.Errorf("%w: %s: %v", ErrInvalid, what, err)
}

// LastFrame scrive in out (.png o .jpg) l'ultimo fotogramma della clip; con
// overwrite false si rifiuta se out esiste. Tutto entro ProbeTimeout (o
// Tool.Timeout se più stretto).
func (t Tool) LastFrame(ctx context.Context, clip, out string, overwrite bool) error {
	if _, err := LastFrameArgs(clip, out, overwrite); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, t.probeTimeout())
	defer cancel()
	if err := regularFile(clip); err != nil {
		return fmt.Errorf("%w: clip: %v", ErrInvalid, err)
	}
	if err := checkOutput(out, overwrite); err != nil {
		return err
	}
	if err := sameAsInput(out, []string{clip}); err != nil {
		return err
	}
	bin, err := t.bin("ffmpeg", t.FFmpeg)
	if err != nil {
		return err
	}
	work, err := stagingDir("giv-fotogramma-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	staged := filepath.Join(work, "fotogramma"+strings.ToLower(filepath.Ext(out)))
	args, err := LastFrameArgs(clip, staged, false)
	if err != nil {
		return err
	}
	if _, err := run(ctx, bin, t.probeTimeout(), args); err != nil {
		return fmt.Errorf("ultimo fotogramma di %s: %w", clip, err)
	}
	if st, err := os.Stat(staged); err != nil || st.Size() == 0 {
		return fmt.Errorf("ultimo fotogramma di %s: nessun fotogramma video", clip)
	}
	return publish(ctx, staged, "", nil, out, overwrite)
}

// Montage esegue il piano: controlla file e tracce, completa le durate mancanti
// dei video con quella del file, converte un .srt in .ass, sposta i
// sottotitoli delle clip con la voce ritardata (VoiceAt), monta in una
// cartella privata e porta il file al nome finale solo a lavoro riuscito.
// Tutto (letture con ffprobe, conversione, montaggio) sta entro Tool.Timeout.
func (t Tool) Montage(ctx context.Context, p Plan) (*Result, error) {
	if err := p.checkStatic(false); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout())
	defer cancel()
	ffmpeg, err := t.bin("ffmpeg", t.FFmpeg)
	if err != nil {
		return nil, err
	}
	if _, err := t.bin("ffprobe", t.FFprobe); err != nil {
		return nil, err
	}
	for _, in := range p.inputs() {
		if err := regularFile(in); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	if err := checkOutput(p.Output, p.Overwrite); err != nil {
		return nil, err
	}
	if err := sameAsInput(p.Output, p.inputs()); err != nil {
		return nil, err
	}

	res := &Result{}
	clips := append([]Clip(nil), p.Clips...)
	p.Clips = clips
	voiceLen := make([]float64, len(clips))
	for i := range clips {
		c := &clips[i]
		if !c.Image {
			info, err := t.Probe(ctx, c.Path)
			if err != nil {
				return nil, probeErr(fmt.Sprintf("clip %d", i+1), err)
			}
			if info.Video == nil {
				return nil, invalid("clip %d: %s non ha una traccia video", i+1, c.Path)
			}
			if c.KeepAudio && info.Audio == nil {
				return nil, invalid("clip %d: %s non ha una traccia audio da tenere", i+1, c.Path)
			}
			length := info.Duration // secondi della clip nel montato, a velocità normale
			if c.retimed() && length > 0 {
				if c.TrimStart >= length {
					return nil, invalid("clip %d: il taglio parte a %s s, il file %s dura %s s", i+1, num(c.TrimStart), c.Path, num(length))
				}
				if c.TrimEnd > length+0.05 {
					res.Warnings = append(res.Warnings, fmt.Sprintf("clip %d: il taglio finisce a %s s, il file dura %s s", i+1, num(c.TrimEnd), num(length)))
				}
				if c.TrimEnd > 0 {
					length = min(length, c.TrimEnd)
				}
				length = (length - c.TrimStart) / c.speed()
			}
			if c.Duration == 0 {
				if length <= 0 {
					return nil, invalid("clip %d: durata di %s ignota, indicala nel piano", i+1, c.Path)
				}
				c.Duration = length
			} else if length > 0 && length+0.05 < c.Duration {
				res.Warnings = append(res.Warnings, fmt.Sprintf("clip %d: il file dura %s s, la scena %s s: l'ultimo fotogramma resta fermo",
					i+1, num(length), num(c.Duration)))
			}
		}
		if c.Voice != "" {
			info, err := t.Probe(ctx, c.Voice)
			if err != nil {
				return nil, probeErr(fmt.Sprintf("clip %d: voce", i+1), err)
			}
			if info.Audio == nil {
				return nil, invalid("clip %d: la voce %s non ha una traccia audio", i+1, c.Voice)
			}
			voiceLen[i] = info.Duration
		}
	}
	if p.Music != nil {
		info, err := t.Probe(ctx, p.Music.Path)
		if err != nil {
			return nil, probeErr("musica", err)
		}
		if info.Audio == nil {
			return nil, invalid("musica: %s non ha una traccia audio", p.Music.Path)
		}
	}
	starts, total, err := Timeline(p)
	if err != nil {
		return nil, err
	}
	for i, c := range clips {
		if c.Voice != "" && starts[i]+c.VoiceAt+voiceLen[i] > total+0.05 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("clip %d: la voce supera la fine del montato e viene tagliata", i+1))
		}
	}

	work, err := stagingDir("giv-montaggio-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	if p.Subtitles != "" && strings.ToLower(filepath.Ext(p.Subtitles)) == ".srt" {
		ass := filepath.Join(work, "sottotitoli.ass")
		args, err := srtToASSArgs(p.Subtitles, ass)
		if err != nil {
			return nil, err
		}
		if _, err := run(ctx, ffmpeg, t.timeout(), args); err != nil {
			return nil, probeErr("sottotitoli "+p.Subtitles, err)
		}
		p.Subtitles = ass
	}
	if p.Subtitles != "" && voiceDelayed(clips) {
		shifted := filepath.Join(work, "sottotitoli-voce"+strings.ToLower(filepath.Ext(p.Subtitles)))
		if err := shiftSubtitles(p.Subtitles, shifted, starts, clips); err != nil {
			return nil, err
		}
		p.Subtitles = shifted
	}
	outTotal := total
	if p.extended() {
		w := &Work{Dir: work, ctx: ctx, tool: t, ffmpeg: ffmpeg}
		if outTotal, err = t.prepareExtended(w, &p, starts, total, res); err != nil {
			return nil, err
		}
	}

	final := p.Output
	staged := p
	staged.Output, staged.Overwrite, staged.Root = filepath.Join(work, "montato.mp4"), false, ""
	args, err := Args(staged)
	if err != nil {
		return nil, err
	}
	if _, err := run(ctx, ffmpeg, t.timeout(), args); err != nil {
		return nil, fmt.Errorf("montaggio: %w", err)
	}
	info, err := t.Probe(ctx, staged.Output)
	if err != nil {
		return nil, fmt.Errorf("montato illeggibile: %w", err)
	}
	if err := publish(ctx, staged.Output, p.Root, p.RootDir, final, p.Overwrite); err != nil {
		return nil, err
	}
	res.Output, res.Duration, res.Info = final, outTotal, info
	return res, nil
}

// prepareExtended prepara in p le sovrimpressioni e l'audio della 0.11.0:
// misure del logo e delle immagini del cartello, tempi dei titoli sul
// montato, file .ass dei testi coi font nella cartella di lavoro, misura del
// volume finale (prima passata). Restituisce la durata del montato col
// cartello. Logo e cartello si copiano: il piano del chiamante non cambia.
func (t Tool) prepareExtended(w *Work, p *Plan, starts []float64, total float64, res *Result) (float64, error) {
	image := func(what, path string) (*Info, error) {
		info, err := t.Probe(w.ctx, path)
		if err != nil {
			return nil, probeErr(what, err)
		}
		if err := CheckImage(path, info); err != nil {
			return nil, err
		}
		return info, nil
	}
	if p.Logo != nil {
		l := *p.Logo
		info, err := image("logo", l.Path)
		if err != nil {
			return 0, err
		}
		l.SrcW, l.SrcH = info.Video.Width, info.Video.Height
		if l.From >= total || l.To > total+timeSlack {
			return 0, invalid("logo: finestra da %s a %s s fuori dalle scene (%s s)", num(l.From), num(l.To), num(total))
		}
		l.To = min(l.To, total)
		p.Logo = &l
	}
	outTotal := total
	if p.EndCard != nil {
		c := *p.EndCard
		if c.Image != "" {
			if _, err := image("cartello finale: immagine", c.Image); err != nil {
				return 0, err
			}
		}
		if c.Logo != nil {
			lg := *c.Logo
			info, err := image("cartello finale: logo", lg.Path)
			if err != nil {
				return 0, err
			}
			lg.SrcW, lg.SrcH = info.Video.Width, info.Video.Height
			c.Logo = &lg
		}
		if err := c.check(); err != nil {
			return 0, err
		}
		p.EndCard = &c
		outTotal += c.Duration
	}
	var texts []Text
	for i, ti := range p.Titles {
		x := ti.Text
		if ti.Clip > 0 {
			x.Start += starts[ti.Clip-1]
			x.End += starts[ti.Clip-1]
		}
		if x.Start >= outTotal {
			return 0, invalid("titolo %d: comincia a %s s, il montato dura %s s", i+1, num(x.Start), num(outTotal))
		}
		if x.End > outTotal+timeSlack {
			res.Warnings = append(res.Warnings, fmt.Sprintf("titolo %d: finisce a %s s, oltre la fine del montato (%s s): tagliato", i+1, num(x.End), num(outTotal)))
		}
		x.End = min(x.End, outTotal)
		texts = append(texts, x)
	}
	m := SafeMargins(p.Width, p.Height)
	if p.Margins != nil {
		m = *p.Margins
	}
	if p.EndCard != nil {
		texts = append(texts, EndCardTexts(*p.EndCard, total, p.Height, m)...)
	}
	if len(texts) > 0 {
		data, err := ASS(p.Width, p.Height, m, texts)
		if err != nil {
			return 0, err
		}
		if p.texts, err = w.WriteFile("testi.ass", data); err != nil {
			return 0, err
		}
		if len(p.Fonts) > 0 {
			if p.fontsDir, err = w.Fonts(p.Fonts...); err != nil {
				return 0, err
			}
		}
	}
	if p.Loudness != 0 {
		args, err := loudnessArgs(*p)
		if err != nil {
			return 0, err
		}
		if p.measured, err = w.MeasureLoudness(args); err != nil {
			return 0, fmt.Errorf("misura del volume: %w", err)
		}
	}
	return outTotal, nil
}

// srtToASSArgs converte un .srt in .ass leggendolo col solo lettore SubRip.
func srtToASSArgs(src, dst string) ([]string, error) {
	in, err := fileArg(src)
	if err != nil {
		return nil, invalid("sottotitoli: %v", err)
	}
	out, err := fileArg(dst)
	if err != nil {
		return nil, err
	}
	return []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "error", "-n",
		"-protocol_whitelist", "file", "-f", "srt", "-i", in, "-f", "ass", out}, nil
}
