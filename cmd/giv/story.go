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
	case "help", "-h", "--help":
		fmt.Fprint(os.Stderr, storyHelp)
		return nil
	case "check":
		return cmdStoryCheck(args[1:])
	case "run":
		return cmdStoryRun(args[1:])
	case "cut":
		return cmdStoryCut(args[1:])
	}
	return usageError("giv story %s: sottocomando sconosciuto (check, run o cut)", args[0])
}

// storyHelp è l'aiuto di giv story (giv story --help), su stderr come quello di giv.
const storyHelp = `giv story — video a più scene da uno storyboard JSON (formato nel README, sezione Storyboard)

Uso:
  giv story check <storyboard.json>   controlla lo storyboard e ne stima il costo: nessuna
                                      generazione, gratuito
  giv story run [--scene <id> [--new-keyframe]] <storyboard.json>
                                      genera a pagamento le scene video e avatar mancanti; stato e
                                      spesa in <nome>.stato.json accanto allo storyboard
  giv story cut <storyboard.json>     monta con ffmpeg le scene già pronte in un solo video
  giv story <sottocomando> --help     aiuto e flag del sottocomando

Flag di giv story run (anche dopo il file):
  --scene <id>       rifà solo questa scena video o avatar, anche fatta, incerta o fallita
  --new-keyframe     con --scene: rigenera anche il fotogramma chiave della scena
  --new-job          con --scene: abbandona il job già partito della scena e ne avvia uno nuovo

Uscita: su stdout solo il JSON del risultato; avanzamento, avvisi ed errori su stderr.
Uno storyboard, uno stato o un tetto di spesa non validi escono con codice 2, senza generazioni.
`

// storySubHelp è l'aiuto dei sottocomandi (giv story <sottocomando> --help).
var storySubHelp = map[string]string{
	"check": `uso: giv story check <storyboard.json>

Controlla lo storyboard senza generare niente: forma, file, riferimenti fra scene, modelli
sul catalogo letto adesso, capacità, preset e tutela. Somma stime da listino e costi
dichiarati e, con tetto_costo_usd, li confronta col tetto insieme alla spesa già registrata
nello stato (<nome>.stato.json) per scene non più nello storyboard (removed_scenes).
Ogni problema trovato esce con codice 2, uno per riga.

Flag:
  -h, --help   questo aiuto
`,
	"run": `uso: giv story run [--scene <id> [--new-keyframe]] <storyboard.json>

Genera a pagamento le scene video e avatar dello storyboard, in ordine e una richiesta alla
volta: prima l'eventuale fotogramma chiave, poi la clip, in clip/ accanto allo storyboard
(scena avatar: immagine e voce caricate su fal, Kling AI Avatar v2; serve FAL_KEY). Stato e
spesa sono in <nome>.stato.json: una nuova esecuzione recupera i job già partiti senza
rilanciarli e genera solo le scene mancanti. Con tetto_costo_usd, prima di ogni richiesta
a pagamento la spesa registrata (anche di scene non più nello storyboard) più le stime
ancora da fare non deve superare il tetto, altrimenti codice 2 senza nuovi avvii.

Flag (anche dopo il file):
  --scene <id>       rifà solo questa scena video o avatar, anche fatta, incerta o fallita: la
                     ripresa precedente resta nello stato e in clip/; il fotogramma
                     chiave già generato si riusa (se il suo file manca, codice 2:
                     serve --new-keyframe)
  --new-keyframe     solo con --scene, per una scena con fotogramma_chiave: rigenera
                     anche il fotogramma chiave, con la stessa riserva e lo stesso tetto
                     delle altre richieste; il precedente resta fra le riprese precedenti
  --new-job          solo con --scene, per una scena con un job partito che non si
                     scarica più: il job non si recupera, resta fra i precedenti con
                     l'errore e il suo addebito resta contato; poi una nuova richiesta,
                     con la stessa riserva e lo stesso tetto delle altre
  -h, --help         questo aiuto
`,
	"cut": `uso: giv story cut <storyboard.json>

Monta con ffmpeg e ffprobe le scene nell'ordine dello storyboard (per le scene video la
clip in uso nello stato di giv story run, poi clip e immagini) con transizione, voce,
musica e sottotitoli del blocco montaggio. La voce di una scena avatar è già nella sua clip
e non si aggiunge una seconda volta. Nessuna generazione e nessuna rete; il file
d'uscita (montaggio.uscita, altrimenti <nome>.mp4) si riscrive a ogni montaggio.

Flag:
  -h, --help   questo aiuto
`,
}

