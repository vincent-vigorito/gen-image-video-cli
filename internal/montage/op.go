package montage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Operazioni locali su file (giv post, sheet, frame, gif, export): ffmpeg
// legge file esistenti e scrive un file nuovo, senza rete né chiavi. Valgono
// le regole del pacchetto (argomenti come lista, file come "file:" più il
// percorso assoluto, solo file locali dai formati ammessi, testi in un file
// .ass generato da ASS e mai nella stringa dei filtri) e in più: ffmpeg
// scrive solo nella cartella privata del lavoro; il file finale si pubblica
// nella cartella d'uscita a lavoro riuscito, senza mai sostituire un file che
// c'è già (ingressi compresi): se il nome è preso si usa <nome>-2, <nome>-3…

// imageFormats sono i formati ammessi in lettura per le immagini (logo,
// cartello, fotogrammi).
const imageFormats = "image2,png_pipe,jpeg_pipe,webp_pipe"

// outputFormats sono i formati ammessi nella lettura del file scritto.
const outputFormats = probeFormats + ",gif"

// maxNames è il numero di nomi provati per l'uscita (<nome>, <nome>-2…).
const maxNames = 100

// Op è un'operazione locale: legge Inputs e scrive Output, dentro Root.
type Op struct {
	// Inputs sono tutti i file letti (video, audio, immagini, font, .ass):
	// devono essere file normali esistenti; nessuno viene mai sovrascritto.
	Inputs []string
	// Output è il file da scrivere; l'estensione dice il formato (.mp4,
	// .png, .jpg, .gif). Se esiste già si usa il primo nome libero fra
	// <nome>-2, <nome>-3… (il nome scelto è in OpResult.Output).
	Output string
	// Root è la cartella d'uscita (--out), creata se manca: Output deve
	// starci dentro, altrimenti ErrInvalid. Vuota: la cartella di Output.
	Root string
	// Build scrive il risultato in staged, un file nella cartella privata
	// w.Dir con l'estensione di Output, lanciando ffmpeg con w.FFmpeg (anche
	// più passate, con gli intermedi in w.Dir). Non scrive altrove.
	Build func(w *Work, staged string) error
}

// OpResult è l'esito di RunOp.
type OpResult struct {
	Output string // file scritto (Output, o il primo nome libero)
	Info   *Info  // misure del file scritto, lette con ffprobe
}

// Work è un'operazione in corso: la cartella privata (0700, tolta alla fine)
// e i programmi, entro la scadenza dell'operazione.
type Work struct {
	Dir    string // cartella privata del lavoro
	ctx    context.Context
	tool   Tool
	ffmpeg string
}

// Context è la scadenza dell'operazione.
func (w *Work) Context() context.Context { return w.ctx }

// FFmpeg lancia ffmpeg con args (lista, mai shell), entro la scadenza
// dell'operazione; l'errore riporta la coda dello stderr.
func (w *Work) FFmpeg(args []string) error {
	_, err := run(w.ctx, w.ffmpeg, w.tool.timeout(), args)
	return err
}

// Analyze è FFmpeg per le passate di analisi (loudnorm, ebur128,
// volumedetect…): restituisce gli ultimi 64 KiB dello stderr.
func (w *Work) Analyze(args []string) (string, error) {
	_, errOut, err := runTail(w.ctx, w.ffmpeg, w.tool.timeout(), args, 64<<10)
	return string(errOut), err
}

// Probe legge le misure di un file (anche un intermedio della cartella privata).
func (w *Work) Probe(path string) (*Info, error) {
	return w.tool.Probe(w.ctx, path)
}

// Path è il percorso di un file della cartella privata: name è un nome
// semplice, senza cartelle (es. "palette.png").
func (w *Work) Path(name string) (string, error) {
	if err := checkPath(name); err != nil || name != filepath.Base(name) || name == "." || name == ".." {
		return "", fmt.Errorf("nome %q non valido per un file di lavoro", name)
	}
	return filepath.Join(w.Dir, name), nil
}

