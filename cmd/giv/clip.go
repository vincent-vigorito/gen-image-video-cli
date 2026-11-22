package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/provider"
)

// Comandi sulle clip esistenti (giv extend, edit, upscale, avatar): lo stesso
// giro di giv video. Controlli a codice 2 prima di chiave e chiamate (flag,
// file, misure, capacità), stima, --dry-run e --max-cost, avvio mai ripetuto,
// attesa, manifest e registro (giv-log.jsonl, giv-jobs.jsonl).

// clipSpec descrive un comando sulle clip.
type clipSpec struct {
	Op        string   // nome del comando e dell'operazione: extend, edit, upscale, avatar
	Providers []string // provider che la fanno; il primo vale senza --provider
}

// clipOpTables sono le tabelle delle operazioni sulle clip per provider
// (internal/caps), lette senza chiave né chiamate: ogni provider aggiunge la sua.
var clipOpTables = map[string]func() []caps.Op{}

// clipFlags sono i flag dei comandi sulle clip oltre a quelli comuni: quali
// valgono lo decide la tabella dell'operazione (caps.Op.Flags).
var clipFlags = []string{"video", "from", "image", "audio", "duration", "factor", "mode"}

// clipRefused sono flag di giv video che i comandi sulle clip accettano solo
// per rifiutarli col motivo (codice 2) se la tabella non li ammette.
var clipRefused = []string{"resolution", "aspect", "seed", "negative", "ref", "last-frame", "param"}

// clipOptions sono i parametri di un comando sulle clip, nella forma dei flag.
type clipOptions struct {
	Provider, Model, Prompt   string
	Video, From, Image, Audio string
	Duration                  int
	Factor                    float64
	Mode                      string
	Timeout                   time.Duration
	MaxCost                   *float64 // nil = nessun tetto
	Strict, DryRun            bool
	Out, Name                 string
	Set                       []string // flag indicati fra clipFlags e clipRefused, "prompt" se c'è

	client provider.Provider // già creato (test); nil = newProvider all'avvio
}

// cmdClip è un comando sulle clip: flag, controlli, --dry-run o generazione.
func cmdClip(spec clipSpec, args []string) error {
	o, err := parseClipFlags(spec, args)
	if err != nil {
		return err
	}
	return runClip(spec, o)
}

// runClip stampa il piano (--dry-run) o genera ed emette il manifest.
func runClip(spec clipSpec, o clipOptions) error {
	job, err := prepareClip(spec, o)
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
	return emit(spec.Op, man)
}

// parseClipFlags legge i flag di un comando sulle clip; il prompt è facoltativo
// (lo chiede la tabella dell'operazione).
func parseClipFlags(spec clipSpec, args []string) (clipOptions, error) {
	o := clipOptions{}
	fs := flag.NewFlagSet(spec.Op, flag.ExitOnError)
	fs.StringVar(&o.Provider, "provider", spec.Providers[0], "provider: "+strings.Join(spec.Providers, ", "))
	fs.StringVar(&o.Model, "model", "", "modello (default: il primo del provider)")
	fs.StringVar(&o.Video, "video", "", "clip sorgente: file mp4 o URL https")
	fs.StringVar(&o.From, "from", "", "job di partenza (video generato da giv)")
	fs.StringVar(&o.Image, "image", "", "immagine (file o URL)")
	fs.StringVar(&o.Audio, "audio", "", "audio wav o mp3 (file locale)")
	fs.IntVar(&o.Duration, "duration", 0, "secondi aggiunti")
	fs.Float64Var(&o.Factor, "factor", 0, "fattore di ingrandimento")
	fs.StringVar(&o.Mode, "mode", "", "modalità")
	for _, n := range clipRefused {
		fs.Var(&stringList{}, n, "non ammesso dai comandi sulle clip")
	}
	fs.StringVar(&o.Out, "out", "out", "directory di output")
	fs.StringVar(&o.Name, "name", "", "slug per i nomi file")
	fs.DurationVar(&o.Timeout, "timeout", 15*time.Minute, "tetto di attesa del job (es. 20m)")
	fs.BoolVar(&o.DryRun, "dry-run", false, "nessuna generazione: stampa parametri e costo stimato")
	maxCost := fs.Float64("max-cost", 0, "rifiuta se il costo stimato supera questi dollari")
	fs.BoolVar(&o.Strict, "strict", false, "anche i controlli di rifinitura sono errori")
	prompt, err := parseOptionalPrompt(fs, args)
	if err != nil {
		return o, err
	}
	if strings.TrimSpace(prompt) != "" {
		o.Prompt = prompt
		o.Set = append(o.Set, "prompt")
	}
	fs.Visit(func(f *flag.Flag) {
		switch {
		case f.Name == "max-cost":
			o.MaxCost = maxCost
		case slices.Contains(clipFlags, f.Name), slices.Contains(clipRefused, f.Name):
			o.Set = append(o.Set, f.Name)
		}
	})
	return o, nil
}

