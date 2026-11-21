package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"time"

	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/presets"
	"gen-image-video-cli/internal/provider"
)

// Generazione riusabile: giv video e giv image la fanno in due passi, «prepara e
// controlla» (nessuna spesa) e «genera e salva»; giv story li richiama per ogni scena.

// videoOptions sono i parametri di una generazione video, nella forma dei flag di giv video.
type videoOptions struct {
	Provider, Model, Prompt      string
	Aspect, Resolution, Negative string
	Image, LastFrame             string
	Refs                         []string
	Duration, Seed               int
	Audio                        string   // on, off o "" (default del modello)
	Params                       []string // chiave=valore
	Timeout                      time.Duration
	Presets                      []string
	Protect                      string
	MaxCost                      *float64 // nil = nessun tetto
	Strict, DryRun               bool
	Out, Name                    string

	// per giv story: il primo fotogramma nasce prima della scena (fotogramma chiave,
	// continua_da), quindi solo capacità e preset; client già creato e riusato
	ImagePending bool
	client       provider.Provider
}

// videoJob è una generazione video controllata e pronta a partire.
type videoJob struct {
	c         provider.Provider
	provider  string // come indicato dall'utente
	prompt    string // dell'utente; quello coi preset è in req
	req       provider.VideoRequest
	pres      presets.Result
	plan      *videoPlan // controlli e stima (prompt dell'utente in Prompt)
	out, name string
}

// prepareVideo controlla o senza spendere: provider, modello, preset, flag, ingressi,
// capacità e costo (codice 2 prima di chiave, download e chiamate dove possibile).
func prepareVideo(o videoOptions) (*videoJob, error) {
	// provider e modello prima delle credenziali: gli errori video (openai, gemini
	// senza --model) escono senza chiave e senza chiamate di rete
	if err := checkProvider(o.Provider); err != nil {
		return nil, err
	}
	m, err := resolveVideoModel(o.Model, o.Provider)
	if err != nil {
		return nil, err
	}
	// preset: prompt e negative inviati al provider (codice 2 prima di chiave e chiamate);
	// il manifest tiene il prompt dell'utente
	pres, err := videoPresets(o.Presets, o.Protect, o.Prompt, o.Negative, m,
		presets.Inputs{FirstFrame: o.Image != "", LastFrame: o.LastFrame != "", Duration: o.Duration})
	if err != nil {
		return nil, err
	}
	// controlli dei flag nuovi: codice 2 prima di chiave, download e chiamate
	req := provider.VideoRequest{
		Prompt: pres.Prompt, Model: m, Aspect: o.Aspect,
		Resolution: o.Resolution, Negative: pres.Negative,
		Duration: o.Duration, Seed: o.Seed, Timeout: o.Timeout,
	}
	if req.Audio, err = parseAudio(o.Audio); err != nil {
		return nil, err
	}
	if req.Params, err = parseParams(o.Params); err != nil {
		return nil, err
	}
	if o.Seed < 0 {
		return nil, usageError("--seed deve essere un intero non negativo")
	}
	if o.Timeout <= 0 {
		return nil, usageError("--timeout deve essere una durata positiva (es. 20m)")
	}
	// --max-cost: mai NaN o infinito
	if o.MaxCost != nil && (math.IsNaN(*o.MaxCost) || math.IsInf(*o.MaxCost, 0) || *o.MaxCost < 0) {
		return nil, usageError("--max-cost deve essere un importo finito e non negativo in dollari")
	}
	req.Image, req.LastFrame, req.Refs = o.Image, o.LastFrame, slices.Clone(o.Refs)
	files := req
	if o.ImagePending {
		files.Image = ""
	}
	if err := checkVideoInputs(&files); err != nil {
		return nil, err
	}
	c := o.client
	if c == nil {
		if c, err = newProvider(o.Provider); err != nil {
			return nil, err
		}
	}
	// capacità e costo prima dei download e della generazione
	plan, err := checkVideo(c, &req, videoChecks{strict: o.Strict, maxCost: o.MaxCost, dryRun: o.DryRun})
	if err != nil {
		return nil, err
	}
	plan.Prompt = o.Prompt // prompt dell'utente; prompt_final è quello coi preset
	return &videoJob{c: c, provider: o.Provider, prompt: o.Prompt, req: req, pres: pres,
		plan: plan, out: o.Out, name: o.Name}, nil
}

