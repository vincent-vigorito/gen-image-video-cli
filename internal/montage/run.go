package montage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 5 * time.Second
	out := &limitBuffer{max: maxStdout}
	errBuf := &tailBuffer{max: 4096}
	cmd.Stdout = out
	cmd.Stderr = errBuf
	err := cmd.Run()
	name := filepath.Base(bin)
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s: interrotto, superato il tetto di tempo: %w", name, context.DeadlineExceeded)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s: %w", name, ctx.Err())
	}
	if out.exceeded {
		return nil, fmt.Errorf("%s: uscita oltre %d byte, interrotto", name, maxStdout)
	}
	if err != nil {
		msg := strings.TrimSpace(string(errBuf.buf))
		if msg == "" {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		return nil, fmt.Errorf("%s: %v: %s", name, err, msg)
	}
	return out.buf.Bytes(), nil
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
	return os.MkdirTemp("", prefix)
}

// publish copia il file preparato nella cartella privata al nome finale, senza
// riaprire per nome file che altri possono sostituire. Senza overwrite il nome
// finale si crea con O_EXCL, che fallisce se esiste (anche come link
// simbolico): un file comparso nel frattempo non viene mai sostituito. Con
// overwrite si scrive in un temporaneo nuovo accanto (O_EXCL, tenuto aperto),
// i permessi si cambiano dal descrittore e la rinomina sostituisce la voce
// della cartella senza seguire link.
func publish(src, out string, overwrite bool) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	var f *os.File
	if overwrite {
		f, err = os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".part-*")
	} else {
		f, err = os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%w: %s (chiedi la sovrascrittura per sostituirlo)", ErrExists, out)
		}
	}
	if err != nil {
		return err
	}
	name := f.Name()
	_, err = io.Copy(f, in)
	if err == nil && overwrite {
		err = f.Chmod(0o644)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && overwrite {
		err = os.Rename(name, out)
	}
	if err != nil {
		os.Remove(name)
		return fmt.Errorf("scrittura di %s: %w", out, err)
	}
	return nil
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
	return publish(staged, out, overwrite)
}

// Montage esegue il piano: controlla file e tracce, completa le durate mancanti
// dei video con quella del file, converte un .srt in .ass, monta in una
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
			if c.Duration == 0 {
				if info.Duration <= 0 {
					return nil, invalid("clip %d: durata di %s ignota, indicala nel piano", i+1, c.Path)
				}
				c.Duration = info.Duration
			} else if info.Duration > 0 && info.Duration+0.05 < c.Duration {
				res.Warnings = append(res.Warnings, fmt.Sprintf("clip %d: il file dura %s s, la scena %s s: l'ultimo fotogramma resta fermo",
					i+1, num(info.Duration), num(c.Duration)))
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

	final := p.Output
	staged := p
	staged.Output, staged.Overwrite = filepath.Join(work, "montato.mp4"), false
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
	if err := publish(staged.Output, final, p.Overwrite); err != nil {
		return nil, err
	}
	res.Output, res.Duration, res.Info = final, total, info
	return res, nil
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
