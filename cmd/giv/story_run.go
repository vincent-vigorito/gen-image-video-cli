package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/provider"
	"gen-image-video-cli/internal/story"
)

// storyClipDir è la cartella di clip e fotogrammi, dentro quella dello storyboard.
const storyClipDir = "clip"

// frameExtractor estrae l'ultimo fotogramma di una clip (continua_da).
type frameExtractor interface {
	Check() error
	LastFrame(ctx context.Context, clip, out string, overwrite bool) error
}

// storyFrames estrae i fotogrammi di continua_da con ffmpeg.
var storyFrames frameExtractor = montage.Tool{}

// cmdStoryRun genera le scene dello storyboard in ordine, una richiesta alla
// volta, con lo stato per scena accanto allo storyboard: una seconda esecuzione
// riprende dalle scene mancanti e recupera i job già partiti senza rilanciarli.
func cmdStoryRun(args []string) error {
	fl := flag.NewFlagSet("story run", flag.ExitOnError)
	only := fl.String("scene", "", "rifà solo questa scena (id); la ripresa precedente resta")
	newKeyframe := fl.Bool("new-keyframe", false, "con --scene: rigenera anche il fotogramma chiave")
	fl.Usage = func() { fmt.Fprint(os.Stderr, storySubHelp["run"]) }
	if err := fl.Parse(args); err != nil {
		return usageError("%v", err)
	}
	// flag anche dopo il file
	rest := fl.Args()
	if len(rest) == 0 {
		return usageError("uso: giv story run [--scene <id> [--new-keyframe]] <storyboard.json>")
	}
	path := rest[0]
	if err := fl.Parse(rest[1:]); err != nil {
		return usageError("%v", err)
	}
	if fl.NArg() > 0 {
		return usageError("uso: giv story run [--scene <id> [--new-keyframe]] <storyboard.json>")
	}
	if *newKeyframe && *only == "" {
		return usageError("--new-keyframe vale solo insieme a --scene <id>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	man, err := runStory(ctx, path, *only, *newKeyframe)
	if man != nil {
		// scene fallite sul provider: manifest delle altre, poi l'errore con l'elenco
		if perr := output.PrintJSON(man); err == nil {
			err = perr
		}
	}
	return err
}

// storyRunManifest è l'uscita di giv story run: per scena stima e addebito affiancati.
type storyRunManifest struct {
	Command    string     `json:"command"`
	Storyboard string     `json:"storyboard"`
	State      string     `json:"state"`
	Titolo     string     `json:"titolo,omitempty"`
	Scenes     []sceneRun `json:"scenes"`
	Started    int        `json:"jobs_started"`   // job video avviati in questa esecuzione
	Recovered  int        `json:"jobs_recovered"` // job già partiti, scaricati senza nuovo avvio
	Keyframes  int        `json:"keyframes"`      // fotogrammi chiave generati in questa esecuzione
	CostEstUSD float64    `json:"cost_est_usd"`   // stime (o costi dichiarati) delle generazioni delle scene
	CostUSD    float64    `json:"cost_usd"`       // addebito riportato dai provider
	SpentUSD   float64    `json:"spent_usd"`      // per il tetto: addebito dove c'è, altrimenti stima (anche delle scene tolte)
	// spesa nello stato di scene non più nello storyboard: in spent_usd, non fra le scene
	RemovedSpentUSD float64        `json:"removed_spent_usd,omitempty"`
	RemovedScenes   []removedScene `json:"removed_scenes,omitempty"`
	CapUSD          *float64       `json:"cap_usd,omitempty"`
}

type sceneRun struct {
	ID         string  `json:"id"`
	Tipo       string  `json:"tipo"`
	Status     string  `json:"status,omitempty"`
	File       string  `json:"file,omitempty"`     // clip in uso, o file della scena clip/immagine
	Keyframe   string  `json:"keyframe,omitempty"` // fotogramma chiave in uso
	Takes      int     `json:"takes,omitempty"`    // riprese generate
	ThisRun    string  `json:"this_run,omitempty"` // generata, recuperata, già fatta
	CostEstUSD float64 `json:"cost_est_usd,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
}

// storyRunner esegue giv story run su uno storyboard già controllato.
type storyRunner struct {
	s       *story.Storyboard
	st      *story.State
	root    *os.Root // cartella dello storyboard: file letti e scritti solo qui dentro
	path    string
	sc      *storyChecker
	order   []*story.Scene     // scene video da trattare, in ordine
	forced  string             // --scene: rifatta anche se fatta
	renew   string             // --new-keyframe: scena di cui rigenerare il fotogramma chiave (vuoto dopo averlo generato)
	est     map[string]float64 // stima non arrotondata (o costo dichiarato) della clip, per scena pianificata
	planned map[string]bool
	thisRun map[string]string
	failed  []string // scene fallite sul provider (in questa esecuzione o prima): non si rigenerano da sole
	man     storyRunManifest
}

func runStory(ctx context.Context, path, only string, newKeyframe bool) (*storyRunManifest, error) {
	s, err := loadStory(path)
	if err != nil {
		return nil, err
	}
	if only != "" {
		target := s.Scena(only)
		if target == nil {
			return nil, usageError("--scene %s: scena inesistente in %s", only, path)
		}
		if target.Tipo != story.Video {
			return nil, usageError("--scene %s: scena %s, niente da generare", only, target.Tipo)
		}
		if newKeyframe && target.FotogrammaChiave == nil {
			return nil, usageError("--new-keyframe: la scena %s non ha fotogramma_chiave", only)
		}
	}
	st, unlock, err := story.OpenRunState(path)
	if err != nil {
		return nil, usageError("%v", err)
	}
	defer unlock()
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	r := &storyRunner{s: s, st: st, root: root, path: path, sc: newStoryChecker(s), forced: only,
		est: map[string]float64{}, planned: map[string]bool{}, thisRun: map[string]string{}}
	if newKeyframe {
		r.renew = only
	}
	if rs, v := removedSpend(s, st); len(rs) > 0 {
		fmt.Fprintf(os.Stderr, "spesa già registrata nello stato: %s; conta nel tetto\n", removedText(rs, v))
	}
	r.sc.out = filepath.Join(s.Dir, storyClipDir)
	r.man = storyRunManifest{Command: "story run", Storyboard: path, State: st.Path(), Titolo: s.Titolo, CapUSD: s.TettoCostoUSD}
	for i := range s.Scene {
		if sc := &s.Scene[i]; sc.Tipo == story.Video && (only == "" || sc.ID == only) {
			r.order = append(r.order, sc)
		}
	}
	if err := r.preflight(); err != nil {
		return nil, err
	}
	// i job già partiti, di tutte le scene, si recuperano prima di ogni nuovo
	// avvio: il tetto conta così il loro addebito reale. Una scena il cui job
	// finisce male sul provider è fallita: non si rigenera da sola (nemmeno con
	// --scene in questa esecuzione) e le altre proseguono.
	for _, scene := range r.pending() {
		if err := r.recover(ctx, scene); err != nil && !r.failedNow(scene.ID) {
			return nil, r.stopped(fmt.Errorf("scena %s: %w", scene.ID, err))
		}
	}
	for i, scene := range r.order {
		if err := r.scene(ctx, i, scene); err != nil && !r.failedNow(scene.ID) {
			return nil, r.stopped(fmt.Errorf("scena %s: %w", scene.ID, err))
		}
	}
	r.summary()
	return &r.man, r.stopped(nil)
}

// failedNow: la scena è fallita sul provider (job finito male o senza video):
// entra nell'elenco di fine esecuzione invece di fermarla.
func (r *storyRunner) failedNow(id string) bool {
	ss := r.st.Scenes[id]
	if ss == nil || ss.Status != story.Fallito {
		return false
	}
	if r.thisRun[id] != "fallita" {
		r.thisRun[id] = "fallita"
		r.failed = append(r.failed, id)
	}
	return true
}

// stopped è l'errore di fine esecuzione: le scene fallite, con il comando per
// rifarle, e l'eventuale errore che ha fermato l'esecuzione.
func (r *storyRunner) stopped(err error) error {
	var errs []error
	for _, id := range r.failed {
		redo := fmt.Sprintf("per rifarla: giv story run --scene %s %s", id, r.path)
		if sc := r.s.Scena(id); sc == nil || sc.Tipo != story.Video {
			redo = "non è più una scena video dello storyboard"
		}
		errs = append(errs, fmt.Errorf("scena %s fallita sul provider (spesa contata): %s; %s", id, r.st.Scenes[id].Error, redo))
	}
	return errors.Join(append(errs, err)...)
}

// preflight controlla tutto prima del primo avvio: job da recuperare, richieste
// rimaste senza esito, scene da generare (capacità, preset, stime sul catalogo
// letto adesso), fotogrammi di continua_da e tetto di spesa. Ogni rifiuto è
// codice 2 e nessun avvio.
func (r *storyRunner) preflight() error {
	var rejects []error
	var launch []*story.Scene
	for _, scene := range r.pending() {
		if err := r.checkJob(r.st.Scenes[scene.ID].Job); err != nil {
			rejects = append(rejects, fmt.Errorf("scena %s: %w", scene.ID, err))
		}
	}
	for _, scene := range r.order {
		ss := r.st.Scenes[scene.ID]
		if ss != nil && ss.Job != nil {
			continue
		}
		if ss != nil && ss.Status == story.Incerto && scene.ID != r.forced {
			rejects = append(rejects, fmt.Errorf("scena %s: l'ultima richiesta di generazione è rimasta senza esito o senza file salvato e può essere stata addebitata (contata nella spesa): controlla sul provider, poi rifalla con --scene %s", scene.ID, scene.ID))
			continue
		}
		if r.toLaunch(scene) {
			if err := r.staleKeyframe(scene); err != nil {
				rejects = append(rejects, err)
				continue
			}
			launch = append(launch, scene)
		}
	}
	if len(rejects) > 0 {
		return storyError(r.st.Path(), errors.Join(rejects...))
	}
	return r.plan(launch, true)
}

// pending sono le scene con un job già partito, di tutto lo stato (anche fuori
// da --scene o non più nello storyboard): prima quelle dello storyboard, in
// ordine, poi le altre per id.
func (r *storyRunner) pending() []*story.Scene {
	var out []*story.Scene
	seen := map[string]bool{}
	for i := range r.s.Scene {
		scene := &r.s.Scene[i]
		seen[scene.ID] = true
		if ss := r.st.Scenes[scene.ID]; ss != nil && ss.Job != nil {
			if scene.Tipo != story.Video {
				scene = &story.Scene{ID: scene.ID, Tipo: story.Video}
			}
			out = append(out, scene)
		}
	}
	var rest []string
	for id, ss := range r.st.Scenes {
		if !seen[id] && ss.Job != nil {
			rest = append(rest, id)
		}
	}
	slices.Sort(rest)
	for _, id := range rest {
		out = append(out, &story.Scene{ID: id, Tipo: story.Video})
	}
	return out
}

// checkJob controlla, senza richieste, un job del file di stato: provider con
// job video e indirizzo di attesa sull'host del provider.
func (r *storyRunner) checkJob(j *story.Job) error {
	if err := checkProvider(j.Provider); err != nil {
		return err
	}
	if _, ok := defaultVideoModels[j.Provider]; !ok {
		return usageError("job %s: %s non ha job video", j.ID, j.Provider)
	}
	c, err := r.sc.client(j.Provider)
	if err != nil {
		return err
	}
	f, ok := c.Provider.(provider.JobFetcher)
	if !ok {
		return usageError("job %s: %s non sa recuperare i job video", j.ID, j.Provider)
	}
	if err := f.CheckJob(provider.Job{ID: j.ID, PollingURL: j.PollingURL}); err != nil {
		return usageError("job %s: %v", j.ID, err)
	}
	return nil
}

// plan prepara le scene da generare e controlla il tetto: spesa già fatta più
// stime delle scene da generare (all) o della sola scena (dopo un job fallito).
func (r *storyRunner) plan(scenes []*story.Scene, all bool) error {
	if len(scenes) == 0 {
		return nil
	}
	ids := map[string]bool{}
	for _, scene := range scenes {
		ids[scene.ID] = true
		r.sc.names[scene.ID] = fmt.Sprintf("%s-r%d", scene.ID, takeNumber(r.st.Scenes[scene.ID]))
	}
	out, err := r.sc.plan(r.path, ids)
	if err != nil {
		return err
	}
	cap := r.s.TettoCostoUSD
	var missing []string
	for _, c := range out.Scenes {
		r.planned[c.ID] = true
		switch {
		case c.CostEstUSD != nil:
			r.est[c.ID] = r.sc.exactEstimate(c)
		case c.CostDeclaredUSD != nil:
			r.est[c.ID] = *c.CostDeclaredUSD
		default:
			missing = append(missing, c.ID)
		}
		scene := r.s.Scena(c.ID)
		if r.needKeyframe(scene) && scene.FotogrammaChiave.CostoDichiaratoUSD == nil {
			missing = append(missing, c.ID+" (fotogramma_chiave)")
		}
		if scene.ContinuaDa != "" {
			if err := storyFrames.Check(); err != nil {
				return &cliError{code: exitError, msg: fmt.Sprintf("scena %s: continua_da: %v", c.ID, err)}
			}
			if err := r.continuaReady(scene, ids); err != nil {
				return usageError("scena %s: %v", c.ID, err)
			}
		}
	}
	if cap == nil {
		if len(missing) > 0 {
			fmt.Fprintf(os.Stderr, "avviso: senza stima del costo (non contate nel totale): %s\n", strings.Join(missing, ", "))
		}
		return nil
	}
	if len(missing) > 0 {
		return usageError("%s: con tetto_costo_usd ogni generazione vuole una stima o un costo_dichiarato_usd; senza: %s",
			r.path, strings.Join(missing, ", "))
	}
	if all {
		return r.checkCap(0)
	}
	return nil
}

// continuaReady: la scena di continua_da ha già una clip, o la genera questa
// esecuzione prima della scena.
func (r *storyRunner) continuaReady(scene *story.Scene, launching map[string]bool) error {
	from := r.s.Scena(scene.ContinuaDa)
	if from.Tipo == story.Clip {
		return nil
	}
	if launching[from.ID] {
		return nil
	}
	ss := r.st.Scenes[from.ID]
	if ss != nil && (ss.Clip != nil || ss.Job != nil) {
		return nil
	}
	if ss != nil && ss.Status == story.Fallito {
		return fmt.Errorf("continua_da %s: la scena %s è fallita sul provider: rifalla con --scene %s", from.ID, from.ID, from.ID)
	}
	return fmt.Errorf("continua_da %s: la scena %s non ha ancora una clip (lancia giv story run senza --scene)", from.ID, from.ID)
}

// checkCap ferma prima di un avvio se la spesa già fatta più la stima delle
// scene ancora da generare, da from in poi, supera il tetto. Tutti i valori
// sono quelli non arrotondati.
func (r *storyRunner) checkCap(from int) error {
	cap := r.s.TettoCostoUSD
	if cap == nil {
		return nil
	}
	spent := r.st.Spent()
	var next float64
	for _, scene := range r.order[from:] {
		if r.toLaunch(scene) {
			next += r.est[scene.ID]
			if r.needKeyframe(scene) {
				next += *scene.FotogrammaChiave.CostoDichiaratoUSD
			}
		}
	}
	if spent+next > *cap {
		removed := ""
		if rs, v := removedSpend(r.s, r.st); len(rs) > 0 {
			removed = " (di cui " + removedText(rs, v) + ")"
		}
		return usageError("tetto_costo_usd %.4f $: spesa già fatta %.4f $%s più stima delle generazioni da fare %.4f $ lo supera; nessun nuovo avvio, stato in %s",
			*cap, round4(spent), removed, round4(next), r.st.Path())
	}
	return nil
}

// toLaunch: la scena va generata (non ha un job da recuperare e non ha una clip,
// oppure è quella di --scene).
func (r *storyRunner) toLaunch(scene *story.Scene) bool {
	ss := r.st.Scenes[scene.ID]
	if ss != nil && ss.Job != nil {
		return false
	}
	if scene.ID == r.forced {
		return true
	}
	if ss != nil && (ss.Status == story.Incerto || ss.Status == story.Fallito) {
		return false // solo con --scene: incerta rifiutata in preflight, fallita elencata a fine esecuzione
	}
	return ss == nil || ss.Clip == nil || !r.exists(ss.Clip.File)
}

// needKeyframe: la scena va generata col fotogramma chiave da fare, la prima
// volta o con --new-keyframe. Uno già registrato non si rigenera da solo, anche
// se il file manca (staleKeyframe).
func (r *storyRunner) needKeyframe(scene *story.Scene) bool {
	if scene.FotogrammaChiave == nil {
		return false
	}
	if scene.ID == r.renew {
		return true
	}
	ss := r.st.Scenes[scene.ID]
	return ss == nil || ss.Keyframe == nil
}

// staleKeyframe rifiuta una scena da generare il cui fotogramma chiave
// registrato non c'è più (o non è un file della cartella dello storyboard):
// rigenerarlo è a pagamento e solo su richiesta, con --new-keyframe.
func (r *storyRunner) staleKeyframe(scene *story.Scene) error {
	if scene.FotogrammaChiave == nil || r.needKeyframe(scene) {
		return nil
	}
	if f := r.st.Scenes[scene.ID].Keyframe.File; !r.exists(f) {
		return usageError("scena %s: il fotogramma chiave registrato %s non c'è più: per generarne uno nuovo (a pagamento) giv story run --scene %s --new-keyframe %s",
			scene.ID, f, scene.ID, r.path)
	}
	return nil
}

func (r *storyRunner) exists(file string) bool {
	_, err := r.s.CheckFile(file)
	return err == nil
}

func takeNumber(ss *story.SceneState) int {
	if ss == nil {
		return 1
	}
	return ss.ClipTakes() + 1
}

// scene genera la scena se serve. Il suo job, se ce n'era uno, è già stato
// recuperato: la ripresa recuperata vale come nuova (anche con --scene).
func (r *storyRunner) scene(ctx context.Context, i int, scene *story.Scene) error {
	ss := r.st.Scenes[scene.ID]
	if t := r.thisRun[scene.ID]; t == "recuperata" || t == "fallita" {
		if scene.ID == r.renew {
			fmt.Fprintf(os.Stderr, "scena %s: c'era un job già partito (%s in questa esecuzione): fotogramma chiave non rigenerato; per rifarla con uno nuovo: --scene %s --new-keyframe\n", scene.ID, t, scene.ID)
		}
		return nil
	}
	if !r.toLaunch(scene) {
		if r.failedNow(scene.ID) {
			return nil
		}
		if r.thisRun[scene.ID] == "" {
			r.thisRun[scene.ID] = "già fatta"
		}
		return nil
	}
	if !r.planned[scene.ID] {
		if err := r.plan([]*story.Scene{scene}, false); err != nil {
			return err
		}
	}
	switch {
	case ss != nil && ss.Status == story.Incerto:
		fmt.Fprintf(os.Stderr, "scena %s: la richiesta precedente è rimasta senza esito (resta contata nella spesa): nuova richiesta\n", scene.ID)
	case ss != nil && ss.Status == story.Fallito:
		fmt.Fprintf(os.Stderr, "scena %s: il job precedente è fallito sul provider (resta contato nella spesa): nuova richiesta\n", scene.ID)
	case ss != nil && ss.Clip != nil && scene.ID != r.forced:
		fmt.Fprintf(os.Stderr, "scena %s: clip %s mancante, la scena si rigenera\n", scene.ID, ss.Clip.File)
	}
	if err := r.checkCap(i); err != nil {
		return err
	}
	return r.launch(ctx, i, scene)
}

// launch genera fotogramma chiave (se serve) e clip della scena. Prima di ogni
// richiesta a pagamento ricontrolla il tetto con la spesa reale e registra la
// generazione nel file di stato (stato incerto): se non si salva, la richiesta
// non parte. Poi salva lo stato a ogni passo: all'avvio del job
// (partito, con l'id), alla fine (fatto) o all'errore.
func (r *storyRunner) launch(ctx context.Context, i int, scene *story.Scene) error {
	ss := r.st.Scene(scene.ID)
	job := r.sc.jobs[scene.ID]
	fail := func(err error) error {
		ss.Error = err.Error()
		if ss.Status == "" {
			ss.Status = idle(ss) // niente inviato: la scena resta da fare
		}
		r.save()
		return err
	}
	// copie private degli ingressi: il provider legge solo file letti qui
	// dentro la cartella dello storyboard e controllati
	work, err := montage.TempDir("giv-story-") // rifiuta una TMPDIR condivisa senza sticky bit
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	var tmp tempInputs // ingressi da URL del fotogramma chiave, scaricati in file privati
	defer tmp.cleanup()

	req := job.req
	req.Refs = slices.Clone(job.req.Refs)
	shown := newManifestInputs(req) // come nello storyboard
	if err := r.staleKeyframe(scene); err != nil {
		return err // nessuna richiesta: il fotogramma si rigenera solo con --new-keyframe
	}
	switch {
	case scene.FotogrammaChiave != nil:
		if r.needKeyframe(scene) {
			img := r.sc.images[scene.ID]
			inputs, err := r.privateInputs(img.req.Inputs, work, &tmp)
			if err != nil {
				return fail(fmt.Errorf("fotogramma_chiave: %w", err))
			}
			ch := story.Charge{Kind: story.ChargeKeyframe, CostDeclaredUSD: scene.FotogrammaChiave.CostoDichiaratoUSD}
			if err := r.reserve(i, ss, ch); err != nil {
				return err
			}
			if err := r.keyframe(ctx, scene, ss, img, inputs); err != nil {
				return fail(fmt.Errorf("fotogramma_chiave: %w", err))
			}
		} else if scene.ID == r.forced {
			fmt.Fprintf(os.Stderr, "scena %s: riuso il fotogramma chiave %s (per rigenerarlo: --new-keyframe)\n", scene.ID, ss.Keyframe.File)
		}
		p, err := r.privateCopy(ss.Keyframe.File, work)
		if err != nil {
			return fail(fmt.Errorf("fotogramma_chiave: %w", err))
		}
		req.Image = p
		shown = newManifestInputs(provider.VideoRequest{Image: r.s.Path(ss.Keyframe.File), LastFrame: req.LastFrame, Refs: req.Refs})
	case scene.ContinuaDa != "":
		p, err := r.lastFrame(ctx, scene, work)
		if err != nil {
			return fail(err)
		}
		req.Image = p
		shown = newManifestInputs(provider.VideoRequest{Image: "ultimo fotogramma di " + scene.ContinuaDa, LastFrame: req.LastFrame, Refs: req.Refs})
	}
	for _, in := range videoInputs(&req) {
		if *in.path == "" || isURL(*in.path) || strings.HasPrefix(*in.path, work+string(filepath.Separator)) {
			continue
		}
		rel, err := r.s.Rel(*in.path)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", in.flag, err))
		}
		if *in.path, err = r.privateCopy(rel, work); err != nil {
			return fail(fmt.Errorf("%s: %w", in.flag, err))
		}
	}
	if err := checkVideoInputs(&req); err != nil {
		return fail(err)
	}
	job.req, job.inputs = req, shown
	job.save = func(slug string, res *provider.Result) ([]output.FileInfo, error) {
		return r.saveFiles(slug, res.Media)
	}

	ch := story.Charge{Kind: story.ChargeClip, CostEstUSD: job.plan.costExact}
	if ch.CostEstUSD == nil {
		ch.CostDeclaredUSD = scene.CostoDichiaratoUSD
	}
	if err := r.reserve(i, ss, ch); err != nil {
		return err
	}
	k := len(ss.Charges) - 1
	started := false
	prev := provider.OnJobStart
	provider.OnJobStart = func(p string, j provider.Job, req provider.VideoRequest) {
		if prev != nil {
			prev(p, j, req) // giv-jobs.jsonl, come giv video
		}
		started = true
		r.man.Started++
		ss.Status, ss.Error = story.Partito, ""
		ss.Job = &story.Job{Provider: p, ID: j.ID, PollingURL: j.PollingURL, Model: req.Model, Started: time.Now()}
		ss.Charges[k].JobID = j.ID
		r.save()
	}
	defer func() { provider.OnJobStart = prev }()

	man, err := job.run(ctx)
	if err != nil {
		var se *httpx.StartError
		switch {
		case started && errors.Is(err, provider.ErrJobFailed):
			ss.Status, ss.Job = story.Fallito, nil // la spesa resta contata, si rifà solo con --scene
		case started:
			ss.Status = story.Partito // il job resta: la prossima esecuzione lo recupera, non lo rilancia
		case errors.As(err, &se):
			// avvio senza esito: forse addebitato, si rifà solo con --scene
		default:
			// avvio rifiutato (4xx) o mai partito: nessun job, niente addebito,
			// la scena resta da fare e la prossima esecuzione la riprova
			ss.Charges = slices.Delete(ss.Charges, k, k+1)
			ss.Status = idle(ss)
		}
		return fail(err)
	}
	r.thisRun[scene.ID] = "generata"
	return r.done(scene, ss, man, "story video")
}

// reserve ricontrolla il tetto con la spesa fatta fin qui e registra la
// generazione ch come incerta nel file di stato, prima di inviarla: se lo
// stato non si salva la richiesta non parte.
func (r *storyRunner) reserve(i int, ss *story.SceneState, ch story.Charge) error {
	if err := r.checkCap(i); err != nil {
		return err
	}
	status, msg := ss.Status, ss.Error
	ch.At = time.Now()
	ss.Charges = append(ss.Charges, ch)
	ss.Status, ss.Error = story.Incerto, ""
	if err := r.st.Save(); err != nil {
		ss.Charges = ss.Charges[:len(ss.Charges)-1]
		ss.Status, ss.Error = status, msg
		return fmt.Errorf("stato non salvato, nessuna richiesta inviata: %w", err)
	}
	return nil
}

// keyframe genera il fotogramma chiave della scena (una sola richiesta, senza
// ritentativi) e lo registra: le riprese successive lo riusano finché il file
// c'è; con --new-keyframe il precedente passa fra le riprese precedenti. La
// generazione è già registrata come ultima di ss.Charges: se la risposta
// arriva (addebitata) ma il file non si salva, o l'esito non si sa, la riserva
// resta e la scena è incerta (si rifà solo con --scene). Se lo stato col
// fotogramma registrato non si salva, la scena si ferma prima del video.
func (r *storyRunner) keyframe(ctx context.Context, scene *story.Scene, ss *story.SceneState, img *imageJob, inputs []string) error {
	k := len(ss.Charges) - 1
	received := false
	img.req.Inputs = inputs
	img.save = func(slug string, res *provider.Result) ([]output.FileInfo, error) {
		received = true
		ss.Charges[k].CostUSD = res.CostUSD
		return r.saveFiles(slug, res.Media)
	}
	man, err := img.run(httpx.WithoutRetry(ctx))
	if err != nil {
		switch {
		case received, uncertainStart(err):
			// arrivata e addebitata senza file salvato, o senza esito: resta incerta
		default:
			// rifiutata (4xx) o mai partita: niente addebito, la scena resta da fare
			ss.Charges = slices.Delete(ss.Charges, k, k+1)
			ss.Status = idle(ss)
		}
		return err
	}
	r.man.Keyframes++
	man.Command = "story image"
	logRun(man)
	if len(man.Files) == 0 {
		return errors.New("nessuna immagine nella risposta") // addebitata: resta incerta
	}
	rel, err := r.s.Rel(man.Files[0].Path)
	if err != nil {
		return err
	}
	ss.Status = story.DaFare
	if ss.Clip != nil {
		ss.Status = story.Fatto // la clip precedente resta in uso
	}
	if scene.ID == r.renew {
		if ss.Keyframe != nil {
			old := *ss.Keyframe
			old.Kind = story.ChargeKeyframe
			ss.Previous = append(ss.Previous, old)
		}
		r.renew = "" // generato: il tetto non lo conta più fra le richieste da fare
	}
	ss.Keyframe = &story.Take{File: rel, Done: time.Now()}
	// il costo reale del fotogramma entra nel file di stato prima del video:
	// se non si salva il video non parte (launch riprova a salvare)
	if err := r.st.Save(); err != nil {
		return fmt.Errorf("fotogramma chiave %s generato (addebito %.4f $) ma stato non salvato, nessuna richiesta video inviata: %w",
			rel, ss.Charges[k].CostUSD, err)
	}
	return nil
}

// idle è lo stato di una scena senza richieste in corso: fatta se ha una clip
// in uso, altrimenti da fare.
func idle(ss *story.SceneState) string {
	if ss.Clip != nil {
		return story.Fatto
	}
	return story.DaFare
}

// uncertainStart: l'errore di una richiesta di generazione non dice se è stata
// addebitata (5xx, risposta persa, interruzione); un 4xx o una connessione mai
// aperta dicono di no.
func uncertainStart(err error) bool {
	var se *httpx.StartError
	return errors.As(httpx.Start(err), &se)
}

// privateInputs copia nella cartella privata work gli ingressi locali di un
// fotogramma chiave; gli URL li scarica tmp in file privati.
func (r *storyRunner) privateInputs(inputs []string, work string, tmp *tempInputs) ([]string, error) {
	out := make([]string, len(inputs))
	for i, in := range inputs {
		var err error
		if isURL(in) {
			out[i], err = tmp.resolve(in)
		} else {
			var rel string
			if rel, err = r.s.Rel(in); err == nil {
				out[i], err = r.privateCopy(rel, work)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("--input %s: %w", shownInput(in), err)
		}
	}
	return out, nil
}

// privateCopy legge rel (relativo alla cartella dello storyboard) solo dentro
// la cartella, anche seguendo i link, controlla che sia un'immagine entro i
// limiti e ne scrive una copia privata in work: il provider legge quella.
func (r *storyRunner) privateCopy(rel, work string) (string, error) {
	f, err := r.root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("%s: file non leggibile dentro la cartella dello storyboard: %w", rel, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: non è un file regolare", rel)
	}
	if fi.Size() > provider.MaxInputBytes {
		return "", fmt.Errorf("%s: oltre il limite di %d MiB per immagine", rel, provider.MaxInputBytes>>20)
	}
	data, err := io.ReadAll(io.LimitReader(f, provider.MaxInputBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > provider.MaxInputBytes {
		return "", fmt.Errorf("%s: oltre il limite di %d MiB per immagine", rel, provider.MaxInputBytes>>20)
	}
	mime := provider.ImageMime(data)
	if mime == "" {
		return "", fmt.Errorf("%s: non è un'immagine png, jpeg, webp o gif", rel)
	}
	out, err := os.CreateTemp(work, "in-*"+extForMime(mime))
	if err != nil {
		return "", err
	}
	if _, err := out.Write(data); err != nil {
		out.Close()
		return "", err
	}
	return out.Name(), out.Close()
}

// saveFiles scrive i media generati in clip/ dentro la cartella dello
// storyboard (una cartella clip che porta fuori, anche con un link, è
// rifiutata) creando file nuovi: un nome che esiste già, anche come link, non
// si sovrascrive mai.
func (r *storyRunner) saveFiles(slug string, media []provider.Media) ([]output.FileInfo, error) {
	if err := r.root.MkdirAll(storyClipDir, 0o755); err != nil {
		return nil, err
	}
	dir, err := r.root.OpenRoot(storyClipDir)
	if err != nil {
		return nil, fmt.Errorf("cartella %s: %w", storyClipDir, err)
	}
	defer dir.Close()
	ts := time.Now().Format("20060102-150405")
	var files []output.FileInfo
	for i, m := range media {
		name := slug + "-" + ts
		if len(media) > 1 {
			name = fmt.Sprintf("%s-%d", name, i+1)
		}
		name += output.Ext(m.Mime)
		f, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", storyClipDir, name, err)
		}
		_, err = f.Write(m.Data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", storyClipDir, name, err)
		}
		files = append(files, output.FileInfo{Path: filepath.Join(r.s.Dir, storyClipDir, name), Mime: m.Mime, Bytes: len(m.Data)})
	}
	return files, nil
}

// lastFrame estrae nella cartella privata work l'ultimo fotogramma della scena
// di continua_da, primo fotogramma della nuova; ffmpeg legge una copia privata
// della clip, letta dentro la cartella dello storyboard.
func (r *storyRunner) lastFrame(ctx context.Context, scene *story.Scene, work string) (string, error) {
	from := r.s.Scena(scene.ContinuaDa)
	rel := from.File
	if from.Tipo != story.Clip {
		ss := r.st.Scenes[from.ID]
		if ss == nil || ss.Clip == nil {
			return "", fmt.Errorf("continua_da %s: %w", from.ID, story.ErrNotDone)
		}
		rel = ss.Clip.File
	}
	clip, err := r.copyClip(rel, work)
	if err != nil {
		return "", fmt.Errorf("continua_da %s: %w", from.ID, err)
	}
	out := filepath.Join(work, "primo.png")
	if err := storyFrames.LastFrame(ctx, clip, out, false); err != nil {
		return "", fmt.Errorf("continua_da %s: %w", from.ID, err)
	}
	return out, nil
}

// copyClip copia in work la clip rel, letta solo dentro la cartella dello storyboard.
func (r *storyRunner) copyClip(rel, work string) (string, error) {
	f, err := r.root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("clip %s non leggibile dentro la cartella dello storyboard: %w", rel, err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("clip %s: non è un file regolare", rel)
	}
	out, err := os.CreateTemp(work, "clip-*"+strings.ToLower(filepath.Ext(rel)))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		return "", err
	}
	return out.Name(), out.Close()
}

// recover scarica il job già partito della scena senza un nuovo avvio. Se il
// provider dice che il job è finito male (provider.ErrJobFailed) la scena è
// fallita, spesa contata: si rifà solo con --scene. Ogni altro errore lascia il
// job, da recuperare alla prossima esecuzione.
func (r *storyRunner) recover(ctx context.Context, scene *story.Scene) error {
	ss := r.st.Scenes[scene.ID]
	j := ss.Job
	c, err := r.sc.client(j.Provider)
	if err != nil {
		return err
	}
	f := c.Provider.(provider.JobFetcher) // controllato in preflight
	if !slices.ContainsFunc(ss.Charges, func(ch story.Charge) bool { return ch.Kind == story.ChargeClip && ch.JobID == j.ID }) {
		// ogni job recuperato conta nella spesa
		ss.Charges = append(ss.Charges, story.Charge{Kind: story.ChargeClip, JobID: j.ID, CostDeclaredUSD: scene.CostoDichiaratoUSD, At: time.Now()})
		r.save()
	}
	fmt.Fprintf(os.Stderr, "scena %s: recupero del job %s (%s), senza nuovo avvio\n", scene.ID, j.ID, j.Provider)
	res, err := f.FetchVideo(ctx, provider.Job{ID: j.ID, PollingURL: j.PollingURL}, storyTimeout)
	if err != nil {
		ss.Error = err.Error()
		if errors.Is(err, provider.ErrJobFailed) {
			ss.Status, ss.Job = story.Fallito, nil
			r.save()
			return err
		}
		ss.Status = story.Partito
		r.save()
		return fmt.Errorf("%w (il job resta: si recupera alla prossima esecuzione)", err)
	}
	files, err := r.saveFiles(fmt.Sprintf("%s-r%d", scene.ID, takeNumber(ss)), res.Media)
	if err != nil {
		ss.Error = err.Error()
		r.save()
		return err
	}
	man := manifest{Provider: c.Name(), Model: j.Model, Prompt: scene.Prompt}
	man.done(files, res)
	r.man.Recovered++
	r.thisRun[scene.ID] = "recuperata"
	return r.done(scene, ss, man, "story video")
}

// done registra la nuova ripresa della scena (la precedente resta in previous,
// file compreso) e l'addebito del suo job.
func (r *storyRunner) done(scene *story.Scene, ss *story.SceneState, man manifest, command string) error {
	man.Command = command
	logRun(man)
	file := ""
	for _, f := range man.Files {
		if strings.HasPrefix(f.Mime, "video/") {
			file = f.Path
			break
		}
	}
	if file == "" {
		ss.Error = "nessun video nella risposta"
		ss.Status, ss.Job = story.Fallito, nil // job finito, forse addebitato: si rifà solo con --scene
		r.save()
		return errors.New(ss.Error)
	}
	rel, err := r.s.Rel(file)
	if err != nil {
		return err
	}
	if ss.Clip != nil {
		ss.Previous = append(ss.Previous, *ss.Clip)
	}
	ss.Clip = &story.Take{File: rel, JobID: man.JobID, Done: time.Now()}
	ss.Status, ss.Job, ss.Error = story.Fatto, nil, ""
	for i := range ss.Charges {
		if c := &ss.Charges[i]; c.Kind == story.ChargeClip && c.JobID != "" && c.JobID == man.JobID {
			c.CostUSD = man.CostUSD
		}
	}
	r.save()
	return nil
}

// save scrive lo stato; un errore è un avviso: l'ultima generazione registrata
// prima dell'invio resta nel file di stato (il job anche in giv-jobs.jsonl).
func (r *storyRunner) save() {
	if err := r.st.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "avviso: %v\n", err)
	}
}

// summary compila il manifest finale dallo stato: per scena stima e addebito.
func (r *storyRunner) summary() {
	for i := range r.s.Scene {
		scene := &r.s.Scene[i]
		row := sceneRun{ID: scene.ID, Tipo: scene.Tipo}
		if scene.Tipo != story.Video {
			row.File = scene.File
			r.man.Scenes = append(r.man.Scenes, row)
			continue
		}
		row.ThisRun = r.thisRun[scene.ID]
		if ss := r.st.Scenes[scene.ID]; ss != nil {
			row.Status = ss.Status
			if ss.Clip != nil {
				row.File = ss.Clip.File
				row.Takes = ss.ClipTakes()
			}
			if ss.Keyframe != nil {
				row.Keyframe = ss.Keyframe.File
			}
			var est, cost float64
			for _, c := range ss.Charges {
				switch {
				case c.CostEstUSD != nil:
					est += *c.CostEstUSD
				case c.CostDeclaredUSD != nil:
					est += *c.CostDeclaredUSD
				}
				cost += c.CostUSD
			}
			row.CostEstUSD, row.CostUSD = round4(est), round4(cost)
			r.man.CostEstUSD += est
			r.man.CostUSD += cost
		}
		r.man.Scenes = append(r.man.Scenes, row)
	}
	r.man.CostEstUSD, r.man.CostUSD = round4(r.man.CostEstUSD), round4(r.man.CostUSD)
	r.man.SpentUSD = round4(r.st.Spent())
	var removed float64
	r.man.RemovedScenes, removed = removedSpend(r.s, r.st)
	r.man.RemovedSpentUSD = round4(removed)
}
