package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gen-image-video-cli/internal/config"
	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/provider"
	"gen-image-video-cli/internal/provider/gemini"
	"gen-image-video-cli/internal/provider/openaicompat"
)

const (
	version = "0.7.0"
	logFile = "giv-log.jsonl"
)

// Codici di uscita, come negli altri CLI del progetto (1 = errore generico).
const (
	exitError    = 1
	exitUsage    = 2  // uso sbagliato: flag, prompt, provider o modello
	exitNotFound = 3  // modello non trovato (HTTP 404)
	exitAuth     = 4  // autenticazione o credito (HTTP 401/402/403)
	exitAPI      = 5  // errore dell'API o di rete
	exitRate     = 7  // troppe richieste (HTTP 429)
	exitNoKey    = 10 // chiave API mancante
)

// cliError è un errore deciso dal CLI con il suo codice di uscita.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

func usageError(format string, a ...any) error {
	return &cliError{code: exitUsage, msg: fmt.Sprintf(format, a...)}
}

// exitCode sceglie codice di uscita e suggerimento per l'errore.
func exitCode(err error) (int, string) {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.code, ""
	}
	var se *httpx.StartError
	if errors.As(err, &se) {
		return exitAPI, "controlla sul provider se il job è partito prima di rilanciare: un secondo invio può far pagare due video"
	}
	var je *httpx.JobError
	job := errors.As(err, &je)
	jobHint := ""
	if job {
		jobHint = fmt.Sprintf("il job %s è già partito e può essere addebitato: cercalo sul provider prima di rilanciare", je.ID)
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return exitAuth, fmt.Sprintf("chiave rifiutata da %s: controlla %s", he.Prefix, envKeys[he.Prefix])
		case http.StatusPaymentRequired:
			return exitAuth, fmt.Sprintf("credito esaurito su %s: ricaricalo o usa un altro provider (--provider o GIV_PROVIDER)", he.Prefix)
		case http.StatusTooManyRequests:
			return exitRate, "troppe richieste: riprova tra qualche minuto"
		case http.StatusNotFound:
			if !job {
				return exitNotFound, fmt.Sprintf("modello non trovato: elenca quelli disponibili con giv models --provider %s", he.Prefix)
			}
		}
		return exitAPI, jobHint
	}
	if job || httpx.IsNetwork(err) {
		return exitAPI, jobHint
	}
	return exitError, ""
}

// providers è l'ordine dei provider nel testo di aiuto.
var providers = []string{"gemini", "openai", "xai", "openrouter"}

// Tabelle dei modelli di default: valgono per il codice e per il testo di aiuto.
var defaultImageModels = map[string]string{
	"gemini":     "gemini-3.1-flash-image",
	"openai":     "gpt-image-2.6-flare",
	"xai":        "grok-imagine-image-2.0",
	"openrouter": "google/gemini-3.1-flash-image",
}

// gemini non ha un default video (serve --model); openai non ha un'API video.
var defaultVideoModels = map[string]string{
	"xai":        "grok-imagine-video-1.5",
	"openrouter": "google/gemini-omni-1.2-flash",
}

