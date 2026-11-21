package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gen-image-video-cli/internal/httpx"
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

// storyFrames estrae i fotogrammi di continua_da: diventa il pacchetto di
// montaggio (ffmpeg) quando c'è.
var storyFrames frameExtractor = noFrames{}

type noFrames struct{}

func (noFrames) Check() error {
	return errors.New("continua_da: estrazione dell'ultimo fotogramma non ancora disponibile (serve il montaggio con ffmpeg)")
}

func (n noFrames) LastFrame(context.Context, string, string, bool) error { return n.Check() }

// cmdStoryRun genera le scene dello storyboard in ordine, una richiesta alla
// volta, con lo stato per scena accanto allo storyboard: una seconda esecuzione
// riprende dalle scene mancanti e recupera i job già partiti senza rilanciarli.
func cmdStoryRun(args []string) error {
	fl := flag.NewFlagSet("story run", flag.ExitOnError)
	only := fl.String("scene", "", "rifà solo questa scena (id); la ripresa precedente resta")
	if err := fl.Parse(args); err != nil {
		return usageError("%v", err)
	}
	// flag anche dopo il file
	rest := fl.Args()
	if len(rest) == 0 {
		return usageError("uso: giv story run [--scene <id>] <storyboard.json>")
	}
	path := rest[0]
	if err := fl.Parse(rest[1:]); err != nil {
		return usageError("%v", err)
	}
	if fl.NArg() > 0 {
		return usageError("uso: giv story run [--scene <id>] <storyboard.json>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	man, err := runStory(ctx, path, *only)
	if err != nil {
		return err
	}
	return output.PrintJSON(man)
}

// storyRunManifest è l'uscita di giv story run: per scena stima e addebito affiancati.
type storyRunManifest struct {
	Command    string     `json:"command"`
	Storyboard string     `json:"storyboard"`
	State      string     `json:"state"`
	Titolo     string     `json:"titolo,omitempty"`
	Scene      []sceneRun `json:"scene"`
	Started    int        `json:"jobs_started"`   // job video avviati in questa esecuzione
	Recovered  int        `json:"jobs_recovered"` // job già partiti, scaricati senza nuovo avvio
	Keyframes  int        `json:"keyframes"`      // fotogrammi chiave generati in questa esecuzione
	CostEstUSD float64    `json:"cost_est_usd"`   // stime (o costi dichiarati) di tutte le generazioni
	CostUSD    float64    `json:"cost_usd"`       // addebito riportato dai provider
	SpentUSD   float64    `json:"spent_usd"`      // per il tetto: addebito dove c'è, altrimenti stima
	CapUSD     *float64   `json:"cap_usd,omitempty"`
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
	path    string
	sc      *storyChecker
	order   []*story.Scene     // scene video da trattare, in ordine
	forced  string             // --scene: rifatta anche se fatta
	est     map[string]float64 // stima (o costo dichiarato) della clip, per scena pianificata
	planned map[string]bool
	thisRun map[string]string
	man     storyRunManifest
}

func runStory(ctx context.Context, path, only string) (*storyRunManifest, error) {
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
	}
	statePath := story.StatePath(path)
	unlock, err := story.LockState(statePath)
	if err != nil {
		return nil, usageError("%v", err)
	}
	defer unlock()
	st, err := story.LoadState(statePath)
	if err != nil {
		return nil, usageError("%v", err)
	}
	r := &storyRunner{s: s, st: st, path: path, sc: newStoryChecker(s), forced: only,
		est: map[string]float64{}, planned: map[string]bool{}, thisRun: map[string]string{}}
	r.sc.out = filepath.Join(s.Dir, storyClipDir)
	r.man = storyRunManifest{Command: "story run", Storyboard: path, State: statePath, Titolo: s.Titolo, CapUSD: s.TettoCostoUSD}
	for i := range s.Scene {
		if sc := &s.Scene[i]; sc.Tipo == story.Video && (only == "" || sc.ID == only) {
			r.order = append(r.order, sc)
		}
	}
	if err := r.preflight(); err != nil {
		return nil, err
	}
	for i, scene := range r.order {
		if err := r.scene(ctx, i, scene); err != nil {
			return nil, fmt.Errorf("scena %s: %w", scene.ID, err)
		}
	}
	r.summary()
	return &r.man, nil
}

// preflight controlla tutto prima del primo avvio: job da recuperare, scene da
// generare (capacità, preset, stime sul catalogo letto adesso), fotogrammi di
// continua_da e tetto di spesa. Ogni rifiuto è codice 2 e nessun avvio.
func (r *storyRunner) preflight() error {
	var rejects []error
	var launch []*story.Scene
	for _, scene := range r.order {
		ss := r.st.Scenes[scene.ID]
		if ss != nil && ss.Job != nil {
			if err := r.checkJob(ss.Job); err != nil {
				rejects = append(rejects, fmt.Errorf("scena %s: %w", scene.ID, err))
			}
			continue
		}
		if r.toLaunch(scene) {
			launch = append(launch, scene)
		}
	}
	if len(rejects) > 0 {
		return storyError(r.st.Path(), errors.Join(rejects...))
	}
	return r.plan(launch, true)
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
	for _, c := range out.Scene {
		r.planned[c.ID] = true
		switch {
		case c.CostEstUSD != nil:
			r.est[c.ID] = *c.CostEstUSD
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
				return &cliError{code: exitError, msg: fmt.Sprintf("scena %s: %v", c.ID, err)}
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
	if ss := r.st.Scenes[from.ID]; ss != nil && (ss.Clip != nil || ss.Job != nil) {
		return nil
	}
	return fmt.Errorf("continua_da %s: la scena %s non ha ancora una clip (lancia giv story run senza --scene)", from.ID, from.ID)
}

// checkCap ferma prima di un avvio se la spesa già fatta più la stima delle
// scene ancora da generare, da from in poi, supera il tetto.
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
		return usageError("tetto_costo_usd %.4f $: spesa già fatta %.4f $ più stima delle scene da generare %.4f $ lo supera; nessun nuovo avvio, stato in %s",
			*cap, round4(spent), round4(next), r.st.Path())
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
	return ss == nil || ss.Clip == nil || !r.exists(ss.Clip.File)
}

func (r *storyRunner) needKeyframe(scene *story.Scene) bool {
	if scene.FotogrammaChiave == nil {
		return false
	}
	ss := r.st.Scenes[scene.ID]
	return ss == nil || ss.Keyframe == nil || !r.exists(ss.Keyframe.File)
}

func (r *storyRunner) exists(file string) bool {
	_, err := r.s.CheckFile(file)
	return err == nil
}

func takeNumber(ss *story.SceneState) int {
	if ss == nil {
		return 1
	}
	n := len(ss.Previous) + 1
	if ss.Clip != nil {
		n++
	}
	return n
}

// scene tratta una scena: recupera il suo job se ce n'è uno, altrimenti la
// genera se serve.
func (r *storyRunner) scene(ctx context.Context, i int, scene *story.Scene) error {
	ss := r.st.Scenes[scene.ID]
	if ss != nil && ss.Job != nil {
		done, err := r.recover(ctx, scene)
		if err != nil || done {
			return err
		}
		// job fallito sul provider: la scena si rigenera, col tetto
	}
	if !r.toLaunch(scene) {
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
	if ss != nil && ss.Clip != nil && scene.ID != r.forced {
		fmt.Fprintf(os.Stderr, "scena %s: clip %s mancante, la scena si rigenera\n", scene.ID, ss.Clip.File)
	}
	if err := r.checkCap(i); err != nil {
		return err
	}
	return r.launch(ctx, scene)
}

// launch genera fotogramma chiave (se serve) e clip della scena, salvando lo
// stato a ogni passo: all'avvio del job (partito, con l'id), alla fine (fatto)
// o all'errore.
func (r *storyRunner) launch(ctx context.Context, scene *story.Scene) error {
	ss := r.st.Scene(scene.ID)
	job := r.sc.jobs[scene.ID]
	fail := func(err error) error {
		ss.Error = err.Error()
		if ss.Job == nil {
			ss.Status = story.Fallito
		}
		r.save()
		return err
	}
	image := ""
	switch {
	case scene.FotogrammaChiave != nil:
		if r.needKeyframe(scene) {
			if err := r.keyframe(ctx, scene, ss); err != nil {
				return fail(fmt.Errorf("fotogramma_chiave: %w", err))
			}
		}
		p, err := r.s.CheckFile(ss.Keyframe.File)
		if err != nil {
			return fail(fmt.Errorf("fotogramma_chiave: %w", err))
		}
		image = p
	case scene.ContinuaDa != "":
		p, err := r.lastFrame(ctx, scene)
		if err != nil {
			return fail(err)
		}
		image = p
	}
	if image != "" {
		job.req.Image = image
		if err := checkVideoInputs(&job.req); err != nil {
			return fail(err)
		}
	}

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
		ch := story.Charge{Kind: story.ChargeClip, JobID: j.ID, CostEstUSD: job.plan.CostEstUSD, At: time.Now()}
		if ch.CostEstUSD == nil {
			ch.CostDeclaredUSD = scene.CostoDichiaratoUSD
		}
		ss.Charges = append(ss.Charges, ch)
		r.save()
	}
	defer func() { provider.OnJobStart = prev }()

	start := time.Now()
	man, err := job.run(ctx)
	if err != nil {
		if started {
			if recoverable(err, time.Since(start) >= job.req.Timeout) {
				ss.Status = story.Partito // la prossima esecuzione lo recupera
			} else {
				ss.Status = story.Fallito // la prossima esecuzione prova a recuperarlo, poi rilancia
			}
		}
		return fail(err)
	}
	r.thisRun[scene.ID] = "generata"
	return r.done(scene, ss, man, "story video")
}

// keyframe genera il fotogramma chiave della scena e lo registra: le riprese
// successive lo riusano finché il file c'è.
func (r *storyRunner) keyframe(ctx context.Context, scene *story.Scene, ss *story.SceneState) error {
	img := r.sc.images[scene.ID]
	man, err := img.run(ctx)
	if err != nil {
		return err
	}
	r.man.Keyframes++
	ss.Charges = append(ss.Charges, story.Charge{Kind: story.ChargeKeyframe, CostDeclaredUSD: scene.FotogrammaChiave.CostoDichiaratoUSD,
		CostUSD: man.CostUSD, At: time.Now()})
	man.Command = "story image"
	logRun(man)
	if len(man.Files) == 0 {
		r.save()
		return errors.New("nessuna immagine nella risposta")
	}
	rel, err := r.s.Rel(man.Files[0].Path)
	if err != nil {
		r.save()
		return err
	}
	ss.Keyframe = &story.Take{File: rel, Done: time.Now()}
	r.save()
	return nil
}

// lastFrame estrae l'ultimo fotogramma della scena di continua_da, primo
// fotogramma della nuova.
func (r *storyRunner) lastFrame(ctx context.Context, scene *story.Scene) (string, error) {
	from := r.s.Scena(scene.ContinuaDa)
	var clip string
	var err error
	if from.Tipo == story.Clip {
		clip, err = r.s.CheckFile(from.File)
	} else {
		clip, err = r.st.ClipFile(r.s, from.ID)
	}
	if err != nil {
		return "", fmt.Errorf("continua_da %s: %w", from.ID, err)
	}
	if err := os.MkdirAll(r.sc.out, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(r.sc.out, scene.ID+"-primo.png")
	if err := storyFrames.LastFrame(ctx, clip, out, true); err != nil {
		return "", fmt.Errorf("continua_da %s: %w", from.ID, err)
	}
	return out, nil
}

// recover scarica il job già partito della scena senza un nuovo avvio. done è
// false se il provider dice che il job è fallito: la scena va rigenerata.
func (r *storyRunner) recover(ctx context.Context, scene *story.Scene) (done bool, err error) {
	ss := r.st.Scenes[scene.ID]
	j := ss.Job
	c, err := r.sc.client(j.Provider)
	if err != nil {
		return false, err
	}
	f := c.Provider.(provider.JobFetcher) // controllato in preflight
	fmt.Fprintf(os.Stderr, "scena %s: recupero del job %s (%s), senza nuovo avvio\n", scene.ID, j.ID, j.Provider)
	start := time.Now()
	res, err := f.FetchVideo(ctx, provider.Job{ID: j.ID, PollingURL: j.PollingURL}, storyTimeout)
	if err != nil {
		ss.Error = err.Error()
		if recoverable(err, time.Since(start) >= storyTimeout) {
			ss.Status = story.Partito
			r.save()
			return false, err
		}
		// fallito sul provider: il job non si recupera più, la scena si rigenera
		fmt.Fprintf(os.Stderr, "avviso: scena %s: job %s non recuperabile (%v): la scena si rigenera\n", scene.ID, j.ID, err)
		ss.Status, ss.Job = story.Fallito, nil
		r.save()
		return false, nil
	}
	files, err := output.SaveAll(r.sc.out, fmt.Sprintf("%s-r%d", scene.ID, takeNumber(ss)), res.Media)
	if err != nil {
		ss.Error = err.Error()
		r.save()
		return false, err
	}
	man := manifest{Provider: c.Name(), Model: j.Model, Prompt: scene.Prompt}
	man.done(files, res)
	r.man.Recovered++
	r.thisRun[scene.ID] = "recuperata"
	return true, r.done(scene, ss, man, "story video")
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
		ss.Status, ss.Job = story.Fallito, nil
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

// save scrive lo stato; un errore è un avviso (il job resta anche in giv-jobs.jsonl).
func (r *storyRunner) save() {
	if err := r.st.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "avviso: stato non salvato in %s: %v\n", r.st.Path(), err)
	}
}

// recoverable: errore dopo cui il job può ancora finire bene (interruzione,
// rete, tetto di attesa, provider momentaneamente non disponibile).
func recoverable(err error, timedOut bool) bool {
	if timedOut || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || httpx.IsNetwork(err) {
		return true
	}
	var he *httpx.Error
	return errors.As(err, &he) && (he.Status >= 500 || he.Status == 429)
}

// summary compila il manifest finale dallo stato: per scena stima e addebito.
func (r *storyRunner) summary() {
	for i := range r.s.Scene {
		scene := &r.s.Scene[i]
		row := sceneRun{ID: scene.ID, Tipo: scene.Tipo}
		if scene.Tipo != story.Video {
			row.File = scene.File
			r.man.Scene = append(r.man.Scene, row)
			continue
		}
		row.ThisRun = r.thisRun[scene.ID]
		if ss := r.st.Scenes[scene.ID]; ss != nil {
			row.Status = ss.Status
			if ss.Clip != nil {
				row.File = ss.Clip.File
				row.Takes = len(ss.Previous) + 1
			}
			if ss.Keyframe != nil {
				row.Keyframe = ss.Keyframe.File
			}
			for _, c := range ss.Charges {
				switch {
				case c.CostEstUSD != nil:
					row.CostEstUSD += *c.CostEstUSD
				case c.CostDeclaredUSD != nil:
					row.CostEstUSD += *c.CostDeclaredUSD
				}
				row.CostUSD += c.CostUSD
			}
			row.CostEstUSD, row.CostUSD = round4(row.CostEstUSD), round4(row.CostUSD)
			r.man.CostEstUSD += row.CostEstUSD
			r.man.CostUSD += row.CostUSD
		}
		r.man.Scene = append(r.man.Scene, row)
	}
	r.man.CostEstUSD, r.man.CostUSD = round4(r.man.CostEstUSD), round4(r.man.CostUSD)
	r.man.SpentUSD = round4(r.st.Spent())
}
