package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"gen-image-video-cli/internal/caps"
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
	Keyframes                    []string        // fotogrammi intermedi, in ordine
	Shots                        []provider.Shot // inquadrature: niente prompt, durata = somma
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

	// giv story run: ingressi come compaiono nel manifest (req ha le copie
	// private) e salvataggio dei file al posto di output.SaveAll
	inputs *manifestInputs
	save   saveFunc
}

// saveFunc salva i media del risultato col nome slug (vede anche l'addebito,
// prima di scrivere); nil = output.SaveAll nella cartella out.
type saveFunc func(slug string, res *provider.Result) ([]output.FileInfo, error)

func saveMedia(save saveFunc, dir, slug string, res *provider.Result) ([]output.FileInfo, error) {
	if save != nil {
		return save(slug, res)
	}
	return output.SaveAll(dir, slug, res.Media)
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
	if err := checkFramesShots(o.Provider, m, len(o.Keyframes) > 0, len(o.Shots) > 0); err != nil {
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
	if len(o.Shots) > 0 {
		if err := shotsRequest(o, pres, &req); err != nil {
			return nil, err
		}
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
	req.Keyframes = slices.Clone(o.Keyframes)
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

// checkFramesShots rifiuta --keyframe e --shot su provider e schede che non li
// hanno, prima di chiave e catalogo (codice 2): li dichiarano solo schede di
// fal, nella tabella fissa.
func checkFramesShots(prov, model string, keyframes, shots bool) error {
	cards := caps.Fal()
	for _, f := range []struct {
		flag string
		used bool
		ok   func(*caps.Model) bool
	}{
		{"--keyframe", keyframes, func(m *caps.Model) bool { return m.MaxKeyframes != nil && *m.MaxKeyframes > 0 }},
		{"--shot", shots, func(m *caps.Model) bool { return m.Shots != nil && *m.Shots }},
	} {
		if !f.used {
			continue
		}
		if m := caps.Find(cards, model); prov == "fal" && m != nil && f.ok(m) {
			continue
		}
		var with []string
		for i := range cards {
			if f.ok(&cards[i]) {
				with = append(with, cards[i].ID)
			}
		}
		return usageError("%s non supportato da %s/%s: vale solo con --provider fal e --model %s", f.flag, prov, model, strings.Join(with, " o "))
	}
	return nil
}

// parseShots legge i --shot "<secondi>:<testo>": secondi interi, testo non
// vuoto; i limiti del modello li controlla la scheda.
func parseShots(list []string) ([]provider.Shot, error) {
	var out []provider.Shot
	for i, v := range list {
		s, text, ok := strings.Cut(v, ":")
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if !ok || err != nil {
			return nil, usageError("--shot n. %d: serve la forma \"<secondi>:<testo>\" (secondi interi)", i+1)
		}
		out = append(out, provider.Shot{Prompt: strings.TrimSpace(text), Duration: n})
	}
	return out, nil
}

// maxShotSeconds è il tetto di una inquadratura prima dei limiti del modello
// (tiene la somma lontana dall'overflow).
const maxShotSeconds = 1000

// shotsRequest mette in req le inquadrature di o: il testo è tutto lì (niente
// prompt libero né frasi dei preset) e il video dura la loro somma.
func shotsRequest(o videoOptions, pres presets.Result, req *provider.VideoRequest) error {
	if o.Prompt != "" || len(o.Presets) > 0 || pres.Prompt != "" {
		return usageError("--shot: il testo va nelle inquadrature, niente prompt né --preset")
	}
	sum := 0
	for i, s := range o.Shots {
		if strings.TrimSpace(s.Prompt) == "" {
			return usageError("--shot n. %d: testo mancante", i+1)
		}
		if s.Duration < 1 || s.Duration > maxShotSeconds {
			return usageError("--shot n. %d: %d s non validi (secondi interi positivi)", i+1, s.Duration)
		}
		sum += s.Duration
	}
	if o.Duration != 0 && o.Duration != sum {
		return usageError("--duration %d diversa dalla somma delle inquadrature (%d s): toglila o correggila", o.Duration, sum)
	}
	req.Duration, req.Shots = sum, slices.Clone(o.Shots)
	return nil
}

// run scarica gli ingressi, genera e salva i file; restituisce il manifest da
// emettere. Un job partito e poi fallito lascia la sua riga nel registro.
func (j *videoJob) run(ctx context.Context) (manifest, error) {
	req := j.req
	req.Refs = slices.Clone(j.req.Refs) // resolveVideo li sostituisce coi file scaricati
	req.Keyframes = slices.Clone(j.req.Keyframes)
	var tmp tempInputs
	defer tmp.cleanup()
	inputs := newManifestInputs(req) // come li ha passati l'utente, prima dei download
	if j.inputs != nil {
		inputs = j.inputs
	}
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
		text := j.prompt
		if text == "" && len(req.Shots) > 0 {
			text = req.Shots[0].Prompt // --shot: niente prompt, il nome dalla prima inquadratura
		}
		slug = output.Slug(text)
	}
	files, err := saveMedia(j.save, j.out, slug, res)
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
	save      saveFunc // giv story run; nil = output.SaveAll
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
	files, err := saveMedia(j.save, j.out, slug, res)
	if err != nil {
		return manifest{}, err
	}
	return manifest{Provider: j.c.Name(), Model: req.Model, Prompt: req.Prompt, Files: files, CostUSD: res.CostUSD}, nil
}
