package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/kit"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/provider/gemini"
	"gen-image-video-cli/internal/provider/openaicompat"
	"gen-image-video-cli/internal/speech"
)

// giv voice: voce fuori campo da un testo, un WAV e (dove il provider li dà) i
// tempi per carattere. Controlli a codice 2 prima di chiave e chiamate
// (testo, voce, lingua, velocità, pronunce, tetto), stima dai caratteri,
// --dry-run e --max-cost, una sola richiesta mai ripetuta, manifest su stdout
// e riga di registro. Con un esito incerto (3xx, 5xx, rete, risposta non
// valida) la voce può essere stata addebitata: errore, nessun rilancio.

const voiceHelp = `giv voice — voce fuori campo da un testo (WAV), a pagamento

Uso:
  giv voice [flag] "<testo>"
  giv voice [flag] --text-file <file>
  giv voice --voices [--provider xai|gemini]   elenco delle voci del provider (gratuito)

Flag:
  --provider <p>         provider della voce: xai (default) o gemini
  --model <m>            modello (gemini: gemini-3.8-flash-tts default, gemini-3.8-flash-lite-tts;
                         xai non ne ha)
  --voice <nome>         voce del provider (default: xai eve, gemini Kore)
  --lang <codice>        lingua BCP-47 o auto (default it; gemini la rileva dal testo)
  --speed <n>            velocità da 0.7 a 1.5 (xai, default 1.0; gemini la ignora con un avviso)
  --style "<t>"          stile a parole (gemini; xai lo ignora con un avviso)
  --kit <kit.json>       predefiniti dal kit: blocco voce e pronunce (i flag vincono; con un
                         --provider diverso da quello del kit valgono solo lingua e pronunce)
  --pronuncia <file>     pronunce JSON {"parola": "come dirla"}, sopra quelle del kit: al
                         massimo 200 voci, parola fino a 100 caratteri (lettere, cifre,
                         apostrofi, spazi), pronuncia fino a 128 (anche IPA fra barre)
  --text-file <file>     testo da un file UTF-8 (al posto del testo fra virgolette)
  --out <dir>            cartella di uscita (default out)
  --name <nome>          base dei nomi: <nome>.wav e <nome>.tempi.json (default dal testo)
  --dry-run              nessuna chiamata: stampa richiesta e costo stimato (JSON)
  --max-cost <usd>       rifiuta (codice 2) se il costo stimato supera il tetto o non è stimabile
                         (gemini sempre: il suo costo non ha un massimo garantito)
  --voices               elenca le voci invece di generare

Testo al massimo 15000 caratteri (gemini 6000, pronunce comprese). Prezzi: xai 15 $ per
milione di caratteri del testo inviato (le pronunce non cambiano il conto); gemini 0,50 $
per milione di token di testo e 9 $ (flash-lite 6 $) per milione di token audio, 25 al
secondo, il doppio dal 01/01/2027. Gemini non ha un massimo garantito: con --max-cost esce
con codice 2 (usa xai o togli il tetto); senza tetto parte con un avviso e una stima
probabile sul limite di token della richiesta (circa 0,15 $, 0,30 $ dal 2027); il costo
viene dai token riportati o dalla durata. Uscita: il WAV, i tempi per carattere se il provider li dà
(xai), e su stdout il JSON con file, durata misurata, caratteri e costo.
`

// voiceOptions sono i parametri di giv voice, nella forma dei flag.
type voiceOptions struct {
	Provider, Model, Voice   string
	Lang, Style              string
	Speed                    float64
	Kit, Pronuncia, TextFile string
	Text                     string
	Out, Name                string
	DryRun, ListVoices       bool
	MaxCost                  *float64 // nil = nessun tetto
	Set                      []string // flag indicati

	client speech.Synthesizer // già creato (test); nil = newSpeechProvider all'avvio
}

