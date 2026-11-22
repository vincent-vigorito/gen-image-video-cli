package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/output"
)

// Comandi locali (giv post <operazione>, sheet, frame, gif, export): ffmpeg su
// file esistenti, nessuna chiamata a provider, nessuna chiave, nessuna spesa.
// Flag comuni --video, --out, --name, --dry-run; su stdout solo il JSON del
// risultato, il resto su stderr; un valore non valido esce con codice 2 prima
// di lanciare ffmpeg; il file nuovo si pubblica in --out a lavoro riuscito,
// senza sostituire niente (ingresso compreso); una riga a costo zero in
// giv-log.jsonl (provider local, model ffmpeg).

// Provider e modello dei comandi locali nel manifest e nel registro.
const (
	localProvider = "local"
	localModel    = "ffmpeg"
)

// localTool lancia ffmpeg e ffprobe per i comandi locali (i test lo puntano
// a programmi finti o assenti).
var localTool = montage.Tool{}

// localFlags sono i flag comuni dei comandi locali.
type localFlags struct {
	Video  string // --video: file in ingresso
	Out    string // --out: cartella d'uscita
	Name   string // --name: nome del file senza cartelle né estensione
	DryRun bool   // --dry-run: solo controlli e piano
}

// localHelpFlags è l'aiuto dei flag comuni, in coda a quello di ogni comando.
const localHelpFlags = `Flag comuni:
  --video <file>   video in ingresso, file locale: non viene mai modificato né sovrascritto
  --out <dir>      cartella d'uscita (default: out), creata se manca
  --name <nome>    nome del file, senza cartelle (default: <nome del video>-<operazione>);
                   se il file esiste già si usa <nome>-2, <nome>-3…
  --dry-run        nessun file: controlli e piano (JSON), gratuito
  -h, --help       questo aiuto

Uscita: su stdout solo il JSON del risultato, avvisi ed errori su stderr; una riga a
costo zero in giv-log.jsonl. Un valore non valido esce con codice 2 senza lanciare ffmpeg.
`

// newLocalFlags crea il FlagSet di un comando locale coi flag comuni; help è
// l'aiuto del comando (uso e flag propri), seguito da localHelpFlags.
func newLocalFlags(command, help string) (*flag.FlagSet, *localFlags) {
	lf := &localFlags{}
	fs := flag.NewFlagSet(command, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, help+"\n"+localHelpFlags) }
	fs.StringVar(&lf.Video, "video", "", "video in ingresso")
	fs.StringVar(&lf.Out, "out", "out", "cartella d'uscita")
	fs.StringVar(&lf.Name, "name", "", "nome del file d'uscita")
	fs.BoolVar(&lf.DryRun, "dry-run", false, "solo controlli e piano")
	return fs, lf
}

// parseLocal legge i flag; nessun argomento libero.
func parseLocal(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return usageError("%v", err)
	}
	if fs.NArg() > 0 {
		return usageError("giv %s: argomento inatteso %q (i valori vanno nei flag; aiuto: giv %s --help)", fs.Name(), fs.Arg(0), fs.Name())
	}
	return nil
}

// localSource è il file in ingresso coi suoi valori misurati.
type localSource struct {
	Path string
	Info *montage.Info
}

// source controlla --video (obbligatorio, file normale con una traccia video)
// e ne legge le misure con ffprobe.
func (lf *localFlags) source() (*localSource, error) {
	if strings.TrimSpace(lf.Video) == "" {
		return nil, usageError("manca --video <file>")
	}
	info, err := localMedia("--video", lf.Video)
	if err != nil {
		return nil, err
	}
	if info.Video == nil {
		return nil, usageError("--video %s: nessuna traccia video", lf.Video)
	}
	return &localSource{Path: lf.Video, Info: info}, nil
}

// localMedia controlla un file in ingresso di un comando locale (flag è il
// nome del flag, per i messaggi) e ne legge le misure con ffprobe: un file
// mancante o illeggibile è un errore d'uso (codice 2), ffprobe assente un
// errore (codice 1).
func localMedia(flagName, path string) (*montage.Info, error) {
	if err := localTool.Check(); err != nil {
		return nil, &cliError{code: exitError, msg: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), montage.ProbeTimeout)
	defer cancel()
	info, err := localTool.Probe(ctx, path)
	if err != nil {
		if errors.Is(err, montage.ErrInvalid) {
			return nil, usageError("%s %s: %s", flagName, path, localErrMsg(err))
		}
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, usageError("%s %s: file illeggibile: %v", flagName, path, err)
	}
	return info, nil
}

// localErrMsg è il messaggio di un errore di montage senza il prefisso di
// ErrInvalid (che parla di piano di montaggio).
func localErrMsg(err error) string {
	return strings.Replace(err.Error(), montage.ErrInvalid.Error()+": ", "", 1)
}