// storyArg legge l'unico argomento di un sottocomando: il file dello storyboard.
func storyArg(name string, args []string) (string, error) {
	fl := flag.NewFlagSet("story "+name, flag.ExitOnError)
	fl.Usage = func() { fmt.Fprint(os.Stderr, storySubHelp[name]) }
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
	Storyboard      string         `json:"storyboard"`
	Titolo          string         `json:"titolo,omitempty"`
	Scenes          []sceneCheck   `json:"scenes"`
	CostEstUSD      float64        `json:"cost_est_usd"`                // somma delle stime da listino
	CostDeclaredUSD float64        `json:"cost_declared_usd,omitempty"` // somma dei costi dichiarati dall'autore
	CostTotalUSD    float64        `json:"cost_total_usd"`              // stime più costi dichiarati
	NotEstimated    []string       `json:"not_estimated,omitempty"`     // senza stima né costo dichiarato
	RemovedSpentUSD float64        `json:"removed_spent_usd,omitempty"` // spesa nello stato di scene non più nello storyboard: conta nel tetto
	RemovedScenes   []removedScene `json:"removed_scenes,omitempty"`
	CapUSD          *float64       `json:"cap_usd,omitempty"` // tetto_costo_usd

	exactUSD     float64 // totale con le stime non arrotondate: è quello che conta per il tetto
	removedExact float64 // RemovedSpentUSD non arrotondata
}

// removedScene è la spesa registrata nello stato per una scena che non è più
// una scena video dello storyboard (tolta, rinominata o cambiata di tipo):
// conta ancora nel tetto, ma non è fra le scene.
type removedScene struct {
	ID       string  `json:"id"`
	SpentUSD float64 `json:"spent_usd"`
}

// removedSpend elenca, per id, le scene dello stato con generazioni registrate
// che non sono più scene video dello storyboard e ne somma la spesa non arrotondata.
func removedSpend(s *story.Storyboard, st *story.State) ([]removedScene, float64) {
	var out []removedScene
	var total float64
	for _, id := range sortedKeys(st.Scenes) {
		ss := st.Scenes[id]
		if sc := s.Scena(id); (sc != nil && sc.Generata()) || len(ss.Charges) == 0 {
			continue
		}
		v := ss.Spent()
		out = append(out, removedScene{ID: id, SpentUSD: round4(v)})
		total += v
	}
	return out, total
}

