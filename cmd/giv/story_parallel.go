package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
	"gen-image-video-cli/internal/story"
)

// Scene in parallelo (giv story run --parallel N, default.parallelo). Il
// coordinatore è il solo a scrivere lo stato e a chiedere alla cassa: sceglie
// in ordine di storyboard fra le scene pronte (continua_da aspetta la clip
// della precedente), riserva, e affida al lavoratore l'unico POST; il
// prossimo avvio parte solo quando il lavoratore ha avviato il job (o ha
// finito), quindi gli avvii restano uno alla volta e in ordine e si
// sovrappongono solo attesa e scaricamento, fino a N lavori aperti. I
// lavoratori non toccano lo stato: restituiscono eventi identificati da scena
// e ripresa (job avviato, lavoro finito) che il coordinatore registra.

// maxParallel è il massimo di --parallel e default.parallelo.
const maxParallel = story.MaxParallelo

// sceneWork è il lavoro aperto di una scena: un job da avviare (avvio) o già
// partito (recupero), atteso e scaricato da un lavoratore. Lo legge e scrive
// solo il coordinatore; il lavoratore usa solo run.
type sceneWork struct {
	scene   *story.Scene
	ss      *story.SceneState
	take    int  // ripresa che il lavoro produce (clip numero take)
	k       int  // avvio: riserva della clip in ss.Charges
	recover bool // job già partito, scaricato senza nuovo avvio
	command string
	fail    func(error) error // avvio: registra l'errore nello stato
	cleanup func()            // copie private degli ingressi, tolte a lavoro finito
	run     func(context.Context) (manifest, error)

	started  bool // avvio: il provider ha avviato il job (OnJobStart)
	finished bool
	fetched  bool // recupero: job scaricato (un errore dopo è dei file)
}

// workEvent è ciò che un lavoratore restituisce al coordinatore, per scena e
// ripresa: il job appena avviato (start) oppure la fine del lavoro.
type workEvent struct {
	id    string
	take  int
	start *jobStart
	man   manifest
	err   error
}

// jobStart è il job video appena avviato, come lo passa provider.OnJobStart.
type jobStart struct {
	provider string
	job      provider.Job
	req      provider.VideoRequest
}

// parallelism è il numero di lavori aperti insieme: --parallel, poi
// default.parallelo, poi 1.
func parallelism(flag int, s *story.Storyboard) int {
	switch {
	case flag > 0:
		return flag
	case s.Parallelo() > 0:
		return s.Parallelo() // da 1 a 4: controllato con lo storyboard
	}
	return 1
}

// openWork apre il lavoro w della scena e lo affida a un lavoratore.
func (r *storyRunner) openWork(ctx context.Context, w *sceneWork) {
	r.open[w.scene.ID] = w
	id, take, run := w.scene.ID, w.take, w.run
	go func() {
		man, err := run(ctx)
		r.events <- workEvent{id: id, take: take, man: man, err: err}
	}()
}

// jobStarted è provider.OnJobStart durante le scene: il job appena avviato
// va al coordinatore come evento del lavoro in avvio (ce n'è uno alla volta).
func (r *storyRunner) jobStarted(p string, j provider.Job, req provider.VideoRequest) {
	w := r.starting.Load()
	if w == nil {
		// nessun avvio di scena in corso: solo il registro dei job
		if r.prevJobStart != nil {
			r.prevJobStart(p, j, req)
		}
		return
	}
	r.events <- workEvent{id: w.scene.ID, take: w.take, start: &jobStart{provider: p, job: j, req: req}}
}

// next riceve e registra il prossimo evento di un lavoratore.
func (r *storyRunner) next() {
	ev := <-r.events
	w := r.open[ev.id]
	if w == nil || w.take != ev.take {
		// non può succedere: ogni lavoro aperto è nella mappa fino alla fine
		fmt.Fprintf(os.Stderr, "avviso: scena %s, ripresa %d: risultato senza lavoro aperto, ignorato\n", ev.id, ev.take)
		return
	}
	if ev.start != nil {
		r.workStarted(w, ev.start)
		return
	}
	w.finished = true
	delete(r.open, ev.id)
	if w.cleanup != nil {
		w.cleanup()
	}
	var err error
	if w.recover {
		err = r.recovered(w, ev.man, ev.err)
	} else {
		err = r.clipDone(w, ev.man, ev.err)
	}
	r.sceneErr(w.scene.ID, err)
}

// drain aspetta la fine dei lavori aperti, registrandone i risultati.
func (r *storyRunner) drain() {
	for len(r.open) > 0 {
		r.next()
	}
}

// workStarted registra il job avviato dal lavoro w: partito, con l'id, prima
// che il lavoratore lo aspetti e scarichi.
func (r *storyRunner) workStarted(w *sceneWork, s *jobStart) {
	if r.prevJobStart != nil {
		r.prevJobStart(s.provider, s.job, s.req) // giv-jobs.jsonl, come giv video
	}
	w.started = true
	r.man.Started++
	ss := w.ss
	ss.Status, ss.Error = story.Partito, ""
	ss.Job = &story.Job{Provider: s.provider, ID: s.job.ID, PollingURL: s.job.PollingURL, Model: s.req.Model, Started: time.Now()}
	ss.Charges[w.k].JobID = s.job.ID
	r.save()
}