// localOp è un'operazione locale controllata e pronta: la prepara il comando
// (flag, misure, valori) e la esegue runLocal.
type localOp struct {
	Command string // nel manifest e nel registro: "post trim", "frame"…
	// Inputs sono tutti i file letti, per ruolo: "video" (il --video),
	// "audio", "logo", "font"…; più file dello stesso tipo con un numero
	// ("clip-1", "clip-2").
	Inputs   map[string]string
	Name     string         // nome di default del file, senza estensione (localName)
	Ext      string         // estensione dell'uscita: ".mp4", ".png", ".gif"…
	Params   map[string]any // valori controllati dell'operazione, nel manifest
	Warnings []string       // avvisi, su stderr e nel manifest
	// Build scrive il risultato in staged lanciando ffmpeg (vedi montage.Op).
	Build func(w *montage.Work, staged string) error
}

// localManifest è l'uscita di un comando locale e la sua riga del registro.
// provider, model e cost_usd rendono la riga leggibile da giv log come le
// altre: costo sempre zero.
type localManifest struct {
	DryRun     bool                 `json:"dry_run,omitempty"`
	Command    string               `json:"command"`
	Provider   string               `json:"provider"`
	Model      string               `json:"model"`
	Inputs     map[string]inputFile `json:"inputs"` // per ruolo, come inputs.video dei comandi sulle clip
	Params     map[string]any       `json:"params,omitempty"`
	Output     string               `json:"output,omitempty"` // solo --dry-run: il file che verrebbe scritto
	Files      []output.FileInfo    `json:"files"`
	DurationS  float64              `json:"duration_s,omitempty"`
	Width      int                  `json:"width,omitempty"`
	Height     int                  `json:"height,omitempty"`
	FPS        float64              `json:"fps,omitempty"`
	VideoCodec string               `json:"video_codec,omitempty"`
	AudioCodec string               `json:"audio_codec,omitempty"`
	CostUSD    float64              `json:"cost_usd"`
	Warnings   []string             `json:"warnings,omitempty"`
}

// localLogEntry è la riga di giv-log.jsonl di un comando locale.
type localLogEntry struct {
	Ts string `json:"ts"`
	localManifest
}

// localName è il nome di default dell'uscita: il nome del file in ingresso
// senza estensione, un trattino e suffix (es. "scena1-trim").
func localName(input, suffix string) string {
	stem := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	if stem == "" || strings.HasPrefix(stem, ".") {
		stem = "video"
	}
	return stem + "-" + suffix
}