// voicePlan è l'uscita di --dry-run.
type voicePlan struct {
	DryRun     bool           `json:"dry_run"`
	Command    string         `json:"command"`
	Provider   string         `json:"provider"`
	Model      string         `json:"model"`
	Params     map[string]any `json:"params"`
	Request    map[string]any `json:"request"`
	CostEstUSD *float64       `json:"cost_est_usd"`
	CostSource string         `json:"cost_source,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
}

// voiceJob è una richiesta di voce controllata e pronta a partire.
type voiceJob struct {
	o    voiceOptions
	req  speech.Request
	est  *speech.Estimate
	plan *voicePlan
}

func cmdVoice(args []string) error {
	if len(args) > 0 && args[0] == "help" {
		fmt.Fprint(os.Stdout, voiceHelp)
		return nil
	}
	o, err := parseVoiceFlags(args)
	if err != nil {
		return err
	}
	if o.ListVoices {
		return listVoices(o)
	}
	job, err := prepareVoice(o)
	if err != nil {
		return err
	}
	if o.DryRun {
		return output.PrintJSON(job.plan)
	}
	man, err := job.run(context.Background())
	if err != nil {
		return err
	}
	return emit("voice", man)
}

// parseVoiceFlags legge i flag; il testo fra virgolette può mancare con
// --text-file o --voices.
func parseVoiceFlags(args []string) (voiceOptions, error) {
	var o voiceOptions
	fl := flag.NewFlagSet("voice", flag.ExitOnError)
	fl.Usage = func() { fmt.Fprint(fl.Output(), voiceHelp) }
	fl.StringVar(&o.Provider, "provider", "", "provider della voce (default xai, o quello del kit)")
	fl.StringVar(&o.Model, "model", "", "modello della voce (gemini)")
	fl.StringVar(&o.Kit, "kit", "", "kit di marca: voce e pronunce predefinite")
	fl.StringVar(&o.Voice, "voice", "", "voce del provider")
	fl.StringVar(&o.Lang, "lang", "", "lingua BCP-47 o auto")
	fl.Float64Var(&o.Speed, "speed", 0, "velocità da 0.7 a 1.5")
	fl.StringVar(&o.Style, "style", "", "stile a parole")
	fl.StringVar(&o.Pronuncia, "pronuncia", "", "file JSON delle pronunce")
	fl.StringVar(&o.TextFile, "text-file", "", "testo da un file")
	fl.StringVar(&o.Out, "out", "out", "cartella di uscita")
	fl.StringVar(&o.Name, "name", "", "base dei nomi dei file")
	fl.BoolVar(&o.DryRun, "dry-run", false, "nessuna chiamata: richiesta e costo stimato")
	maxCost := fl.Float64("max-cost", 0, "rifiuta se il costo stimato supera questi dollari")
	fl.BoolVar(&o.ListVoices, "voices", false, "elenco delle voci del provider")
	text, err := parsePromptIf(fl, args, func() bool { return o.TextFile != "" || o.ListVoices })
	if err != nil {
		if strings.Contains(err.Error(), "manca il prompt") {
			return o, usageError("manca il testo: giv voice \"<testo>\" oppure --text-file <file> (aiuto: giv voice help)")
		}
		return o, err
	}
	o.Text = text
	fl.Visit(func(f *flag.Flag) {
		o.Set = append(o.Set, f.Name)
		if f.Name == "max-cost" {
			o.MaxCost = maxCost
		}
	})
	return o, nil
}

// listVoices stampa le voci del provider (gratuito).
func listVoices(o voiceOptions) error {
	if o.Text != "" || o.TextFile != "" || o.DryRun {
		return usageError("--voices elenca le voci: niente testo, --text-file né --dry-run")
	}
	if slices.Contains(o.Set, "provider") && strings.TrimSpace(o.Provider) == "" {
		return usageError("--provider vuoto")
	}
	req, _, err := speech.Check(speech.Request{Provider: o.Provider, Text: "-"})
	if err != nil {
		return usageError("--voices: %v", err)
	}
	p, err := newProvider(req.Provider)
	if err != nil {
		return err
	}
	vl, ok := p.(speech.VoiceLister)
	if !ok {
		return usageError("--voices: elenco non disponibile con %s", req.Provider)
	}
	voices, err := vl.Voices(context.Background())
	if err != nil {
		return err
	}
	return output.PrintJSON(map[string]any{"provider": req.Provider, "voices": voices})
}

// prepareVoice fa tutti i controlli senza chiave né chiamate: testo, flag,
// pronunce, stima e tetto.
func prepareVoice(o voiceOptions) (*voiceJob, error) {
	if o.MaxCost != nil && (math.IsNaN(*o.MaxCost) || math.IsInf(*o.MaxCost, 0) || *o.MaxCost < 0) {
		return nil, usageError("--max-cost deve essere un importo finito e non negativo in dollari")
	}
	// uno zero esplicito non vale come "default" (decisione 15)
	if slices.Contains(o.Set, "speed") && o.Speed == 0 {
		return nil, usageError("--speed deve essere un numero da %.1f a %.1f", speech.MinSpeed, speech.MaxSpeed)
	}
	for _, f := range []struct{ name, val string }{{"voice", o.Voice}, {"lang", o.Lang}, {"provider", o.Provider}, {"model", o.Model}, {"kit", o.Kit}, {"pronuncia", o.Pronuncia}, {"text-file", o.TextFile}, {"out", o.Out}} {
		if slices.Contains(o.Set, f.name) && strings.TrimSpace(f.val) == "" {
			return nil, usageError("--%s vuoto", f.name)
		}
	}
	text := o.Text
	if o.TextFile != "" {
		if strings.TrimSpace(o.Text) != "" {
			return nil, usageError("testo fra virgolette e --text-file insieme: scegline uno")
		}
		data, err := speech.ReadSmallFile(o.TextFile, speech.MaxTextFileBytes)
		if err != nil {
			return nil, usageError("--text-file %s: %v", o.TextFile, err)
		}
		text = string(data)
	}
	req := speech.Request{
		Provider: o.Provider, Model: o.Model, Voice: o.Voice, Language: o.Lang, Speed: o.Speed,
		Style: o.Style, Text: text,
	}
	var warns []string
	if o.Kit != "" {
		k, err := loadKit(o.Kit)
		if err != nil {
			return nil, err
		}
		var w string
		req, w = withKitVoice(req, k)
		if w != "" {
			warns = append(warns, w)
		}
	}
	if o.Pronuncia != "" {
		d, err := speech.ReadDictionaryFile(o.Pronuncia)
		if err != nil {
			return nil, usageError("--pronuncia %v", err)
		}
		req.Dict = req.Dict.Merge(d)
	}
	req, cw, err := speech.Check(req)
	if err != nil {
		return nil, usageError("%v", err)
	}
	warns = append(warns, cw...)
	plan := &voicePlan{DryRun: true, Command: "voice", Provider: req.Provider, Model: voiceModel(req),
		Params: voiceParams(o, req), Request: voiceBody(req)}
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
		plan.Warnings = append(plan.Warnings, w)
	}
	est, why := speech.EstimateCost(req, time.Now())
	if est != nil {
		plan.CostEstUSD, plan.CostSource = &est.USD, est.Source
	}
	if o.MaxCost != nil {
		// gemini: nessun massimo garantito, con un tetto non parte (decisione 22)
		if r := speech.CapRefusal(req.Provider); r != "" {
			return nil, usageError("--max-cost: costo non stimabile (%s)", r)
		}
		if est == nil {
			return nil, usageError("--max-cost: costo non stimabile (%s)", why)
		}
		// il tetto vale sul valore non arrotondato
		if est.Exact > *o.MaxCost {
			return nil, usageError("costo stimato %.6f $ oltre --max-cost %.6f $ (%s)", est.USD, *o.MaxCost, est.Source)
		}
	}
	var msg string
	switch {
	case est == nil:
		msg = "stima del costo non disponibile: " + why
	case est.Probable:
		msg = fmt.Sprintf("il costo della voce %s non ha un massimo garantito: stima probabile %.6f $ (%d token d'uscita al massimo nella richiesta)",
			req.Provider, est.USD, speech.MaxGeminiOutputTokens)
	}
	if msg != "" {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", msg)
		plan.Warnings = append(plan.Warnings, msg)
	}
	return &voiceJob{o: o, req: req, est: est, plan: plan}, nil
}

// withKitVoice mette in req i predefiniti del kit (blocco voce e pronunce)
// dove i flag non li danno. Il blocco voce vale per il provider del kit: con
// un --provider diverso valgono solo la lingua e le pronunce (avviso).
func withKitVoice(req speech.Request, k *kit.Kit) (speech.Request, string) {
	if len(k.Pronuncia) > 0 {
		req.Dict = speech.Dictionary(maps.Clone(k.Pronuncia))
	}
	v := k.Voce
	if v == nil {
		return req, ""
	}
	kp := v.Provider
	if kp == "" {
		kp = speech.DefaultProvider
	}
	if req.Provider == "" {
		req.Provider = kp
	}
	if req.Language == "" {
		req.Language = v.Lingua
	}
	if req.Provider != kp {
		return req, fmt.Sprintf("kit: la voce del kit è per %s, provider %s: del kit valgono solo lingua e pronunce", kp, req.Provider)
	}
	if req.Model == "" {
		req.Model = v.Modello
	}
	if req.Voice == "" {
		req.Voice = v.Voce
	}
	if req.Speed == 0 && v.Velocita != nil {
		req.Speed = *v.Velocita
	}
	if req.Style == "" {
		req.Style = v.Stile
	}
	return req, ""
}

// voiceBody è il corpo della richiesta al provider (anche nel piano).
func voiceBody(req speech.Request) map[string]any {
	if req.Provider == speech.Gemini {
		return gemini.VoiceBody(req)
	}
	return openaicompat.GrokVoiceBody(req)
}

// voiceModel è il modello per manifest e registro: xai non ne ha uno da
// scegliere, vale il nome dell'endpoint (tts).
func voiceModel(req speech.Request) string {
	if req.Model != "" {
		return req.Model
	}
	return "tts"
}

// voiceParams sono i valori della richiesta per piano e manifest.
func voiceParams(o voiceOptions, req speech.Request) map[string]any {
	p := map[string]any{"voice": req.Voice, "chars": len([]rune(req.Text))}
	if req.Provider == speech.XAI { // gemini rileva la lingua dal testo
		p["language"] = req.Language
	}
	if req.Speed != 0 {
		p["speed"] = req.Speed
	}
	if req.Style != "" {
		p["style"] = req.Style
	}
	if len(req.Dict) > 0 {
		p["pronunce"] = len(req.Dict)
	}
	if o.Kit != "" {
		p["kit"] = o.Kit
	}
	if o.Pronuncia != "" {
		p["pronuncia_file"] = o.Pronuncia
	}
	if o.TextFile != "" {
		p["text_file"] = o.TextFile
	}
	return p
}

// newSpeechProvider è il client della voce del provider (chiave e indirizzo
// come per gli altri comandi: baseURLs, che i test puntano a un server finto).
func newSpeechProvider(name string) (speech.Synthesizer, error) {
	p, err := newProvider(name)
	if err != nil {
		return nil, err
	}
	s, ok := p.(speech.Synthesizer)
	if !ok {
		return nil, usageError("voce non disponibile con %s", name)
	}
	return s, nil
}

// voiceError traduce gli errori della richiesta di voce: esito incerto =
// codice 5 con l'avviso che può essere stata addebitata; voce sconosciuta =
// codice 3. Gli altri come per gli altri comandi (chiave, credito, 429).
func voiceError(prov string, err error) error {
	var se *httpx.StartError
	if errors.As(err, &se) {
		return &cliError{code: exitAPI, msg: fmt.Sprintf("voce dall'esito incerto: %v — può essere stata addebitata, richiesta non ripetuta: controlla sul provider prima di rilanciare", se.Err)}
	}
	var he *httpx.Error
	if errors.As(err, &he) && he.Status == http.StatusNotFound {
		return &cliError{code: exitNotFound, msg: fmt.Sprintf("%v — voce sconosciuta? elenco con giv voice --voices --provider %s", err, prov)}
	}
	return err
}

// run genera la voce e scrive WAV e tempi; restituisce il manifest. Una voce
// dall'esito incerto lascia una riga failed nel registro (può essere stata
// addebitata).
func (j *voiceJob) run(ctx context.Context) (manifest, error) {
	c := j.o.client
	if c == nil {
		var err error
		if c, err = newSpeechProvider(j.req.Provider); err != nil {
			return manifest{}, err
		}
	}
	// la cartella prima della spesa: un errore qui non costa niente
	if err := os.MkdirAll(j.o.Out, 0o755); err != nil {
		return manifest{}, err
	}
	man := manifest{Command: "voice", Provider: j.req.Provider, Model: voiceModel(j.req), Prompt: j.req.Text,
		Params: j.plan.Params, Warnings: slices.Clone(j.plan.Warnings)}
	if j.est != nil {
		man.CostEstUSD, man.CostSource = j.est.USD, j.est.Source
	}
	fmt.Fprintf(os.Stderr, "giv voice con %s (voce %s, %d caratteri)…\n", j.req.Provider, j.req.Voice, len([]rune(j.req.Text)))
	res, err := c.Synthesize(ctx, j.req)
	if err != nil {
		var se *httpx.StartError
		if errors.As(err, &se) {
			failed := man
			failed.Status, failed.Files = "failed", []output.FileInfo{}
			logRun(failed)
		}
		return manifest{}, voiceError(j.req.Provider, err)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
		man.Warnings = append(man.Warnings, w)
	}
	var tf *speech.TimingsFile
	if res.Timings != nil {
		tf = &speech.TimingsFile{Provider: j.req.Provider, Text: j.req.Text, DurationS: res.DurationS, Chars: res.Timings}
	}
	files, err := writeVoiceFiles(j.o.Out, voiceBase(j.o.Name, j.req.Text), res, tf)
	if err != nil {
		return manifest{}, fmt.Errorf("voce generata (e addebitata) ma non salvata: %v", err)
	}
	man.Files, man.DurationS = files, res.DurationS
	man.Params = maps.Clone(man.Params) // il piano resta com'è
	maps.Copy(man.Params, map[string]any{"sample_rate": res.WAV.SampleRate, "channels": res.WAV.Channels, "bits": res.WAV.Bits})
	if res.CostUSD > 0 {
		man.CostUSD = res.CostUSD
	}
	if res.UsageUSD > 0 {
		man.CostUsageUSD, man.CostUsageSource = math.Round(res.UsageUSD*1e6)/1e6, res.UsageSource
		man.usageEstUSD = res.UsageUSD
	}
	return man, nil
}

// voiceBase è la base dei nomi: --name, altrimenti dal testo con data e ora.
func voiceBase(name, text string) string {
	if name != "" {
		return output.Slug(name)
	}
	slug := output.Slug(text)
	if slug == "media" {
		slug = "voce"
	}
	return slug + "-" + time.Now().Format("20060102-150405")
}

// writeVoiceFiles scrive i file della voce senza sovrascrivere: un nome preso
// dà <base>-2, <base>-3…
func writeVoiceFiles(dir, base string, res *speech.Result, tf *speech.TimingsFile) ([]output.FileInfo, error) {
	for n := 1; ; n++ {
		b := base
		if n > 1 {
			b = fmt.Sprintf("%s-%d", base, n)
		}
		files, err := speech.WriteFiles(dir, b, res, tf)
		if errors.Is(err, fs.ErrExist) && n < 100 {
			continue
		}
		return files, err
	}
}
