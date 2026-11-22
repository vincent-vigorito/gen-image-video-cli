package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/config"
	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/presets"
	"gen-image-video-cli/internal/provider"
	"gen-image-video-cli/internal/provider/fal"
	"gen-image-video-cli/internal/provider/gemini"
	"gen-image-video-cli/internal/provider/openaicompat"
)

const (
	version = "0.10.0"
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

// openai non ha un'API video (vedi resolveVideoModel).
var defaultVideoModels = map[string]string{
	"gemini":     caps.GeminiVideoModel,
	"xai":        "grok-imagine-video-1.5",
	"openrouter": "google/gemini-omni-1.2-flash",
	"fal":        "kling-v3-pro",
}

// manifest è l'uscita di image e video e la riga del registro. I campi oltre
// provider, model, prompt, files e cost_usd sono opzionali: assenti quando non
// servono, così i manifest e i registri di prima si leggono come prima.
type manifest struct {
	Command     string            `json:"command,omitempty"`
	Provider    string            `json:"provider"`
	Model       string            `json:"model"`
	Prompt      string            `json:"prompt"`
	PromptFinal string            `json:"prompt_final,omitempty"` // prompt inviato, se diverso (preset)
	Presets     []presets.Applied `json:"presets,omitempty"`
	Inputs      *manifestInputs   `json:"inputs,omitempty"`
	Params      map[string]any    `json:"params,omitempty"`
	Files       []output.FileInfo `json:"files"`
	JobID       string            `json:"job_id,omitempty"`
	Status      string            `json:"status,omitempty"` // solo registro: failed, timeout
	DurationS   float64           `json:"duration_s,omitempty"`
	CostUSD     float64           `json:"cost_usd,omitempty"`     // riportato dal provider
	CostEstUSD  float64           `json:"cost_est_usd,omitempty"` // stimato da listino, se il provider non lo riporta
	CostSource  string            `json:"cost_source,omitempty"`
	// stima dai consumi riportati (gemini), informativa accanto a quella da listino
	CostUsageUSD    float64  `json:"cost_usage_usd,omitempty"`
	CostUsageSource string   `json:"cost_usage_source,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`

	usageEstUSD float64 // CostUsageUSD non arrotondata (conto del tetto di story run)
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
	case "extend":
		err = cmdExtend(os.Args[2:])
	case "edit":
		err = cmdEdit(os.Args[2:])
	case "job":
		err = cmdJob(os.Args[2:])
	case "log":
		err = cmdLog(os.Args[2:])
	case "presets":
		err = cmdPresets(os.Args[2:])
	case "story":
		err = cmdStory(os.Args[2:])
	case "upscale":
		err = cmdUpscale(os.Args[2:])
	case "avatar":
		err = cmdAvatar(os.Args[2:])
	case "post":
		err = cmdPost(os.Args[2:])
	case "export":
		err = cmdExport(os.Args[2:])
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
	fmt.Fprintf(os.Stderr, `giv — generazione immagini e video multi-provider (gemini, openai, xai, openrouter, fal)

Uso:
  giv models [--all] [--json]      elenca i modelli image/video disponibili
             [--kind image|video|other] [--caps]   --caps (con --kind video): capacità e $/s
  giv image  [flag] "<prompt>"     genera immagini
  giv video  [flag] "<prompt>"     genera un video (job asincrono ~1-5 min)
  giv extend --video <mp4|url https> | --from <job_id> [--provider xai|gemini] [--duration 2-10|gemini 3-10] "<come continua>"  allunga una clip (xai; gemini solo da --from)
  giv edit   --video <mp4|url https> | --from <job_id> [--provider xai|gemini] "<cosa cambiare>"  modifica una clip (xai; gemini solo da --from)
  giv job fetch --provider <p> <id>  scarica un job video già partito, senza nuovo avvio (--out, --name, --timeout)
  giv log    [-n N|--sum-cost [--by provider|model]]
                                   registro locale delle generazioni (giv-log.jsonl)
  giv presets [--json]             elenca i preset di camera ed effetti
  giv presets show <nome> [<nome>] [--model M] [--prompt "<t>"] [--negative "<t>"]
                   [--tutela prodotto|illustrazione] [--strict]
                                   frase finale dei preset per quel modello (nessuna chiamata)
  giv story check|run|cut <file.json>  storyboard a più scene (check: valida e stima, gratuito;
                                   run: --scene <id> [--new-keyframe] [--new-job]; aiuto: giv story help)
  giv upscale --video <url|file> [--provider openrouter|fal] [--factor 1.5-3|fal 1-4] [--mode precise|creative] ["<prompt>"]  ingrandisce una clip (file locale solo con fal; openrouter vuole un URL https; a pagamento)
  giv avatar --image <file|url> --audio <wav|mp3> [--model standard|pro] ["<prompt>"]  personaggio che parla (fal)
  giv post <operazione> --video <file> [--out <dir>] [--name <nome>] [--dry-run]  rifinitura in locale con ffmpeg, gratuita (operazioni: giv post help)
  giv export --video <file> --for instagram,tiktok,youtube,linkedin[:<formato>] [--fit crop|pad|blur] [--focus <x>[,<y>]] [--loudness <lufs>|off]  un file per canale, in locale con ffmpeg, gratuito (aiuto: giv export --help)
  giv version                      stampa la versione

Flag comuni:
  --provider <p>     gemini | openai | xai | openrouter | fal (default: GIV_PROVIDER, altrimenti gemini)
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
  --last-frame <file|url>  ultimo fotogramma (xai 1.5, gemini, openrouter dove il modello lo prevede)
  --ref <file|url>   immagine di riferimento, ripetibile (xai reference_images, openrouter input_references,
                     gemini fino a 6)
  --keyframe <file|url>  fotogramma intermedio, ripetibile in ordine (fal kling-4.0, al massimo 8)
  --shot "<s>:<testo>"   inquadratura di s secondi, ripetibile (fal kling-4.0 e kling-v3-pro):
                     senza prompt né --preset, il video dura la somma
  --duration <s>     durata in secondi (valori ammessi dal modello)
  --audio on|off     audio nativo (default del modello)
  --seed <n>         seed (openrouter dove il modello lo rispetta; xai e gemini ignorato con avviso)
  --param k=v        parametro passante del modello, ripetibile (openrouter; valore JSON o stringa)
  --timeout <durata> tetto di attesa del job (default 15m)
  --dry-run          nessuna generazione: stampa parametri, avvisi e costo stimato (JSON)
  --max-cost <usd>   rifiuta (codice 2) se il costo stimato supera il tetto o non è stimabile
  --strict           anche i controlli di rifinitura non supportati (--negative, --seed...) sono errori
  Prima di generare giv controlla capacità e prezzi del modello (openrouter: /videos/models,
  xai e gemini: tabella interna): un controllo strutturale non supportato esce con codice 2.
  Senza --provider né GIV_PROVIDER giv video esce con codice 2: il default gemini vale solo per le immagini.
  gemini (gemini-omni-1.2-flash, a pagamento): audio sempre presente, durata 3-15 s (default 10),
  360p-4k (default 720p), 16:9 o 9:16; la durata e la risoluzione sono sempre inviate.
  --preset <nome>    preset di camera o effetto, ripetibile: al massimo uno di camera e uno
                     di effetto (elenco con giv presets); la frase si aggiunge al prompt
  --tutela <t>       clausola aggiunta coi preset: prodotto (default con --image),
                     illustrazione, no

giv presets:
  al massimo un preset di camera e uno di effetto; senza variante per la famiglia del
  modello vale la frase generica (con avviso; con --strict è un errore, codice 2).
  --negative si somma ai negative dei preset dove il modello li ha (kling, veo, wan).
  I file ./giv-presets.json e ~/.config/giv/presets.json aggiungono o sostituiscono
  preset (vince il primo)

Credenziali: GEMINI_API_KEY / OPENAI_API_KEY / XAI_API_KEY / OPENROUTER_API_KEY / FAL_KEY e GIV_PROVIDER
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
			video = "non supportato: usa gemini, openrouter o xai"
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
	return parsePromptIf(fs, args, nil)
}

// parsePromptIf è parsePrompt dove il prompt può mancare: optional, chiamata a
// flag letti, dice se è così (giv video con --shot); "" senza prompt.
func parsePromptIf(fs *flag.FlagSet, args []string, optional func() bool) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", usageError("%v", err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		if optional != nil && optional() {
			return "", nil
		}
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
	"fal":        "FAL_KEY",
}

// defaultProvider è il provider usato senza --provider: GIV_PROVIDER (env o file
// di configurazione, come le chiavi), altrimenti gemini.
func defaultProvider() string {
	if p := envProvider(); p != "" {
		return p
	}
	return "gemini"
}

func envProvider() string { return strings.TrimSpace(config.Get("GIV_PROVIDER")) }

// errNoVideoProvider: un video (a pagamento) non parte col solo default gemini,
// serve un provider scelto (--provider, storyboard o GIV_PROVIDER); senza,
// codice 2 come nella 0.9.1, quando gemini non aveva video.
func errNoVideoProvider(how string) error {
	return usageError("nessun provider scelto per il video: %s openrouter (default %s), xai (default %s), gemini (default %s) o fal (default %s), oppure imposta GIV_PROVIDER",
		how, defaultVideoModels["openrouter"], defaultVideoModels["xai"], defaultVideoModels["gemini"], defaultVideoModels["fal"])
}

func checkProvider(name string) error {
	if _, ok := envKeys[name]; !ok {
		return usageError("provider sconosciuto: %s (validi: gemini, openai, xai, openrouter, fal)", name)
	}
	return nil
}

// baseURLs sono gli endpoint dei provider OpenAI-compatibili e dei video
// gemini (i test li puntano a un server finto).
var baseURLs = map[string]string{
	"gemini":     "https://generativelanguage.googleapis.com/v1beta",
	"openai":     "https://api.openai.com/v1",
	"xai":        "https://api.x.ai/v1",
	"openrouter": "https://openrouter.ai/api/v1",
}

// falConfig sono le origini di fal (coda e storage); i test le puntano a un
// server finto.
var falConfig = fal.Config{Queue: fal.QueueURL, REST: fal.RESTURL}

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
		c := gemini.New(key)
		c.Base = baseURLs["gemini"]
		return c, nil
	case "fal":
		return fal.New(key, falConfig)
	case "openai":
		return openaicompat.New("openai", baseURLs["openai"], key,
			openaicompat.Options{UseSize: true}), nil
	case "xai":
		return openaicompat.New("xai", baseURLs["xai"], key,
			openaicompat.Options{B64Param: true, Grok: true}), nil
	default: // openrouter
		return openaicompat.New("openrouter", baseURLs["openrouter"], key,
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

// resolveVideoModel è resolveModel per i video, col caso senza video spiegato:
// openai non ha un'API video, anche con --model.
func resolveVideoModel(flagValue, providerName string) (string, error) {
	switch {
	case providerName == "openai":
		return "", usageError("openai non ha un'API video: usa --provider openrouter (default %s), --provider xai (default %s) o --provider gemini (default %s)",
			defaultVideoModels["openrouter"], defaultVideoModels["xai"], defaultVideoModels["gemini"])
	case providerName == "fal" && flagValue != "" && !fal.HasCard(flagValue):
		// niente endpoint liberi: senza scheda il prezzo non sarebbe noto
		return "", usageError("fal: scheda %q sconosciuta (schede: %s)", flagValue, strings.Join(fal.Cards(), ", "))
	}
	return resolveModel(flagValue, providerName, defaultVideoModels)
}

func cmdModels(args []string) error {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	prov := fs.String("provider", defaultProvider(), "provider")
	all := fs.Bool("all", false, "includi anche i modelli non image/video")
	kind := fs.String("kind", "", "solo i modelli di un tipo: image, video, other")
	withCaps := fs.Bool("caps", false, "capacità e prezzo al secondo dei modelli video (con --kind video)")
	asJSON := fs.Bool("json", false, "output JSON")
	fs.Parse(args)
	switch *kind {
	case "", "image", "video", "other":
	default:
		return usageError("--kind accetta image, video o other, non %q", *kind)
	}
	if *withCaps {
		if *kind != "video" {
			return usageError("--caps vale con --kind video")
		}
		if err := checkProvider(*prov); err != nil {
			return err
		}
		if _, ok := defaultVideoModels[*prov]; !ok {
			return usageError("--caps: nessun modello video con capacità note su %s (usa gemini, xai o openrouter)", *prov)
		}
	}
	c, err := newProvider(*prov)
	if err != nil {
		return err
	}
	if *withCaps {
		return printVideoCaps(c, *asJSON)
	}
	models, err := c.Models(context.Background())
	if err != nil {
		return err
	}
	if *kind != "" || !*all {
		var f []provider.ModelInfo
		for _, m := range models {
			if m.Kind == *kind || *kind == "" && (m.Kind == "image" || m.Kind == "video") {
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

// capsRow è un modello video con capacità e intervallo di prezzo al secondo.
type capsRow struct {
	caps.Model
	USDPerSecMin *float64 `json:"usd_per_s_min,omitempty"`
	USDPerSecMax *float64 `json:"usd_per_s_max,omitempty"`
}

func printVideoCaps(c provider.Provider, asJSON bool) error {
	vc, ok := c.(videoCapser)
	if !ok {
		return usageError("capacità video non disponibili per %s", c.Name())
	}
	catalog, err := vc.VideoCaps(context.Background())
	if err != nil {
		return err
	}
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].ID < catalog[j].ID })
	rows := make([]capsRow, len(catalog))
	for i, m := range catalog {
		rows[i].Model = m
		if lo, hi, ok := m.PerSecond(); ok {
			rows[i].USDPerSecMin, rows[i].USDPerSecMax = &lo, &hi
		}
	}
	if asJSON {
		return output.PrintJSON(rows)
	}
	yesNo := func(b *bool) string {
		switch {
		case b == nil:
			return "?"
		case *b:
			return "sì"
		}
		return "no"
	}
	fmt.Printf("%-40s %-10s %-20s %-22s %-5s %-5s %s\n", "modello", "durate", "risoluzioni", "fotogrammi", "audio", "seed", "$/s")
	for _, r := range rows {
		price := "-"
		if r.USDPerSecMin != nil {
			price = fmt.Sprintf("%.4g", *r.USDPerSecMin)
			if *r.USDPerSecMax != *r.USDPerSecMin {
				price += fmt.Sprintf("-%.4g", *r.USDPerSecMax)
			}
		}
		fmt.Printf("%-40s %-10s %-20s %-22s %-5s %-5s %s\n", r.ID, durationsText(r.Durations),
			orDash(strings.Join(r.Resolutions, ",")), orDash(strings.Join(r.Frames, ",")),
			yesNo(r.Audio), yesNo(r.Seed), price)
	}
	return nil
}

// durationsText scrive le durate come intervallo (3-15) se sono consecutive.
func durationsText(d []int) string {
	if len(d) == 0 {
		return "-"
	}
	s := slices.Clone(d)
	slices.Sort(s)
	consecutive := true
	for i := 1; i < len(s); i++ {
		if s[i] != s[i-1]+1 {
			consecutive = false
		}
	}
	if consecutive && len(s) > 2 {
		return fmt.Sprintf("%d-%d", s[0], s[len(s)-1])
	}
	parts := make([]string, len(s))
	for i, n := range s {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ",")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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
	job, err := prepareImage(imageOptions{
		Provider: *prov, Model: *model, Prompt: prompt, Aspect: *aspect,
		N: *n, Seed: *seed, Inputs: inputs, Out: *out, Name: *name,
	})
	if err != nil {
		return err
	}
	man, err := job.run(context.Background())
	if err != nil {
		return err
	}
	return emit("image", man)
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
	lastFrame := fs.String("last-frame", "", "ultimo fotogramma (file o URL)")
	var refs, params stringList
	fs.Var(&refs, "ref", "immagine di riferimento, file o URL (ripetibile)")
	audio := fs.String("audio", "", "audio nativo: on o off (default del modello)")
	seed := fs.Int("seed", 0, "seed (dove il modello lo rispetta)")
	fs.Var(&params, "param", "parametro passante del modello chiave=valore (ripetibile)")
	timeout := fs.Duration("timeout", 15*time.Minute, "tetto di attesa del job (es. 20m)")
	dryRun := fs.Bool("dry-run", false, "nessuna generazione: stampa parametri e costo stimato")
	maxCost := fs.Float64("max-cost", 0, "rifiuta se il costo stimato supera questi dollari")
	strict := fs.Bool("strict", false, "anche i controlli non supportati di rifinitura sono errori")
	var presetNames stringList
	fs.Var(&presetNames, "preset", "preset di camera o effetto (ripetibile)")
	protect := fs.String("tutela", "", "clausola di tutela coi preset: prodotto, illustrazione, no")
	var keyframes, shotList stringList
	fs.Var(&keyframes, "keyframe", "fotogramma intermedio, file o URL (ripetibile, in ordine)")
	fs.Var(&shotList, "shot", `inquadratura "<secondi>:<testo>" (ripetibile): niente prompt, durata = somma`)
	// con --shot il testo è nelle inquadrature: il prompt può mancare
	prompt, err := parsePromptIf(fs, args, func() bool { return len(shotList) > 0 })
	if err != nil {
		return err
	}
	shots, err := parseShots(shotList)
	if err != nil {
		return err
	}
	// --max-cost: tetto solo se il flag c'è (anche 0)
	var costCap *float64
	chosen := envProvider() != ""
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "max-cost":
			costCap = maxCost
		case "provider":
			chosen = true
		}
	})
	if !chosen {
		return errNoVideoProvider("usa --provider")
	}
	job, err := prepareVideo(videoOptions{
		Provider: *prov, Model: *model, Prompt: prompt,
		Aspect: *aspect, Resolution: *resolution, Negative: *negative,
		Image: *image, LastFrame: *lastFrame, Refs: refs, Keyframes: keyframes, Shots: shots,
		Duration: *duration, Seed: *seed, Audio: *audio, Params: params, Timeout: *timeout,
		Presets: presetNames, Protect: *protect,
		MaxCost: costCap, Strict: *strict, DryRun: *dryRun,
		Out: *out, Name: *name,
	})
	if err != nil {
		return err
	}
	if *dryRun {
		return output.PrintJSON(job.plan)
	}
	man, err := job.run(context.Background())
	if err != nil {
		return err
	}
	return emit("video", man)
}

// videoPlan è l'esito dei controlli prima di spendere, stampato da --dry-run.
type videoPlan struct {
	DryRun      bool           `json:"dry_run"`
	Provider    string         `json:"provider"`
	Model       string         `json:"model"`
	Prompt      string         `json:"prompt"`
	PromptFinal string         `json:"prompt_final"`
	Params      map[string]any `json:"params,omitempty"`
	Inputs      map[string]any `json:"inputs,omitempty"`
	CostEstUSD  *float64       `json:"cost_est_usd"`
	CostSource  string         `json:"cost_source,omitempty"`
	Warnings    []string       `json:"warnings,omitempty"`

	costExact *float64 // stima non arrotondata, per i tetti (giv story run)
}

type videoChecks struct {
	strict  bool
	maxCost *float64 // nil = nessun tetto
	dryRun  bool
}

// videoCapser è un provider che conosce le capacità dei suoi modelli video.
type videoCapser interface {
	VideoCaps(ctx context.Context) ([]caps.Model, error)
}

// checkVideo controlla req su capacità e prezzi del modello (regole in
// internal/caps), applica --max-cost e prepara il piano di --dry-run.
// Gli avvisi vanno su stderr.
func checkVideo(c provider.Provider, req *provider.VideoRequest, opt videoChecks) (*videoPlan, error) {
	plan := &videoPlan{DryRun: true, Provider: c.Name(), Model: req.Model, Prompt: req.Prompt, PromptFinal: req.Prompt}
	warn := func(msg string) {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", msg)
		plan.Warnings = append(plan.Warnings, msg)
	}
	var m *caps.Model
	if vc, ok := c.(videoCapser); ok {
		catalog, err := vc.VideoCaps(context.Background())
		if err != nil {
			if opt.strict {
				return nil, err
			}
			warn(fmt.Sprintf("capacità di %s non lette (%v): controlli e stima non fatti", c.Name(), err))
		} else {
			m = caps.Find(catalog, req.Model)
			warns, err := caps.Check(c.Name(), m, catalog, req, opt.strict)
			if err != nil {
				return nil, usageError("%v", err)
			}
			for _, w := range warns {
				warn(w)
			}
		}
	}
	// senza capacità note nessun provider li invierebbe: rifiuto, non avviso
	if m == nil && (len(req.Keyframes) > 0 || len(req.Shots) > 0) {
		return nil, usageError("--keyframe e --shot valgono solo coi modelli che li dichiarano, capacità di %s/%s non note (giv models --provider fal --kind video --caps)", c.Name(), req.Model)
	}
	why := "capacità del modello non note"
	var est *caps.Estimate
	if m != nil {
		est, why = m.Estimate(*req)
	}
	if est != nil && (math.IsNaN(est.Exact) || math.IsInf(est.Exact, 0)) {
		est, why = nil, "stima non valida"
	}
	if est != nil {
		plan.CostEstUSD, plan.CostSource, plan.costExact = &est.USD, est.Source, &est.Exact
	}
	if opt.maxCost != nil {
		if est == nil {
			return nil, usageError("--max-cost: costo non stimabile (%s)", why)
		}
		// un default del provider senza tariffa può costare più della stima
		if est.Uncovered != "" {
			return nil, usageError("--max-cost: costo non stimabile (%s)", est.Uncovered)
		}
		// il tetto vale sul valore non arrotondato
		if est.Exact > *opt.maxCost {
			return nil, usageError("costo stimato %.4f $ oltre --max-cost %.4f $ (%s)", est.USD, *opt.maxCost, est.Source)
		}
	}
	if opt.dryRun && plan.CostEstUSD == nil {
		warn("stima del costo non disponibile: " + why)
	}

	params := map[string]any{}
	if req.Duration > 0 {
		params["duration"] = req.Duration
	}
	for k, v := range map[string]string{"resolution": req.Resolution, "aspect": req.Aspect, "negative": req.Negative} {
		if v != "" {
			params[k] = v
		}
	}
	if req.Seed != 0 {
		params["seed"] = req.Seed
	}
	if req.Audio != nil {
		params["audio"] = map[bool]string{true: "on", false: "off"}[*req.Audio]
	}
	if len(req.Params) > 0 {
		params["passthrough"] = req.Params
	}
	if len(req.Shots) > 0 {
		params["shots"] = req.Shots
	}
	if len(params) > 0 {
		plan.Params = params
	}
	// gli URL firmati compaiono solo con schema e host (log CI)
	inputs := map[string]any{}
	if req.Image != "" {
		inputs["image"] = shownInput(req.Image)
	}
	if req.LastFrame != "" {
		inputs["last_frame"] = shownInput(req.LastFrame)
	}
	if len(req.Refs) > 0 {
		refs := make([]string, len(req.Refs))
		for i, r := range req.Refs {
			refs[i] = shownInput(r)
		}
		inputs["refs"] = refs
	}
	if len(req.Keyframes) > 0 {
		kf := make([]string, len(req.Keyframes))
		for i, k := range req.Keyframes {
			kf[i] = shownInput(k)
		}
		inputs["keyframes"] = kf
	}
	if len(inputs) > 0 {
		plan.Inputs = inputs
	}
	return plan, nil
}

// parseAudio legge --audio: vuoto = default del modello.
func parseAudio(v string) (*bool, error) {
	switch v {
	case "":
		return nil, nil
	case "on":
		on := true
		return &on, nil
	case "off":
		off := false
		return &off, nil
	}
	return nil, usageError("--audio accetta on oppure off")
}

// parseParams legge i --param chiave=valore: il valore vale come JSON se lo è
// (numeri, true/false, oggetti), altrimenti come stringa.
func parseParams(list []string) (map[string]any, error) {
	if len(list) == 0 {
		return nil, nil
	}
	out := make(map[string]any, len(list))
	// negli errori la posizione, non il valore: potrebbe essere riservato
	for i, kv := range list {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, usageError("--param n. %d: serve la forma chiave=valore", i+1)
		}
		if !validParamKey(k) {
			return nil, usageError("--param n. %d: chiave non valida (lettere, cifre e _, inizia con una lettera)", i+1)
		}
		if _, dup := out[k]; dup {
			return nil, usageError("--param %s ripetuto", k)
		}
		dec := json.NewDecoder(strings.NewReader(v))
		dec.UseNumber()
		var val any
		if err := dec.Decode(&val); err != nil || dec.More() {
			val = v
		}
		out[k] = val
	}
	return out, nil
}

func validParamKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '_'):
		default:
			return false
		}
	}
	return true
}

// logEntry è una riga di giv-log.jsonl: ts, poi il manifest (command compreso).
type logEntry struct {
	Ts string `json:"ts"`
	manifest
}

// emit stampa il manifest su stdout e lo appende al registro locale.
func emit(command string, man manifest) error {
	man.Command = command
	logRun(man)
	return output.PrintJSON(man)
}

// logRun appende una riga al registro locale; un errore è solo un avviso.
func logRun(man manifest) {
	entry := logEntry{Ts: time.Now().Format(time.RFC3339), manifest: man}
	if err := output.AppendLog(logFile, entry); err != nil {
		fmt.Fprintf(os.Stderr, "avviso: registro %s non aggiornato: %v\n", logFile, err)
	}
}

func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	n := fs.Int("n", 20, "numero di righe da mostrare")
	sum := fs.Bool("sum-cost", false, "totale generazioni e costo registrato")
	by := fs.String("by", "", "con --sum-cost: totali anche per provider o model")
	asJSON := fs.Bool("json", false, "righe raw JSONL")
	fs.Parse(args)
	if *by != "" && *by != "provider" && *by != "model" {
		return usageError("--by accetta provider oppure model, non %q", *by)
	}
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
	if *sum || *by != "" {
		return output.PrintJSON(sumLog(lines, *by))
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
		switch {
		case e.CostUSD > 0:
			cost = fmt.Sprintf("  $%.4f", e.CostUSD)
		case e.CostEstUSD > 0:
			cost = fmt.Sprintf("  ~$%.4f", e.CostEstUSD) // stimato
		}
		file0 := ""
		if len(e.Files) > 0 {
			file0 = e.Files[0].Path
		}
		if e.Status != "" {
			file0 = e.Status + " job " + e.JobID
		}
		fmt.Printf("%s  %-5s  %s/%s%s  %s\n", e.Ts, e.Command, e.Provider, e.Model, cost, file0)
	}
	return nil
}