// run scarica gli ingressi, genera e salva i file; restituisce il manifest da
// emettere. Un job partito e poi fallito lascia la sua riga nel registro.
func (j *videoJob) run(ctx context.Context) (manifest, error) {
	req := j.req
	req.Refs = slices.Clone(j.req.Refs) // resolveVideo li sostituisce coi file scaricati
	var tmp tempInputs
	defer tmp.cleanup()
	inputs := newManifestInputs(req) // come li ha passati l'utente, prima dei download
	if err := tmp.resolveVideo(&req); err != nil {
		return manifest{}, err
	}
	man := videoManifest(j.c.Name(), j.prompt, j.pres, j.plan, inputs, req)
	fmt.Fprintf(os.Stderr, "generazione video con %s/%s…\n", j.provider, req.Model)
	start := time.Now()
	res, err := j.c.GenerateVideo(ctx, req)
	if err != nil {
		logFailedJob(man, err, time.Since(start) >= req.Timeout)
		return manifest{}, err
	}
	slug := j.name
	if slug == "" {
		slug = output.Slug(j.prompt)
	}
	files, err := output.SaveAll(j.out, slug, res.Media)
	if err != nil {
		return manifest{}, err
	}
	man.done(files, res)
	return man, nil
}

// imageOptions sono i parametri di una generazione di immagini, nella forma dei flag di giv image.
type imageOptions struct {
	Provider, Model, Prompt, Aspect string
	N, Seed                         int
	Inputs                          []string // file o URL
	Out, Name                       string
}

// imageJob è una generazione di immagini pronta a partire.
type imageJob struct {
	c         provider.Provider
	provider  string // come indicato dall'utente
	req       provider.ImageRequest
	out, name string
}

// prepareImage controlla provider, chiave e modello, senza spendere.
func prepareImage(o imageOptions) (*imageJob, error) {
	c, err := newProvider(o.Provider)
	if err != nil {
		return nil, err
	}
	m, err := resolveModel(o.Model, o.Provider, defaultImageModels)
	if err != nil {
		return nil, err
	}
	return &imageJob{c: c, provider: o.Provider, out: o.Out, name: o.Name, req: provider.ImageRequest{
		Prompt: o.Prompt, Model: m, N: o.N, Aspect: o.Aspect, Seed: o.Seed, Inputs: slices.Clone(o.Inputs),
	}}, nil
}

// run scarica gli ingressi, genera e salva i file; restituisce il manifest da emettere.
func (j *imageJob) run(ctx context.Context) (manifest, error) {
	req := j.req
	var tmp tempInputs
	defer tmp.cleanup()
	req.Inputs = make([]string, len(j.req.Inputs))
	for i, in := range j.req.Inputs {
		var err error
		if req.Inputs[i], err = tmp.resolve(in); err != nil {
			return manifest{}, err
		}
	}
	fmt.Fprintf(os.Stderr, "generazione immagine con %s/%s…\n", j.provider, req.Model)
	res, err := j.c.GenerateImage(ctx, req)
	if err != nil {
		return manifest{}, err
	}
	slug := j.name
	if slug == "" {
		slug = output.Slug(req.Prompt)
	}
	files, err := output.SaveAll(j.out, slug, res.Media)
	if err != nil {
		return manifest{}, err
	}
	return manifest{Provider: j.c.Name(), Model: req.Model, Prompt: req.Prompt, Files: files, CostUSD: res.CostUSD}, nil
}