// removedText descrive la spesa delle scene non più nello storyboard, per avvisi e rifiuti.
func removedText(rs []removedScene, v float64) string {
	ids := make([]string, len(rs))
	for i, r := range rs {
		ids[i] = r.ID
	}
	return fmt.Sprintf("%.4f $ per scene non più nello storyboard (%s)", round4(v), strings.Join(ids, ", "))
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
	VoiceDelayS     *float64       `json:"voice_delay_s,omitempty"` // ritardo_voce
	CostEstUSD      *float64       `json:"cost_est_usd,omitempty"`
	CostSource      string         `json:"cost_source,omitempty"`
	CostDeclaredUSD *float64       `json:"cost_declared_usd,omitempty"`
	Warnings        []string       `json:"warnings,omitempty"`

	length float64 // secondi della scena nel montaggio previsti; 0 = ignota (clip senza durata)
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
// La spesa registrata nello stato per scene non più nello storyboard è a parte
// e conta nel tetto.
func checkStory(s *story.Storyboard, path string) (*storyCheck, error) {
	st, err := story.LoadState(story.StatePath(path))
	if err != nil {
		return nil, usageError("%v", err)
	}
	chk := newStoryChecker(s)
	out, err := chk.plan(path, nil)
	if err != nil {
		return nil, err
	}
	chk.voiceOverlaps(out)
	out.RemovedScenes, out.removedExact = removedSpend(s, st)
	out.RemovedSpentUSD = round4(out.removedExact)
	if len(out.RemovedScenes) > 0 {
		fmt.Fprintf(os.Stderr, "spesa già registrata nello stato: %s; conta nel tetto\n", removedText(out.RemovedScenes, out.removedExact))
	}
	if cap := s.TettoCostoUSD; cap != nil {
		if len(out.NotEstimated) > 0 {
			return nil, usageError("%s: con tetto_costo_usd ogni generazione vuole una stima o un costo_dichiarato_usd; senza: %s",
				path, strings.Join(out.NotEstimated, ", "))
		}
		if out.exactUSD+out.removedExact > *cap {
			if len(out.RemovedScenes) > 0 {
				return nil, usageError("%s: costo previsto %.4f $ più spesa già registrata di %s oltre tetto_costo_usd %.4f $",
					path, out.CostTotalUSD, removedText(out.RemovedScenes, out.removedExact), *cap)
			}
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
	if err := checkTutela("default.tutela", s.Default.Tutela); err != nil {
		return nil, storyError(path, err)
	}
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
		out.Scenes = append(out.Scenes, c)
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
	if job := sc.clips[c.ID]; job != nil && job.plan.costExact != nil {
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
	clips  map[string]*clipJob  // scene avatar
}

func newStoryChecker(s *story.Storyboard) *storyChecker {
	return &storyChecker{s: s, clients: map[string]*storyClient{}, names: map[string]string{},
		jobs: map[string]*videoJob{}, images: map[string]*imageJob{}, clips: map[string]*clipJob{}}
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
	c := sceneCheck{ID: scene.ID, Tipo: scene.Tipo, Duration: scene.Durata, Voice: scene.Voce, VoiceDelayS: scene.RitardoVoce}
	if scene.Tipo == story.Avatar {
		return sc.avatar(scene, c)
	}
	if scene.Tipo != story.Video {
		c.File, c.length = scene.File, float64(scene.Durata)
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
	c.length = float64(max(job.req.Duration, scene.Durata))
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

// avatar controlla una scena avatar come giv avatar --dry-run, senza chiave:
// modello, immagine, voce (durata letta con ffprobe) e stima; il job pronto
// resta in sc.clips. La scena dura quanto la voce.
func (sc *storyChecker) avatar(scene *story.Scene, c sceneCheck) (sceneCheck, error) {
	s := sc.s
	o := clipOptions{Provider: "fal", Model: avatarModel(s.AvatarModello(scene)), Prompt: scene.Prompt,
		Image: s.Path(scene.Immagine), Audio: s.Path(scene.Voce), Timeout: storyTimeout, Strict: s.Strict,
		Out: sc.out, Name: sc.names[scene.ID], Set: []string{"image", "audio"}}
	if strings.TrimSpace(scene.Prompt) != "" {
		o.Set = append(o.Set, "prompt")
	}
	fmt.Fprintf(os.Stderr, "scena %s: %s/%s\n", scene.ID, o.Provider, o.Model)
	job, err := prepareAvatar(o)
	if err != nil {
		return c, err
	}
	// con un tetto la stima dell'avatar vuole la durata misurata della voce:
	// un costo dichiarato non basta, la spesa dipende dalla misura (decisione 15)
	if job.plan.CostEstUSD == nil && s.TettoCostoUSD != nil {
		return c, usageError("con tetto_costo_usd la scena avatar vuole la durata della voce misurata con ffprobe: costo non stimabile, costo_dichiarato_usd non basta")
	}
	sc.clips[scene.ID] = job
	plan := job.plan
	c.Provider, c.Model, c.PromptFinal, c.Warnings = plan.Provider, plan.Model, plan.Prompt, plan.Warnings
	c.Inputs = map[string]any{"image": plan.Inputs.Image.Path, "audio": plan.Inputs.Audio.Path}
	if d := job.req.Source.AudioDuration; d > 0 {
		c.Params, c.length = map[string]any{"audio_duration_s": d}, d
	}
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
	return c, nil
}

// videoOptions traduce una scena video nei parametri di giv video: la scena
// prevale sul blocco default, che prevale sul formato; il modello deve essere
// nel catalogo letto adesso.
func (sc *storyChecker) videoOptions(scene *story.Scene) (videoOptions, error) {
	s, d, g := sc.s, sc.s.Default, scene.Generazione
	if err := checkTutela("tutela", g.Tutela); err != nil {
		return videoOptions{}, err
	}
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

// checkTutela controlla la tutela di uno storyboard (campo name): una clausola
// della libreria dei preset (prodotto, illustrazione e quelle dei file
// dell'utente) oppure no; vuota = come giv video (prodotto coi preset e un
// primo fotogramma).
func checkTutela(name, t string) error {
	if t == "" || t == "no" {
		return nil
	}
	lib, err := loadPresets()
	if err != nil {
		return err
	}
	valid := lib.Protections()
	if !slices.Contains(valid, t) {
		return usageError("%s %q sconosciuta (valide: %s, no)", name, t, strings.Join(valid, ", "))
	}
	return nil
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
	return runStoryCut(args)
}