// parseOptionalPrompt è parsePrompt con il prompt facoltativo.
func parseOptionalPrompt(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", usageError("%v", err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return "", nil
	}
	if len(rest) > 1 {
		if err := fs.Parse(rest[1:]); err != nil {
			return "", usageError("%v", err)
		}
		if len(fs.Args()) > 0 {
			return "", usageError("passa il prompt come singolo argomento tra virgolette")
		}
	}
	return rest[0], nil
}

// clipSourceInfo sono le misure del sorgente nel piano di --dry-run.
type clipSourceInfo struct {
	DurationS      float64 `json:"duration_s,omitempty"`
	Width          int     `json:"width,omitempty"`
	Height         int     `json:"height,omitempty"`
	AudioDurationS float64 `json:"audio_duration_s,omitempty"`
}

// clipPlan è l'esito dei controlli prima di spendere, stampato da --dry-run.
type clipPlan struct {
	DryRun     bool            `json:"dry_run"`
	Command    string          `json:"command"`
	Provider   string          `json:"provider"`
	Model      string          `json:"model"`
	Prompt     string          `json:"prompt,omitempty"`
	Params     map[string]any  `json:"params,omitempty"`
	Inputs     *manifestInputs `json:"inputs,omitempty"`
	Source     *clipSourceInfo `json:"source,omitempty"`
	CostEstUSD *float64        `json:"cost_est_usd"`
	CostSource string          `json:"cost_source,omitempty"`
	Warnings   []string        `json:"warnings,omitempty"`

	costExact *float64 // stima non arrotondata, per i tetti
}

// clipJob è un comando sulle clip controllato e pronto a partire.
type clipJob struct {
	c         provider.Provider // nil = creato all'avvio (la chiave serve solo lì)
	provider  string
	req       provider.ClipRequest
	op        *caps.Op
	plan      *clipPlan
	out, name string
}

