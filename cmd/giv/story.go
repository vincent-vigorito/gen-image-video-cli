package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"slices"
	"strings"
	"time"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/provider"
	"gen-image-video-cli/internal/story"
)

// storyTimeout è il tetto di attesa di ogni job, come il default di giv video.
const storyTimeout = 15 * time.Minute

// cmdStory smista i sottocomandi di giv story.
func cmdStory(args []string) error {
	if len(args) == 0 {
		return usageError("uso: giv story check|run|cut <storyboard.json>")
	}
	switch args[0] {
	case "check":
		return cmdStoryCheck(args[1:])
	case "run":
		return cmdStoryRun(args[1:])
	case "cut":
		return cmdStoryCut(args[1:])
	}
	return usageError("giv story %s: sottocomando sconosciuto (check, run o cut)", args[0])
}

// storyArg legge l'unico argomento di un sottocomando: il file dello storyboard.
func storyArg(name string, args []string) (string, error) {
	fl := flag.NewFlagSet("story "+name, flag.ExitOnError)
	if err := fl.Parse(args); err != nil {
		return "", err
	}
	if fl.NArg() != 1 {
		return "", usageError("uso: giv story %s <storyboard.json>", name)
	}
	return fl.Arg(0), nil
}

// loadStory legge e controlla lo storyboard (codice 2 con l'elenco dei problemi).
func loadStory(path string) (*story.Storyboard, error) {
	s, err := story.Load(path)
	if err == nil {
		return s, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, usageError("%s: storyboard non trovato", path)
	}
	return nil, storyError(path, err)
}

// storyError è un rifiuto di codice 2 con i problemi dello storyboard, uno per riga.
func storyError(path string, err error) error {
	lines := strings.Split(err.Error(), "\n")
	if len(lines) == 1 {
		return usageError("%s: %s", path, lines[0])
	}
	return usageError("%s: %d problemi:\n  %s", path, len(lines), strings.Join(lines, "\n  "))
}

// storyCheck è l'uscita di giv story check.
type storyCheck struct {
	Storyboard      string       `json:"storyboard"`
	Titolo          string       `json:"titolo,omitempty"`
	Scene           []sceneCheck `json:"scene"`
	CostEstUSD      float64      `json:"cost_est_usd"`                // somma delle stime da listino
	CostDeclaredUSD float64      `json:"cost_declared_usd,omitempty"` // somma dei costi dichiarati dall'autore
	CostTotalUSD    float64      `json:"cost_total_usd"`              // stime più costi dichiarati
	NotEstimated    []string     `json:"not_estimated,omitempty"`     // senza stima né costo dichiarato
	CapUSD          *float64     `json:"cap_usd,omitempty"`           // tetto_costo_usd

	exactUSD float64 // totale con le stime non arrotondate: è quello che conta per il tetto
}

// sceneCheck è una scena controllata: per le scene video il piano di giv video --dry-run.
type sceneCheck struct {
	ID              string         `json:"id"`
	Tipo            string         `json:"tipo"`
	Provider        string         `json:"provider,omitempty"`
	Model           string         `json:"model,omitempty"`
	Duration        int            `json:"duration,omitempty"`
	PromptFinal     string         `json:"prompt_final,omitempty"`
	Params          map[string]any `json:"params,omitempty"`
	Inputs          map[string]any `json:"inputs,omitempty"`
	Keyframe        *keyframeCheck `json:"keyframe,omitempty"`
	File            string         `json:"file,omitempty"`
	Voice           string         `json:"voice,omitempty"`
	CostEstUSD      *float64       `json:"cost_est_usd,omitempty"`
	CostSource      string         `json:"cost_source,omitempty"`
	CostDeclaredUSD *float64       `json:"cost_declared_usd,omitempty"`
	Warnings        []string       `json:"warnings,omitempty"`
}