// clipDone registra la fine dell'avvio di una clip: col job avviato e finito
// male la scena è fallita (spesa contata); avviato e non scaricato resta
// partito (si recupera, non si rilancia); senza esito resta incerta e
// riservata (si rifà solo con --scene); rifiutato o mai partito (caricamento
// compreso) la riserva si toglie e la scena resta da fare.
func (r *storyRunner) clipDone(w *sceneWork, man manifest, err error) error {
	ss := w.ss
	if err != nil {
		var se *httpx.StartError
		switch {
		case w.started && errors.Is(err, provider.ErrJobFailed):
			ss.Status, ss.Job = story.Fallito, nil // la spesa resta contata, si rifà solo con --scene
			settle(ss, w.k, 0, 0)
		case w.started:
			ss.Status = story.Partito // il job resta: la prossima esecuzione lo recupera, non lo rilancia
		case errors.As(err, &se):
			// avvio senza esito: forse addebitato, resta riservato, si rifà solo con --scene
			ss.MarkUncertain(w.k)
		default:
			// avvio rifiutato (4xx) o mai partito: nessun job, niente addebito,
			// la scena resta da fare e la prossima esecuzione la riprova
			releaseCharge(ss, w.k)
			ss.Status = idle(ss)
		}
		return w.fail(err)
	}
	r.thisRun[w.scene.ID] = "generata"
	return r.done(w.scene, ss, man, w.command)
}

// sceneStop è un errore che ferma i nuovi avvii, con la scena (vuota se non
// è di una scena).
type sceneStop struct {
	id  string
	err error
}

// sceneErr registra l'errore della scena id: una scena fallita sul provider
// va nell'elenco di fine esecuzione, ogni altro errore ferma i nuovi avvii
// (i lavori aperti si concludono) ed esce alla fine.
func (r *storyRunner) sceneErr(id string, err error) {
	if err == nil || r.failedNow(id) {
		return
	}
	r.halt = append(r.halt, sceneStop{id: id, err: fmt.Errorf("scena %s: %w", id, err)})
}

// halted: nessun nuovo avvio né recupero (errore di una scena o
// interruzione). Uno stato non salvato ferma i soli avvii, in reserveCharge.
func (r *storyRunner) halted(ctx context.Context) bool {
	return len(r.halt) > 0 || ctx.Err() != nil
}

// interrupted registra l'interruzione se resta qualcosa da fare.
func (r *storyRunner) interrupted(ctx context.Context, left bool) {
	if err := ctx.Err(); err != nil && left && len(r.halt) == 0 {
		r.halt = append(r.halt, sceneStop{err: fmt.Errorf("interrotto: %w", err)})
	}
}

// stopErr è l'errore che ha fermato i nuovi avvii, scene in ordine.
func (r *storyRunner) stopErr() error {
	slices.SortStableFunc(r.halt, func(a, b sceneStop) int { return r.rankID(a.id) - r.rankID(b.id) })
	var errs []error
	for _, h := range r.halt {
		errs = append(errs, h.err)
	}
	return errors.Join(errs...)
}

// rankID è la posizione della scena id fra i lavori di questa esecuzione:
// recuperi, poi scene nell'ordine dello storyboard; dopo le altre.
func (r *storyRunner) rankID(id string) int {
	if n, ok := r.rank[id]; ok {
		return n
	}
	return len(r.rank)
}

// recoverAll recupera i job già partiti, di tutte le scene, prima di ogni
// nuovo avvio (il tetto conta così il loro addebito reale): fino a N attese
// insieme, senza nessun POST. Una scena il cui job finisce male sul provider
// è fallita (non si rigenera da sola); ogni altro errore lascia il job, da
// recuperare alla prossima esecuzione, e ferma l'esecuzione.
func (r *storyRunner) recoverAll(ctx context.Context) error {
	pending := r.pending()
	for i, scene := range pending {
		for len(r.open) >= r.parallel {
			r.next()
		}
		if r.halted(ctx) {
			r.interrupted(ctx, i < len(pending))
			break
		}
		r.rank[scene.ID] = len(r.rank)
		r.sceneErr(scene.ID, r.recover(ctx, scene))
	}
	r.drain()
	return r.stopErr()
}

// recover apre il recupero del job già partito della scena, senza un nuovo
// avvio: l'addebito del job entra nel conto prima dell'attesa.
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
	job, slug := provider.Job{ID: j.ID, PollingURL: j.PollingURL}, fmt.Sprintf("%s-r%d", scene.ID, takeNumber(ss))
	man := manifest{Provider: c.Name(), Model: j.Model, Prompt: scene.Prompt}
	w := &sceneWork{scene: scene, ss: ss, take: takeNumber(ss), recover: true, command: storyCommand(scene)}
	w.run = func(ctx context.Context) (manifest, error) {
		res, err := f.FetchVideo(ctx, job, storyTimeout)
		if err != nil {
			return manifest{}, err
		}
		w.fetched = true // letto dal coordinatore dopo l'evento di fine
		files, err := r.saveFiles(slug, res.Media)
		if err != nil {
			return manifest{}, err
		}
		man.done(files, res)
		return man, nil
	}
	r.openWork(ctx, w)
	return nil
}