// WriteFile scrive data in un file nuovo (0600) della cartella privata e ne
// restituisce il percorso: per i file .ass generati da ASS.
func (w *Work) WriteFile(name string, data []byte) (string, error) {
	p, err := w.Path(name)
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return p, err
}

// RunOp esegue l'operazione: controlla ingressi e uscita (ErrInvalid),
// lancia Build in una cartella privata, legge il risultato con ffprobe e lo
// pubblica nella cartella d'uscita senza sostituire file esistenti. Tutto
// entro Tool.Timeout.
func (t Tool) RunOp(ctx context.Context, op Op) (*OpResult, error) {
	if op.Build == nil {
		return nil, errors.New("operazione senza Build")
	}
	if len(op.Inputs) == 0 {
		return nil, invalid("nessun file in ingresso")
	}
	for _, in := range op.Inputs {
		if err := checkPath(in); err != nil {
			return nil, invalid("ingresso: %v", err)
		}
		if err := regularFile(in); err != nil {
			return nil, invalid("%v", err)
		}
	}
	if err := CheckOutput(op.Output, op.Inputs); err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(op.Output))
	if !simpleExt(ext) {
		return nil, invalid("uscita %s: serve un'estensione come .mp4, .png o .gif", op.Output)
	}
	root := op.Root
	if root == "" {
		root = filepath.Dir(op.Output)
	}
	if err := checkPath(root); err != nil {
		return nil, invalid("cartella d'uscita: %v", err)
	}
	if _, err := relInside(root, op.Output); err != nil {
		return nil, invalid("uscita %s: %v", op.Output, err)
	}
	if st, err := os.Stat(root); err == nil && !st.IsDir() {
		return nil, invalid("la cartella d'uscita %s è un file", root)
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
	work, err := stagingDir("giv-locale-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	w := &Work{Dir: work, ctx: ctx, tool: t, ffmpeg: ffmpeg}
	staged := filepath.Join(work, "uscita"+ext)
	if err := op.Build(w, staged); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(staged); err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return nil, fmt.Errorf("%s: ffmpeg non ha scritto niente", op.Output)
	}
	args, err := probeArgsFormats(staged, outputFormats)
	if err != nil {
		return nil, err
	}
	info, err := t.probeArgs(ctx, staged, args)
	if err != nil {
		return nil, fmt.Errorf("risultato illeggibile: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("cartella d'uscita %s: %w", root, err)
	}
	for _, out := range OutputNames(op.Output) {
		err = publish(ctx, staged, root, nil, out, false)
		if errors.Is(err, ErrExists) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return &OpResult{Output: out, Info: info}, nil
	}
	return nil, fmt.Errorf("%s: esistono già %s e i nomi da -2 a -%d: scegli un altro nome", op.Output, op.Output, maxNames)
}

// CheckOutput rifiuta (ErrInvalid) un'uscita che è uno degli ingressi: stesso
// percorso, oppure lo stesso file tramite link. Gli altri file esistenti non
// sono un errore: RunOp sceglie il primo nome libero.
func CheckOutput(out string, inputs []string) error {
	if err := checkPath(out); err != nil {
		return invalid("uscita: %v", err)
	}
	absOut, err := filepath.Abs(out)
	if err != nil {
		return invalid("uscita %s: %v", out, err)
	}
	stOut, errOut := os.Stat(out)
	for _, in := range inputs {
		same := false
		if absIn, err := filepath.Abs(in); err == nil && absIn == absOut {
			same = true
		} else if errOut == nil {
			if stIn, err := os.Stat(in); err == nil && os.SameFile(stIn, stOut) {
				same = true
			}
		}
		if same {
			return invalid("l'uscita %s è il file in ingresso %s: scegli un altro --name o --out", out, in)
		}
	}
	return nil
}

// OutputNames sono i nomi provati per l'uscita, in ordine: out, poi
// <nome>-2<ext>… fino a <nome>-100<ext>.
func OutputNames(out string) []string {
	ext := filepath.Ext(out)
	stem := strings.TrimSuffix(out, ext)
	names := []string{out}
	for n := 2; n <= maxNames; n++ {
		names = append(names, stem+"-"+strconv.Itoa(n)+ext)
	}
	return names
}

// FreeOutput è il primo nome libero di OutputNames (per il piano di
// --dry-run; RunOp lo ricontrolla al momento di pubblicare).
func FreeOutput(out string) string {
	for _, n := range OutputNames(out) {
		if _, err := os.Lstat(n); err != nil {
			return n
		}
	}
	return out
}

// simpleExt accetta un'estensione come ".mp4": un punto e da 1 a 5 lettere o cifre.
func simpleExt(ext string) bool {
	if len(ext) < 2 || len(ext) > 6 || ext[0] != '.' {
		return false
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// probeArgsFormats è ProbeArgs con i formati ammessi indicati.
func probeArgsFormats(path, formats string) ([]string, error) {
	if err := checkPath(path); err != nil {
		return nil, invalid("%v", err)
	}
	f, err := fileArg(path)
	if err != nil {
		return nil, invalid("%s: %v", path, err)
	}
	args := []string{"-hide_banner", "-loglevel", "error"}
	args = append(args, inputOpts(formats)...)
	args = append(args, "-show_entries",
		"format=format_name,duration:stream=index,codec_type,codec_name,width,height,avg_frame_rate,r_frame_rate,sample_rate,channels,duration",
		"-of", "json", "-i", f)
	return args, nil
}

// Pezzi comuni degli argomenti di ffmpeg delle operazioni (i file delle
// operazioni stanno in questo pacchetto e usano anche fileArg, filterValue,
// num e invalid).

// ffArgs è l'inizio comune: niente banner né stdin, solo errori, e -n
// (ffmpeg non scrive mai su un file esistente). Le passate di analisi che
// leggono il risultato dallo stderr (Work.Analyze) alzano il livello.
func ffArgs() []string {
	return []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "error", "-n"}
}

// inputArgs sono le opzioni e il -i per leggere path: solo file locale, solo
// i formati indicati (videoFormats, audioFormats, imageFormats); opts va
// prima di -i (es. "-ss", "1.5" o "-loop", "1").
func inputArgs(path, formats string, opts ...string) ([]string, error) {
	if err := checkPath(path); err != nil {
		return nil, invalid("ingresso: %v", err)
	}
	f, err := fileArg(path)
	if err != nil {
		return nil, invalid("ingresso %s: %v", path, err)
	}
	args := append([]string(nil), opts...)
	args = append(args, inputOpts(formats)...)
	return append(args, "-i", f), nil
}

// outputArg è il file d'uscita come lo legge ffmpeg ("file:" più il percorso
// assoluto: un nome che inizia con '-' non diventa un'opzione).
func outputArg(path string) (string, error) {
	if err := checkPath(path); err != nil {
		return "", invalid("uscita: %v", err)
	}
	return fileArg(path)
}

// h264Args è la codifica video comune delle uscite .mp4 (come il montaggio).
func h264Args() []string {
	return []string{"-c:v", "libx264", "-preset", "medium", "-crf", "20", "-pix_fmt", "yuv420p"}
}

// aacArgs è la codifica audio comune delle uscite .mp4 (come il montaggio).
func aacArgs() []string {
	return []string{"-c:a", "aac", "-b:a", "192k", "-ar", strconv.Itoa(sampleRate), "-ac", "2"}
}

// mp4Out sono gli ultimi argomenti di un'uscita .mp4: moov in testa, formato
// mp4, il file.
func mp4Out(path string) ([]string, error) {
	o, err := outputArg(path)
	if err != nil {
		return nil, err
	}
	return []string{"-movflags", "+faststart", "-f", "mp4", o}, nil
}