// keyframeCheck è il fotogramma chiave di una scena: un'immagine generata prima
// della clip, senza stima da listino.
type keyframeCheck struct {
	Provider        string   `json:"provider"`
	Model           string   `json:"model"`
	Prompt          string   `json:"prompt"`
	Inputs          []string `json:"inputs,omitempty"`
	CostDeclaredUSD *float64 `json:"cost_declared_usd,omitempty"`
}

func cmdStoryCheck(args []string) error {
	path, err := storyArg("check", args)
	if err != nil {
		return err
	}
	s, err := loadStory(path)
	if err != nil {
		return err
	}
	out, err := checkStory(s, path)
	if err != nil {
		return err
	}
	return output.PrintJSON(out)
}

// checkStory controlla ogni scena sul modello e sul catalogo letto adesso, somma
// i costi e applica il tetto. Nessuna generazione: solo file locali e cataloghi.
func checkStory(s *story.Storyboard, path string) (*storyCheck, error) {
	out, err := newStoryChecker(s).plan(path, nil)
	if err != nil {
		return nil, err
	}
	if cap := s.TettoCostoUSD; cap != nil {
		if len(out.NotEstimated) > 0 {
			return nil, usageError("%s: con tetto_costo_usd ogni generazione vuole una stima o un costo_dichiarato_usd; senza: %s",
				path, strings.Join(out.NotEstimated, ", "))
		}
		if out.exactUSD > *cap {
			return nil, usageError("%s: costo previsto %.4f $ oltre tetto_costo_usd %.4f $", path, out.CostTotalUSD, *cap)
		}
	} else if len(out.NotEstimated) > 0 {
		fmt.Fprintf(os.Stderr, "avviso: senza stima del costo (non contate nel totale): %s\n", strings.Join(out.NotEstimated, ", "))
	}
	return out, nil
}