// prepareClip controlla tutto senza spendere e senza chiave: provider, flag
// ammessi dalla tabella dell'operazione, valori, file in ingresso, misure con
// ffprobe, stima e --max-cost (codice 2).
func prepareClip(spec clipSpec, o clipOptions) (*clipJob, error) {
	if !slices.Contains(spec.Providers, o.Provider) {
		if err := checkProvider(o.Provider); err != nil {
			return nil, err
		}
		return nil, usageError("giv %s non disponibile con %s (provider: %s)", spec.Op, o.Provider, strings.Join(spec.Providers, ", "))
	}
	if o.Timeout <= 0 {
		return nil, usageError("--timeout deve essere una durata positiva (es. 20m)")
	}
	if o.MaxCost != nil && (math.IsNaN(*o.MaxCost) || math.IsInf(*o.MaxCost, 0) || *o.MaxCost < 0) {
		return nil, usageError("--max-cost deve essere un importo finito e non negativo in dollari")
	}
	// uno zero esplicito non vale come "default": si rifiuta
	if o.Duration < 0 || o.Duration == 0 && slices.Contains(o.Set, "duration") {
		return nil, usageError("--duration deve essere un intero positivo di secondi")
	}
	if math.IsNaN(o.Factor) || math.IsInf(o.Factor, 0) || o.Factor < 0 || o.Factor == 0 && slices.Contains(o.Set, "factor") {
		return nil, usageError("--factor deve essere un numero finito e positivo")
	}
	if o.From != "" && !validJobID(o.From) {
		return nil, usageError("--from: id del job non valido: %q", o.From)
	}
	req := provider.ClipRequest{
		Op: spec.Op, Model: o.Model, Prompt: o.Prompt,
		Video: o.Video, From: o.From, Image: o.Image, Audio: o.Audio,
		Duration: o.Duration, Factor: o.Factor, Mode: o.Mode, Timeout: o.Timeout,
	}
	var ops []caps.Op
	if table := clipOpTables[o.Provider]; table != nil {
		ops = table()
	}
	op := caps.FindOp(ops, spec.Op, o.Model)
	if err := caps.CheckOpFlags(o.Provider, op, ops, req, o.Set); err != nil {
		return nil, usageError("%v", err)
	}
	req.Model = op.Model
	if op.VideoURLOnly && req.Video != "" && !isURL(req.Video) {
		return nil, usageError("--video %s: %s", req.Video, localVideoRefusal(spec, o.Provider))
	}

	// file in ingresso: solo quelli ammessi dalla tabella arrivano qui
	for _, in := range []struct {
		path  string
		check func(string) error
	}{{req.Video, checkClipVideo}, {req.Audio, checkClipAudio}, {req.Image, checkClipImage}} {
		if in.path == "" {
			continue
		}
		if err := in.check(in.path); err != nil {
			return nil, err
		}
	}
	plan := &clipPlan{DryRun: true, Command: spec.Op, Provider: o.Provider, Model: req.Model, Prompt: o.Prompt}
	warn := func(msg string) {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", msg)
		plan.Warnings = append(plan.Warnings, msg)
	}
	warns, err := probeClip(&req)
	if err != nil {
		return nil, err
	}
	srcWarns, err := caps.CheckOpSource(o.Provider, op, req, o.Strict)
	if err != nil {
		return nil, usageError("%v", err)
	}
	for _, w := range append(warns, srcWarns...) {
		warn(w)
	}

	est, why := op.Estimate(req)
	if est != nil {
		plan.CostEstUSD, plan.CostSource, plan.costExact = &est.USD, est.Source, &est.Exact
	}
	if o.MaxCost != nil {
		if est == nil {
			return nil, usageError("--max-cost: costo non stimabile (%s)", why)
		}
		// il tetto vale sul valore non arrotondato
		if est.Exact > *o.MaxCost {
			return nil, usageError("costo stimato %.4f $ oltre --max-cost %.4f $ (%s)", est.USD, *o.MaxCost, est.Source)
		}
	}
	if o.DryRun && est == nil {
		warn("stima del costo non disponibile: " + why)
	}
	plan.Params = clipParams(req)
	plan.Inputs = newClipInputs(req)
	if s := req.Source; s != (provider.ClipSource{}) {
		plan.Source = &clipSourceInfo{DurationS: s.Duration, Width: s.Width, Height: s.Height, AudioDurationS: s.AudioDuration}
	}
	return &clipJob{c: o.client, provider: o.Provider, req: req, op: op, plan: plan, out: o.Out, name: o.Name}, nil
}

// localVideoRefusal è il motivo del rifiuto di un --video locale con prov, che
// vuole la clip da URL: le vie sono un URL https pubblico o un altro provider
// del comando che carica il file.
func localVideoRefusal(spec clipSpec, prov string) string {
	msg := fmt.Sprintf("giv %s con %s accetta solo un indirizzo https pubblico, non un file locale: pubblica la clip e passa l'URL", spec.Op, prov)
	for _, p := range spec.Providers {
		if table := clipOpTables[p]; p != prov && table != nil {
			if op := caps.FindOp(table(), spec.Op, ""); op != nil && !op.VideoURLOnly {
				msg += ", oppure usa --provider " + p
			}
		}
	}
	return msg
}