// checkName controlla --name: un nome di file semplice, senza cartelle.
func checkName(name string) error {
	if strings.TrimSpace(name) == "" {
		return usageError("--name vuoto")
	}
	if name != filepath.Base(name) || strings.ContainsAny(name, `/\`) || name == "." || name == ".." || strings.HasPrefix(name, ".") {
		return usageError("--name %q: solo un nome di file, senza cartelle (l'uscita va in --out)", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return usageError("--name %q: caratteri di controllo", name)
		}
	}
	if len(name) > 200 {
		return usageError("--name: al più 200 byte")
	}
	return nil
}

// mimeOf è il tipo dei file scritti dai comandi locali.
func mimeOf(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4":
		return "video/mp4"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	}
	return "application/octet-stream"
}

// runLocal esegue l'operazione e stampa il manifest (o il piano con --dry-run).
func runLocal(lf *localFlags, op localOp) error {
	man, err := execLocal(lf, op)
	if err != nil {
		return err
	}
	return output.PrintJSON(man)
}

// execLocal esegue l'operazione senza stampare: con --dry-run restituisce il
// piano senza scrivere niente; altrimenti scrive il file, aggiunge la riga
// del registro e restituisce il manifest (giv export ne fa più d'uno).
func execLocal(lf *localFlags, op localOp) (*localManifest, error) {
	if op.Command == "" || op.Build == nil || !strings.HasPrefix(op.Ext, ".") {
		return nil, errors.New("operazione locale incompleta")
	}
	if strings.TrimSpace(lf.Out) == "" {
		return nil, usageError("--out vuoto")
	}
	if strings.ContainsFunc(lf.Out, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, usageError("--out %q: caratteri di controllo", lf.Out)
	}
	name := lf.Name
	if name == "" {
		name = op.Name
	}
	name = strings.TrimSuffix(name, op.Ext)
	if err := checkName(name); err != nil {
		return nil, err
	}
	out := filepath.Join(lf.Out, name+op.Ext)
	if len(op.Inputs) == 0 {
		return nil, errors.New("operazione locale senza ingressi")
	}
	man := &localManifest{Command: op.Command, Provider: localProvider, Model: localModel,
		Inputs: map[string]inputFile{}, Params: op.Params, Files: []output.FileInfo{}, Warnings: op.Warnings}
	var paths []string
	for _, role := range sortedKeys(op.Inputs) {
		paths = append(paths, op.Inputs[role])
		man.Inputs[role] = inputFile{Path: op.Inputs[role]}
	}
	for _, w := range op.Warnings {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
	}
	if lf.DryRun {
		man.DryRun, man.Output = true, montage.FreeOutput(out)
		return man, nil
	}
	fmt.Fprintf(os.Stderr, "giv %s: scrivo in %s\n", op.Command, lf.Out)
	res, err := localTool.RunOp(context.Background(), montage.Op{Inputs: paths, Output: out, Root: lf.Out, Build: op.Build})
	if err != nil {
		if errors.Is(err, montage.ErrInvalid) {
			return nil, usageError("giv %s: %s", op.Command, localErrMsg(err))
		}
		if errors.Is(err, montage.ErrMissingTool) {
			return nil, &cliError{code: exitError, msg: err.Error()}
		}
		return nil, fmt.Errorf("giv %s: %w", op.Command, err)
	}
	for role, in := range man.Inputs {
		in.SHA256 = shortHash(in.Path)
		man.Inputs[role] = in
	}
	size := 0
	if st, err := os.Stat(res.Output); err == nil {
		size = int(st.Size())
	}
	man.Files = []output.FileInfo{{Path: res.Output, Mime: mimeOf(res.Output), Bytes: size}}
	if i := res.Info; i != nil {
		man.DurationS = round3(i.Duration)
		if v := i.Video; v != nil {
			man.Width, man.Height, man.FPS, man.VideoCodec = v.Width, v.Height, round3(v.FPS), v.Codec
		}
		if a := i.Audio; a != nil {
			man.AudioCodec = a.Codec
		}
	}
	entry := localLogEntry{Ts: time.Now().Format(time.RFC3339), localManifest: *man}
	if err := appendLocalLog(entry, paths); err != nil {
		fmt.Fprintf(os.Stderr, "avviso: registro %s non aggiornato: %v\n", logFile, err)
	}
	return man, nil
}

// appendLocalLog accoda la riga al registro giv-log.jsonl della cartella
// corrente senza seguire link: il nome deve essere un file normale (o
// mancare, e allora si crea), aperto dentro la cartella (os.Root) e,
// controllato sul descrittore, diverso da ogni ingresso (anche tramite un
// link fisso).
func appendLocalLog(entry any, inputs []string) error {
	r, err := os.OpenRoot(".")
	if err != nil {
		return err
	}
	defer r.Close()
	if st, err := r.Lstat(logFile); err == nil && !st.Mode().IsRegular() {
		return errors.New("non è un file normale (link o cartella): non lo seguo")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := r.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("non è un file normale")
	}
	for _, in := range inputs {
		if ist, err := os.Stat(in); err == nil && os.SameFile(st, ist) {
			return fmt.Errorf("è lo stesso file dell'ingresso %s: non lo modifico", in)
		}
	}
	return json.NewEncoder(f).Encode(entry)
}

// postOp è un'operazione di giv post; ognuna si registra dal suo file con
// registerPost, in un init.
type postOp struct {
	Name  string                    // nome dopo giv post: trim, speed…
	Usage string                    // una riga per giv post --help: flag propri e cosa fa
	Run   func(args []string) error // gli argomenti dopo il nome
}

// postOps sono le operazioni registrate di giv post.
var postOps = map[string]postOp{}

// registerPost aggiunge un'operazione a giv post.
func registerPost(op postOp) {
	if op.Name == "" || op.Run == nil {
		panic("giv post: operazione senza nome o senza Run")
	}
	if _, dup := postOps[op.Name]; dup {
		panic("giv post: operazione registrata due volte: " + op.Name)
	}
	postOps[op.Name] = op
}

// postHelp è l'aiuto di giv post, con le operazioni registrate in ordine alfabetico.
func postHelp() string {
	var b strings.Builder
	b.WriteString(`giv post — rifinitura di un video in locale con ffmpeg: nessuna chiamata a provider, nessuna spesa

Uso:
  giv post <operazione> --video <file> [flag dell'operazione] [--out <dir>] [--name <nome>] [--dry-run]
  giv post <operazione> --help   aiuto e flag dell'operazione

Operazioni:
`)
	names := sortedKeys(postOps)
	if len(names) == 0 {
		b.WriteString("  (nessuna disponibile in questa versione)\n")
	}
	for _, n := range names {
		fmt.Fprintf(&b, "  %-9s %s\n", n, postOps[n].Usage)
	}
	b.WriteString("\n" + localHelpFlags)
	return b.String()
}

// cmdPost è giv post <operazione>.
func cmdPost(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, postHelp())
		return usageError("giv post: manca l'operazione")
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(os.Stderr, postHelp())
		return nil
	}
	op, ok := postOps[args[0]]
	if !ok {
		names := strings.Join(sortedKeys(postOps), ", ")
		if names == "" {
			names = "nessuna in questa versione"
		}
		return usageError("giv post %s: operazione sconosciuta (disponibili: %s)", args[0], names)
	}
	return op.Run(args[1:])
}