// plan controlla le scene indicate (nil = tutte), nell'ordine dello storyboard,
// e ne somma i costi, senza tetto; i job video preparati restano in sc.jobs.
func (sc *storyChecker) plan(path string, ids map[string]bool) (*storyCheck, error) {
	s := sc.s
	out := &storyCheck{Storyboard: path, Titolo: s.Titolo, CapUSD: s.TettoCostoUSD}
	var rejects []error
	for i := range s.Scene {
		scene := &s.Scene[i]
		if ids != nil && !ids[scene.ID] {
			continue
		}
		c, err := sc.scene(scene)
		if err != nil {
			if code, _ := exitCode(err); code != exitUsage {
				return nil, fmt.Errorf("scena %s: %w", scene.ID, err)
			}
			rejects = append(rejects, fmt.Errorf("scena %s: %w", scene.ID, err))
			continue
		}
		out.Scene = append(out.Scene, c)
		if c.CostEstUSD != nil {
			out.CostEstUSD += *c.CostEstUSD
			out.exactUSD += sc.exactEstimate(c)
		}
		if c.CostDeclaredUSD != nil {
			out.CostDeclaredUSD += *c.CostDeclaredUSD
			out.exactUSD += *c.CostDeclaredUSD
		}
		if k := c.Keyframe; k != nil && k.CostDeclaredUSD != nil {
			out.CostDeclaredUSD += *k.CostDeclaredUSD
			out.exactUSD += *k.CostDeclaredUSD
		}
	}
	if len(rejects) > 0 {
		return nil, storyError(path, errors.Join(rejects...))
	}
	out.NotEstimated = sc.notEstimated
	out.CostEstUSD, out.CostDeclaredUSD = round4(out.CostEstUSD), round4(out.CostDeclaredUSD)
	out.CostTotalUSD = round4(out.CostEstUSD + out.CostDeclaredUSD)
	return out, nil
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// exactEstimate è la stima da listino della scena c non arrotondata (quella
// del manifest ha quattro decimali).
func (sc *storyChecker) exactEstimate(c sceneCheck) float64 {
	if job := sc.jobs[c.ID]; job != nil && job.plan.costExact != nil {
		return *job.plan.costExact
	}
	return *c.CostEstUSD
}

// storyChecker controlla le scene di uno storyboard: un client e una lettura del
// catalogo per provider.
type storyChecker struct {
	s            *story.Storyboard
	clients      map[string]*storyClient
	notEstimated []string

	// giv story run: cartella e nome dei file per scena, job pronti a partire
	out    string
	names  map[string]string
	jobs   map[string]*videoJob
	images map[string]*imageJob // fotogrammi chiave
}

func newStoryChecker(s *story.Storyboard) *storyChecker {
	return &storyChecker{s: s, clients: map[string]*storyClient{}, names: map[string]string{},
		jobs: map[string]*videoJob{}, images: map[string]*imageJob{}}
}

// storyClient è un provider col catalogo video letto una volta sola.
type storyClient struct {
	provider.Provider
	read    bool
	catalog []caps.Model
	err     error
}

func (c *storyClient) VideoCaps(ctx context.Context) ([]caps.Model, error) {
	if !c.read {
		c.read = true
		if vc, ok := c.Provider.(videoCapser); ok {
			c.catalog, c.err = vc.VideoCaps(ctx)
		} else {
			c.err = fmt.Errorf("capacità video non disponibili per %s", c.Name())
		}
	}
	return c.catalog, c.err
}

func (sc *storyChecker) client(name string) (*storyClient, error) {
	if c, ok := sc.clients[name]; ok {
		return c, nil
	}
	p, err := newProvider(name)
	if err != nil {
		return nil, err
	}
	c := &storyClient{Provider: p}
	sc.clients[name] = c
	return c, nil
}

func (sc *storyChecker) scene(scene *story.Scene) (sceneCheck, error) {
	c := sceneCheck{ID: scene.ID, Tipo: scene.Tipo, Duration: scene.Durata, Voice: scene.Voce}
	if scene.Tipo != story.Video {
		c.File = scene.File
		return c, nil
	}
	o, err := sc.videoOptions(scene)
	if err != nil {
		return c, err
	}
	o.Out, o.Name = sc.out, sc.names[scene.ID]
	fmt.Fprintf(os.Stderr, "scena %s: %s/%s\n", scene.ID, o.Provider, o.Model)
	job, err := prepareVideo(o)
	if err != nil {
		return c, err
	}
	sc.jobs[scene.ID] = job
	plan := job.plan
	c.Provider, c.Model = o.Provider, job.req.Model
	c.PromptFinal, c.Params, c.Inputs, c.Warnings = plan.PromptFinal, plan.Params, plan.Inputs, plan.Warnings
	switch {
	case plan.CostEstUSD != nil:
		c.CostEstUSD, c.CostSource = plan.CostEstUSD, plan.CostSource
		if scene.CostoDichiaratoUSD != nil {
			c.Warnings = append(c.Warnings, "costo_dichiarato_usd ignorato: c'è la stima da listino")
		}
	case scene.CostoDichiaratoUSD != nil:
		c.CostDeclaredUSD = scene.CostoDichiaratoUSD
	default:
		sc.notEstimated = append(sc.notEstimated, scene.ID)
	}
	if fk := scene.FotogrammaChiave; fk != nil {
		if c.Keyframe, err = sc.keyframe(scene, o); err != nil {
			return c, fmt.Errorf("fotogramma_chiave: %w", err)
		}
		if fk.CostoDichiaratoUSD == nil {
			sc.notEstimated = append(sc.notEstimated, scene.ID+" (fotogramma_chiave)")
		}
	}
	return c, nil
}

// videoOptions traduce una scena video nei parametri di giv video: la scena
// prevale sul blocco default, che prevale sul formato; il modello deve essere
// nel catalogo letto adesso.
func (sc *storyChecker) videoOptions(scene *story.Scene) (videoOptions, error) {
	s, d, g := sc.s, sc.s.Default, scene.Generazione
	pick := func(v ...string) string {
		for _, x := range v {
			if x != "" {
				return x
			}
		}
		return ""
	}
	var f story.Formato
	if s.Formato != nil {
		f = *s.Formato
	}
	o := videoOptions{
		Provider: pick(g.Provider, d.Provider, defaultProvider()),
		Model:    pick(g.Modello, d.Modello),
		Prompt:   scene.Prompt, Negative: scene.Negative, Presets: scene.Preset,
		Aspect:     pick(g.Aspect, d.Aspect, f.Aspect),
		Resolution: pick(g.Risoluzione, d.Risoluzione, f.Risoluzione),
		Audio:      pick(g.Audio, d.Audio),
		Protect:    pick(g.Tutela, d.Tutela),
		Duration:   scene.Durata, Seed: g.Seed, Timeout: storyTimeout,
		Strict:    s.Strict,
		Image:     s.Path(scene.Immagine),
		LastFrame: s.Path(scene.UltimoFotogramma),
	}
	if o.Seed == 0 {
		o.Seed = d.Seed
	}
	// parametri passanti nella forma di --param: il valore è il JSON dello
	// storyboard, che parseParams rilegge uguale; la scena prevale per chiave
	params := map[string]string{}
	for _, m := range []map[string]json.RawMessage{d.Param, g.Param} {
		for k, v := range m {
			params[k] = string(v)
		}
	}
	for _, k := range sortedKeys(params) {
		if !validParamKey(k) {
			return o, usageError("param %q: chiave non valida (lettere, cifre e _, inizia con una lettera)", k)
		}
		o.Params = append(o.Params, k+"="+params[k])
	}
	var refs []string
	for _, name := range scene.Usa {
		for _, p := range s.Riferimenti[name] {
			refs = append(refs, s.Path(p))
		}
	}
	switch {
	case scene.FotogrammaChiave != nil:
		// i riferimenti vanno all'immagine, che porta la coerenza nella clip
		o.Image, o.ImagePending = "(fotogramma chiave di "+scene.ID+")", true
	case scene.ContinuaDa != "":
		o.Image, o.ImagePending = "(ultimo fotogramma di "+scene.ContinuaDa+")", true
		o.Refs = refs
	default:
		o.Refs = refs
	}

	if err := checkProvider(o.Provider); err != nil {
		return o, err
	}
	m, err := resolveVideoModel(o.Model, o.Provider)
	if err != nil {
		return o, err
	}
	o.Model = m
	cl, err := sc.client(o.Provider)
	if err != nil {
		return o, err
	}
	catalog, err := cl.VideoCaps(context.Background())
	if err != nil {
		return o, err
	}
	if caps.Find(catalog, m) == nil {
		return o, usageError("modello %s non è nel catalogo video di %s letto adesso: scegline un altro (giv models --provider %s --kind video --caps)",
			m, o.Provider, o.Provider)
	}
	o.client = cl
	return o, nil
}

// keyframe controlla il fotogramma chiave della scena: provider, chiave e modello
// di giv image; i riferimenti di usa sono le immagini di ingresso.
func (sc *storyChecker) keyframe(scene *story.Scene, o videoOptions) (*keyframeCheck, error) {
	fk := scene.FotogrammaChiave
	var inputs []string
	for _, name := range scene.Usa {
		for _, p := range sc.s.Riferimenti[name] {
			inputs = append(inputs, sc.s.Path(p))
		}
	}
	prov := fk.Provider
	if prov == "" {
		prov = o.Provider
	}
	job, err := prepareImage(imageOptions{Provider: prov, Model: fk.Modello, Prompt: fk.Prompt, Aspect: o.Aspect, N: 1, Inputs: inputs,
		Out: sc.out, Name: sc.names[scene.ID] + "-fotogramma"})
	if err != nil {
		return nil, err
	}
	sc.images[scene.ID] = job
	k := &keyframeCheck{Provider: prov, Model: job.req.Model, Prompt: fk.Prompt, CostDeclaredUSD: fk.CostoDichiaratoUSD}
	for _, in := range inputs {
		k.Inputs = append(k.Inputs, shownInput(in))
	}
	return k, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func cmdStoryCut(args []string) error {
	return &cliError{code: exitError, msg: "giv story cut non ancora disponibile"}
}