// clipParams sono i valori indicati dall'utente, per piano e manifest.
func clipParams(req provider.ClipRequest) map[string]any {
	params := map[string]any{}
	if req.Duration > 0 {
		params["duration"] = req.Duration
	}
	if req.Factor > 0 {
		params["factor"] = req.Factor
	}
	if req.Mode != "" {
		params["mode"] = req.Mode
	}
	if len(params) == 0 {
		return nil
	}
	return params
}

// newClipInputs sono gli ingressi come li ha passati l'utente (gli URL ridotti
// a schema e host); nil se non ce ne sono.
func newClipInputs(req provider.ClipRequest) *manifestInputs {
	in := &manifestInputs{From: req.From}
	if req.Video != "" {
		in.Video = &inputFile{Path: shownInput(req.Video)}
	}
	if req.Audio != "" {
		in.Audio = &inputFile{Path: shownInput(req.Audio)}
	}
	if req.Image != "" {
		in.Image = &inputFile{Path: shownInput(req.Image)}
	}
	if in.Video == nil && in.Audio == nil && in.Image == nil && in.From == "" {
		return nil
	}
	return in
}

// run scarica l'immagine da URL, avvia l'operazione e salva i file;
// restituisce il manifest da emettere. Un job partito e poi fallito lascia la
// sua riga nel registro.
func (j *clipJob) run(ctx context.Context) (manifest, error) {
	c := j.c
	if c == nil {
		var err error
		if c, err = newProvider(j.provider); err != nil {
			return manifest{}, err
		}
	}
	g, ok := c.(provider.ClipGenerator)
	if !ok {
		return manifest{}, usageError("giv %s non disponibile con %s", j.req.Op, j.provider)
	}
	req := j.req
	var tmp tempInputs
	defer tmp.cleanup()
	if isURL(req.Image) {
		local, err := tmp.resolve(req.Image)
		if err != nil {
			return manifest{}, err
		}
		if _, err := provider.CheckImage(local); err != nil {
			return manifest{}, usageError("--image %s: %v", redactURL(req.Image), err)
		}
		req.Image = local
	}
	man := j.manifest(req)
	fmt.Fprintf(os.Stderr, "giv %s con %s/%s…\n", req.Op, j.provider, req.Model)
	start := time.Now()
	res, err := g.GenerateClip(ctx, req)
	if err != nil {
		logFailedJob(man, err, time.Since(start) >= req.Timeout)
		return manifest{}, err
	}
	slug := output.Slug(j.name)
	if j.name == "" {
		slug = output.Slug(req.Prompt)
		if slug == "" {
			slug = output.Slug(req.Op)
		}
	}
	files, err := output.SaveAll(j.out, slug, res.Media)
	if err != nil {
		return manifest{}, err
	}
	man.done(files, res)
	return man, nil
}

// manifest prepara il manifest prima dell'avvio (serve anche alla riga di
// registro di un job fallito): ingressi con l'impronta dei file locali.
func (j *clipJob) manifest(req provider.ClipRequest) manifest {
	man := manifest{Command: req.Op, Provider: j.provider, Model: req.Model, Prompt: req.Prompt,
		Inputs: newClipInputs(j.req), Params: j.plan.Params, Warnings: slices.Clone(j.plan.Warnings)}
	if in := man.Inputs; in != nil {
		for _, f := range []struct {
			in    *inputFile
			path  string
			limit int64
		}{{in.Video, req.Video, provider.MaxVideoBytes}, {in.Audio, req.Audio, provider.MaxAudioBytes}, {in.Image, req.Image, provider.MaxInputBytes}} {
			if f.in != nil && !isURL(f.path) {
				f.in.SHA256 = clipHash(f.path, f.limit)
			}
		}
	}
	if j.plan.CostEstUSD != nil {
		man.CostEstUSD, man.CostSource = *j.plan.CostEstUSD, j.plan.CostSource
	}
	return man
}