// recovered registra la fine del recupero: job finito male = scena fallita
// (spesa contata, si rifà solo con --scene); non scaricato = il job resta, si
// recupera alla prossima esecuzione; scaricato ma non salvato = il job resta.
func (r *storyRunner) recovered(w *sceneWork, man manifest, err error) error {
	ss, scene := w.ss, w.scene
	if err != nil {
		ss.Error = err.Error()
		if w.fetched {
			r.save()
			return err
		}
		if errors.Is(err, provider.ErrJobFailed) {
			if k := jobCharge(ss, ss.Job.ID); k >= 0 {
				settle(ss, k, 0, 0) // la spesa resta contata alla stima
			}
			ss.Status, ss.Job = story.Fallito, nil
			r.save()
			return err
		}
		ss.Status = story.Partito
		r.save()
		hint := ""
		if sc := r.s.Scena(scene.ID); sc != nil && sc.Generata() {
			hint = fmt.Sprintf("; se non si scarica più, per abbandonarlo e rifare la scena: giv story run --scene %s --new-job %s", scene.ID, r.path)
		}
		return fmt.Errorf("%w (il job resta: si recupera alla prossima esecuzione%s)", err, hint)
	}
	r.man.Recovered++
	r.thisRun[scene.ID] = "recuperata"
	return r.done(scene, ss, man, w.command)
}

// runScenes genera le scene video e avatar: fino a N lavori aperti, avvii in
// ordine di storyboard fra le scene pronte, uno alla volta. Dopo un errore
// (non un job fallito sul provider), uno stato non salvato o un'interruzione
// non parte nessun nuovo avvio; i lavori aperti si concludono e si registrano.
func (r *storyRunner) runScenes(ctx context.Context) error {
	r.prevJobStart = provider.OnJobStart
	provider.OnJobStart = r.jobStarted
	defer func() { provider.OnJobStart = r.prevJobStart }()
	for {
		for len(r.open) < r.parallel && !r.halted(ctx) {
			scene := r.nextScene()
			if scene == nil {
				break
			}
			r.picked[scene.ID] = true
			if _, ok := r.rank[scene.ID]; !ok {
				r.rank[scene.ID] = len(r.rank)
			}
			r.sceneErr(scene.ID, r.scene(ctx, scene))
		}
		if len(r.open) == 0 {
			break
		}
		r.next()
	}
	r.interrupted(ctx, slices.ContainsFunc(r.order, func(sc *story.Scene) bool { return !r.picked[sc.ID] }))
	return r.stopErr()
}

// nextScene è la prima scena dello storyboard non ancora scelta e pronta: con
// continua_da, la scena precedente è già stata trattata e non ha un lavoro
// aperto (la sua clip è completa). nil se nessuna è pronta adesso.
func (r *storyRunner) nextScene() *story.Scene {
	for _, scene := range r.order {
		if r.picked[scene.ID] {
			continue
		}
		if from := scene.ContinuaDa; from != "" && (r.open[from] != nil || (r.inOrder(from) && !r.picked[from])) {
			continue
		}
		return scene
	}
	return nil
}

// inOrder: la scena id è fra quelle video da trattare in questa esecuzione.
func (r *storyRunner) inOrder(id string) bool {
	return slices.ContainsFunc(r.order, func(sc *story.Scene) bool { return sc.ID == id })
}

// startClip riserva la generazione della clip della scena (stima est, senza
// stima il costo dichiarato) e la affida a un lavoratore con run; torna
// quando il job è avviato (o il lavoro è finito): il prossimo avvio parte
// solo dopo. cleanup (copie private degli ingressi) è suo: lo chiama a
// lavoro finito, o subito se la riserva non c'è.
func (r *storyRunner) startClip(ctx context.Context, scene *story.Scene, ss *story.SceneState, est *float64,
	run func(context.Context) (manifest, error), fail func(error) error, command string, cleanup func()) error {
	ch := story.Charge{Kind: story.ChargeClip, CostEstUSD: est}
	if ch.CostEstUSD == nil {
		ch.CostDeclaredUSD = scene.CostoDichiaratoUSD
	}
	k, err := r.reserve(ss, ch)
	if err != nil {
		cleanup()
		return err
	}
	r.sent[scene.ID] = true // la riserva è nello stato: il tetto non la conta più fra le richieste da fare
	w := &sceneWork{scene: scene, ss: ss, take: takeNumber(ss), k: k, command: command, fail: fail, cleanup: cleanup, run: run}
	r.starting.Store(w)
	defer r.starting.Store(nil)
	r.openWork(ctx, w)
	for !w.started && !w.finished {
		r.next()
	}
	return nil
}