type manifest struct {
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Prompt   string            `json:"prompt"`
	Files    []output.FileInfo `json:"files"`
	CostUSD  float64           `json:"cost_usd,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitUsage)
	}
	var err error
	switch os.Args[1] {
	case "models":
		err = cmdModels(os.Args[2:])
	case "image":
		err = cmdImage(os.Args[2:])
	case "video":
		err = cmdVideo(os.Args[2:])
	case "log":
		err = cmdLog(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("giv " + version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "comando sconosciuto: %s\n\n", os.Args[1])
		usage()
		os.Exit(exitUsage)
	}
	if err != nil {
		code, hint := exitCode(err)
		fmt.Fprintln(os.Stderr, "errore:", err)
		if hint != "" {
			fmt.Fprintln(os.Stderr, "suggerimento:", hint)
		}
		os.Exit(code)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `giv — generazione immagini e video multi-provider (gemini, openai, xai, openrouter)

Uso:
  giv models [--all] [--json]      elenca i modelli image/video disponibili
  giv image  [flag] "<prompt>"     genera immagini
  giv video  [flag] "<prompt>"     genera un video (job asincrono ~1-5 min)
  giv log    [-n N|--sum-cost]     registro locale delle generazioni (giv-log.jsonl)
  giv version                      stampa la versione

Flag comuni:
  --provider <p>     gemini | openai | xai | openrouter (default: GIV_PROVIDER, altrimenti gemini)
  --model <nome>     modello; senza --model il default del provider:
%s  --out <dir>        directory di output (default: out)
  --name <slug>      base dei nomi file (default: derivato dal prompt)

giv image:
  -n <num>           numero di immagini (default 1)
  --aspect <ratio>   1:1, 16:9, 9:16, 4:3, 3:4
  --seed <n>         seed deterministico (gemini, openrouter; altrove ignorato con avviso)
  --input <file|url> immagine di input/riferimento, ripetibile (non supportato da xai);
                     gli URL http(s) vengono scaricati automaticamente (vale anche per --image di video)

giv video:
  --aspect <ratio>   16:9, 9:16
  --resolution <r>   720p, 1080p
  --negative "<t>"   negative prompt
  --image <file>     frame iniziale (image-to-video)
  --duration <s>     durata in secondi (valori ammessi dal modello)

Credenziali: GEMINI_API_KEY / OPENAI_API_KEY / XAI_API_KEY / OPENROUTER_API_KEY e GIV_PROVIDER
             da env di processo, ./credentials.env, ./.env o ~/.config/giv/credentials.env
             (in quest'ordine: vince il primo che la definisce)
Output: file salvati in --out, manifest JSON su stdout (i log vanno su stderr)
`, defaultsHelp())
}

// defaultsHelp genera la tabella dei modelli di default dalle mappe defaultImageModels
// e defaultVideoModels, così l'aiuto non diverge dal codice.
func defaultsHelp() string {
	const indent = "                       "
	rows := [][3]string{{"provider", "image", "video"}}
	for _, p := range providers {
		video := defaultVideoModels[p]
		switch {
		case p == "openai":
			video = "non supportato: usa openrouter o xai"
		case video == "":
			video = "nessun default: --model obbligatorio"
		}
		rows = append(rows, [3]string{p, defaultImageModels[p], video})
	}
	w0, w1 := 0, 0
	for _, r := range rows {
		w0, w1 = max(w0, len(r[0])), max(w1, len(r[1]))
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%s%-*s  %-*s  %s\n", indent, w0, r[0], w1, r[1], r[2])
	}
	return b.String()
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// parsePrompt accetta i flag sia prima che dopo il prompt (quotato).
func parsePrompt(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", usageError("%v", err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return "", usageError("manca il prompt")
	}
	prompt := rest[0]
	if len(rest) > 1 {
		if err := fs.Parse(rest[1:]); err != nil {
			return "", usageError("%v", err)
		}
		if len(fs.Args()) > 0 {
			return "", usageError("passa il prompt come singolo argomento tra virgolette")
		}
	}
	return prompt, nil
}

var envKeys = map[string]string{
	"gemini":     "GEMINI_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"xai":        "XAI_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
}

// defaultProvider è il provider usato senza --provider: GIV_PROVIDER (env o file
// di configurazione, come le chiavi), altrimenti gemini.
func defaultProvider() string {
	if p := strings.TrimSpace(config.Get("GIV_PROVIDER")); p != "" {
		return p
	}
	return "gemini"
}

func checkProvider(name string) error {
	if _, ok := envKeys[name]; !ok {
		return usageError("provider sconosciuto: %s (validi: gemini, openai, xai, openrouter)", name)
	}
	return nil
}

func newProvider(name string) (provider.Provider, error) {
	if err := checkProvider(name); err != nil {
		return nil, err
	}
	envKey := envKeys[name]
	key := config.Get(envKey)
	if key == "" {
		return nil, &cliError{code: exitNoKey, msg: envKey + " mancante: mettila in .env o credentials.env nella directory " +
			"corrente o in ~/.config/giv/credentials.env, oppure esportala"}
	}
	switch name {
	case "gemini":
		return gemini.New(key), nil
	case "openai":
		return openaicompat.New("openai", "https://api.openai.com/v1", key,
			openaicompat.Options{UseSize: true}), nil
	case "xai":
		return openaicompat.New("xai", "https://api.x.ai/v1", key,
			openaicompat.Options{B64Param: true, Grok: true}), nil
	default: // openrouter
		return openaicompat.New("openrouter", "https://openrouter.ai/api/v1", key,
			openaicompat.Options{Router: true}), nil
	}
}

func resolveModel(flagValue, providerName string, defaults map[string]string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if m := defaults[providerName]; m != "" {
		return m, nil
	}
	return "", usageError("nessun modello di default per %s: specifica --model", providerName)
}

// resolveVideoModel è resolveModel per i video, con i casi senza default spiegati:
// openai non ha un'API video, gemini non ha un modello video di default.
func resolveVideoModel(flagValue, providerName string) (string, error) {
	switch {
	case providerName == "openai":
		return "", usageError("openai non ha un'API video: usa --provider openrouter (default %s) o --provider xai (default %s)",
			defaultVideoModels["openrouter"], defaultVideoModels["xai"])
	case providerName == "gemini" && flagValue == "":
		return "", usageError("nessun modello video di default per gemini: usa --provider openrouter --model %s, "+
			"oppure indica un modello gemini con --model", defaultVideoModels["openrouter"])
	}
	return resolveModel(flagValue, providerName, defaultVideoModels)
}

func cmdModels(args []string) error {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	prov := fs.String("provider", defaultProvider(), "provider")
	all := fs.Bool("all", false, "includi anche i modelli non image/video")
	asJSON := fs.Bool("json", false, "output JSON")
	fs.Parse(args)
	c, err := newProvider(*prov)
	if err != nil {
		return err
	}
	models, err := c.Models(context.Background())
	if err != nil {
		return err
	}
	if !*all {
		var f []provider.ModelInfo
		for _, m := range models {
			if m.Kind == "image" || m.Kind == "video" {
				f = append(f, m)
			}
		}
		models = f
	}
	if *asJSON {
		return output.PrintJSON(models)
	}
	for _, m := range models {
		fmt.Printf("%-50s %-6s %s\n", m.Name, m.Kind, m.DisplayName)
	}
	return nil
}

func cmdImage(args []string) error {
	fs := flag.NewFlagSet("image", flag.ExitOnError)
	prov := fs.String("provider", defaultProvider(), "provider")
	model := fs.String("model", "", "modello (default: dipende dal provider)")
	n := fs.Int("n", 1, "numero di immagini")
	aspect := fs.String("aspect", "", "aspect ratio")
	out := fs.String("out", "out", "directory di output")
	name := fs.String("name", "", "slug per i nomi file")
	seed := fs.Int("seed", 0, "seed deterministico (gemini, openrouter; altrove ignorato)")
	var inputs stringList
	fs.Var(&inputs, "input", "immagine di input (ripetibile)")
	prompt, err := parsePrompt(fs, args)
	if err != nil {
		return err
	}
	c, err := newProvider(*prov)
	if err != nil {
		return err
	}
	m, err := resolveModel(*model, *prov, defaultImageModels)
	if err != nil {
		return err
	}
	resolved := make([]string, len(inputs))
	for i, in := range inputs {
		if resolved[i], err = resolveInput(in); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "generazione immagine con %s/%s…\n", *prov, m)
	res, err := c.GenerateImage(context.Background(), provider.ImageRequest{
		Prompt: prompt, Model: m, N: *n, Aspect: *aspect, Seed: *seed, Inputs: resolved,
	})
	if err != nil {
		return err
	}
	slug := *name
	if slug == "" {
		slug = output.Slug(prompt)
	}
	files, err := output.SaveAll(*out, slug, res.Media)
	if err != nil {
		return err
	}
	return emit("image", manifest{Provider: c.Name(), Model: m, Prompt: prompt, Files: files, CostUSD: res.CostUSD})
}

func cmdVideo(args []string) error {
	fs := flag.NewFlagSet("video", flag.ExitOnError)
	prov := fs.String("provider", defaultProvider(), "provider")
	model := fs.String("model", "", "modello (default: dipende dal provider)")
	aspect := fs.String("aspect", "", "aspect ratio")
	resolution := fs.String("resolution", "", "720p o 1080p")
	negative := fs.String("negative", "", "negative prompt")
	image := fs.String("image", "", "frame iniziale (image-to-video)")
	duration := fs.Int("duration", 0, "durata in secondi (0 = default modello)")
	out := fs.String("out", "out", "directory di output")
	name := fs.String("name", "", "slug per i nomi file")
	prompt, err := parsePrompt(fs, args)
	if err != nil {
		return err
	}
	// provider e modello prima delle credenziali: gli errori video (openai, gemini
	// senza --model) escono senza chiave e senza chiamate di rete
	if err := checkProvider(*prov); err != nil {
		return err
	}
	m, err := resolveVideoModel(*model, *prov)
	if err != nil {
		return err
	}
	c, err := newProvider(*prov)
	if err != nil {
		return err
	}
	img := *image
	if img != "" {
		if img, err = resolveInput(img); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "generazione video con %s/%s…\n", *prov, m)
	res, err := c.GenerateVideo(context.Background(), provider.VideoRequest{
		Prompt: prompt, Model: m, Aspect: *aspect,
		Resolution: *resolution, Negative: *negative, Image: img,
		Duration: *duration,
	})
	if err != nil {
		return err
	}
	slug := *name
	if slug == "" {
		slug = output.Slug(prompt)
	}
	files, err := output.SaveAll(*out, slug, res.Media)
	if err != nil {
		return err
	}
	return emit("video", manifest{Provider: c.Name(), Model: m, Prompt: prompt, Files: files, CostUSD: res.CostUSD})
}

type logEntry struct {
	Ts      string `json:"ts"`
	Command string `json:"command"`
	manifest
}

// emit stampa il manifest su stdout e lo appende al registro locale.
func emit(command string, man manifest) error {
	entry := logEntry{Ts: time.Now().Format(time.RFC3339), Command: command, manifest: man}
	if err := output.AppendLog(logFile, entry); err != nil {
		fmt.Fprintf(os.Stderr, "avviso: registro %s non aggiornato: %v\n", logFile, err)
	}
	return output.PrintJSON(man)
}

var inputHTTP = &http.Client{Timeout: 120 * time.Second}

// resolveInput accetta un path locale o un URL http(s); gli URL vengono scaricati in tmp.
func resolveInput(p string) (string, error) {
	if !strings.HasPrefix(p, "http://") && !strings.HasPrefix(p, "https://") {
		return p, nil
	}
	resp, err := inputHTTP.Get(p)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download input %s: HTTP %d", p, resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(p)
	if err != nil {
		return "", err
	}
	ext := filepath.Ext(u.Path)
	if ext == "" {
		ext = extForMime(resp.Header.Get("Content-Type"))
	}
	sum := sha256.Sum256([]byte(p))
	dst := filepath.Join(os.TempDir(), fmt.Sprintf("giv-input-%x%s", sum[:6], ext))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "input scaricato: %s (%d byte)\n", dst, len(data))
	return dst, nil
}

func extForMime(ct string) string {
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "jpeg"):
		return ".jpg"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "gif"):
		return ".gif"
	default:
		return ".bin"
	}
}

func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	n := fs.Int("n", 20, "numero di righe da mostrare")
	sum := fs.Bool("sum-cost", false, "totale generazioni e costo registrato")
	asJSON := fs.Bool("json", false, "righe raw JSONL")
	fs.Parse(args)
	data, err := os.ReadFile(logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("nessun registro %s in questa directory", logFile)
		}
		return err
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if *sum {
		var total float64
		for _, l := range lines {
			var e struct {
				CostUSD float64 `json:"cost_usd"`
			}
			if json.Unmarshal([]byte(l), &e) == nil {
				total += e.CostUSD
			}
		}
		return output.PrintJSON(map[string]any{"generations": len(lines), "cost_usd": total})
	}
	if len(lines) > *n {
		lines = lines[len(lines)-*n:]
	}
	if *asJSON {
		for _, l := range lines {
			fmt.Println(l)
		}
		return nil
	}
	for _, l := range lines {
		var e logEntry
		if json.Unmarshal([]byte(l), &e) != nil {
			continue
		}
		cost := ""
		if e.CostUSD > 0 {
			cost = fmt.Sprintf("  $%.4f", e.CostUSD)
		}
		file0 := ""
		if len(e.Files) > 0 {
			file0 = e.Files[0].Path
		}
		fmt.Printf("%s  %-5s  %s/%s%s  %s\n", e.Ts, e.Command, e.Provider, e.Model, cost, file0)
	}
	return nil
}
