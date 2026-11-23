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
	"sync/atomic"
	"syscall"
	"time"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/provider"
	"gen-image-video-cli/internal/provider/fal"
	"gen-image-video-cli/internal/provider/gemini"
	"gen-image-video-cli/internal/provider/openaicompat"
	"gen-image-video-cli/internal/speech"
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
	newJob := fl.Bool("new-job", false, "con --scene: abbandona il job già partito della scena e ne avvia uno nuovo")
	newVoice := fl.Bool("new-voice", false, "con --scene: rigenera anche la voce dal testo della scena")
	voicesOnly := fl.Bool("voices", false, "genera solo le voci dai testi, nessun video")
	kitFile := fl.String("kit", "", "kit di marca e personaggi al posto del campo kit")
	parallel := fl.Int("parallel", 0, "scene aperte insieme, da 1 a 4 (di partenza 1 o default.parallelo): avvii uno alla volta in ordine, si sovrappongono attesa e scaricamento")
	keyframes := fl.Bool("keyframes", false, "solo i fotogrammi chiave che mancano, nessun video né voce")
	draft := fl.Bool("bozza", false, "bozze video col profilo default.bozza in clip/bozza/, nessun definitivo né voce")
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
	if *newJob && *only == "" {
		return usageError("--new-job vale solo insieme a --scene <id>")
	}
	if *newVoice && *only == "" {
		return usageError("--new-voice vale solo insieme a --scene <id>")
	}
	if *voicesOnly && (*newKeyframe || *newJob) {
		return usageError("--voices genera solo le voci: niente --new-keyframe né --new-job")
	}
	if err := checkKitFlag(fl, *kitFile); err != nil {
		return err
	}
	mode, err := runMode(*keyframes, *draft, *voicesOnly, *newVoice, *newJob)
	if err != nil {
		return err
	}
	parallelSet := false
	fl.Visit(func(f *flag.Flag) { parallelSet = parallelSet || f.Name == "parallel" })
	if parallelSet && (*parallel < 1 || *parallel > maxParallel) {
		return usageError("--parallel va da 1 a %d, non %d", maxParallel, *parallel)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	o := storyRunOptions{only: *only, newKeyframe: *newKeyframe, newJob: *newJob, newVoice: *newVoice, voicesOnly: *voicesOnly, kit: *kitFile, parallel: *parallel, mode: mode}
	man, err := runStory(ctx, path, o)
	if man != nil && ctx.Err() == nil {
		if s, lerr := story.LoadWithKit(path, *kitFile); lerr == nil {
			contactSheet(s, path)
			if mode != modeFinal {
				approvalSheet(s, path) // fotogrammi e bozze da vedere prima di approvare
			}
			if controlOn(s) || voiceControlOn(s) || judgeControlOn(s) {
				// avvisa e basta: nessuna scena si rigenera; con strict un
				// difetto fa uscire con 1 dopo il manifest
				man.Controllo = controlStory(s, path, "story run", man.voiceChecks)
				if c := man.Controllo; c != nil && s.Strict && c.Result == montage.QCDefect && err == nil {
					err = &cliError{code: exitError, msg: fmt.Sprintf("controllo dei file: difetti (strict nello storyboard): vedi %s; nessuna scena rigenerata", firstOf(c.File, "controllo nel JSON"))}
				}
			}
		}
	}
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
	Started    int        `json:"jobs_started"`     // job video avviati in questa esecuzione
	Recovered  int        `json:"jobs_recovered"`   // job già partiti, scaricati senza nuovo avvio
	Keyframes  int        `json:"keyframes"`        // fotogrammi chiave generati in questa esecuzione
	Voices     int        `json:"voices,omitempty"` // voci dai testi generate in questa esecuzione
	// trascrizioni delle voci generate (controllo.voce) fatte in questa esecuzione
	VoiceChecks int     `json:"voice_checks,omitempty"`
	CostEstUSD  float64 `json:"cost_est_usd"` // stime (o costi dichiarati) delle generazioni delle scene
	CostUSD     float64 `json:"cost_usd"`     // addebito riportato dai provider
	SpentUSD    float64 `json:"spent_usd"`    // per il tetto: addebito dove c'è, altrimenti stima (anche delle scene tolte)
	// richieste a pagamento partite in questa esecuzione (fotogrammi chiave,
	// video, voci e trascrizioni): stime (o costi dichiarati) e spesa contata
	// per il tetto (addebito dove c'è, altrimenti stima); i job recuperati
	// restano dell'esecuzione che li ha avviati
	RunCostEstUSD float64 `json:"run_cost_est_usd,omitempty"`
	RunSpentUSD   float64 `json:"run_spent_usd,omitempty"`
	// spesa nello stato di scene non più nello storyboard: in spent_usd, non fra le scene
	RemovedSpentUSD float64        `json:"removed_spent_usd,omitempty"`
	RemovedScenes   []removedScene `json:"removed_scenes,omitempty"`
	CapUSD          *float64       `json:"cap_usd,omitempty"`
	// Parallel sono le scene tenute aperte insieme (--parallel o
	// default.parallelo), solo se più di una
	Parallel int `json:"parallel,omitempty"`
	// Mode è il gradino dell'esecuzione: keyframes (--keyframes, solo
	// fotogrammi chiave) o bozza (--bozza: stato, file e riprese delle scene
	// sono quelli delle bozze); vuoto per il definitivo
	Mode string `json:"mode,omitempty"`
	// AwaitingApproval sono le scene il cui definitivo aspetta giv story
	// approve (approvazione nello storyboard): né fotogramma né video
	AwaitingApproval []string `json:"awaiting_approval,omitempty"`
	// Judgements sono i giudizi di aderenza (controllo.aderenza) con esito in
	// questa esecuzione
	Judgements int `json:"judgements,omitempty"`
	// Controllo è il controllo dei file a fine giro (controllo.tecnico e controllo.voce)
	Controllo *storyControl `json:"controllo,omitempty"`

	voiceChecks map[string]*storyVoiceCheck // controlli della voce di questa esecuzione, per scena
}

type sceneRun struct {
	ID         string  `json:"id"`
	Tipo       string  `json:"tipo"`
	Status     string  `json:"status,omitempty"`
	File       string  `json:"file,omitempty"`     // clip in uso, o file della scena clip/immagine
	Keyframe   string  `json:"keyframe,omitempty"` // fotogramma chiave in uso
	Takes      int     `json:"takes,omitempty"`    // riprese generate
	ThisRun    string  `json:"this_run,omitempty"` // generata, recuperata, già fatta
	Voice      string  `json:"voice,omitempty"`    // voce in uso: file della scena o voce generata
	VoiceRun   string  `json:"voice_this_run,omitempty"`
	CostEstUSD float64 `json:"cost_est_usd,omitempty"`
	CostUSD    float64 `json:"cost_usd,omitempty"`
	// Judgement è il rapporto del giudizio di aderenza fatto in questa esecuzione
	Judgement *qcJudgeOut `json:"judgement,omitempty"`
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
	abandon string             // --new-job: scena il cui job partito non si recupera e si abbandona alla nuova richiesta
	est     map[string]float64 // stima non arrotondata (o costo dichiarato) della clip, per scena pianificata
	planned map[string]bool
	thisRun map[string]string
	failed  []string // scene fallite sul provider (in questa esecuzione o prima): non si rigenerano da sole
	man     storyRunManifest
	started time.Time // inizio dell'esecuzione: gli addebiti da qui in poi sono suoi

	kitRoot      *os.Root // cartella del kit: i suoi file letti solo qui dentro (nil senza kit)
	newVoice     string   // --new-voice: scena di cui rigenerare la voce
	voicesOnly   bool     // --voices: solo le voci
	voiceOrder   []*story.Scene
	voiceSent    map[string]bool // voci già riservate in questa esecuzione: non più nelle stime da fare
	voiceThisRun map[string]string
	speech       map[string]speech.Synthesizer // client della voce per provider

	// controllo.voce: trascrizioni da fare, stima non arrotondata per scena,
	// trascrizioni già riservate o saltate, esiti di questa esecuzione e client
	checkOrder  []*story.Scene
	checkEst    map[string]float64
	checkDone   map[string]bool
	voiceChecks map[string]*storyVoiceCheck
	transcriber speech.Transcriber

	// coordinatore delle scene (story_parallel.go) e cassa (story_spend.go):
	// lavori aperti insieme al massimo, lavori aperti per scena, eventi dei
	// lavoratori, lavoro in avvio (uno alla volta), scene già scelte, clip
	// già riservate in questa esecuzione, posizione delle scene per l'ordine
	// degli errori, errori che fermano i nuovi avvii, primo stato non salvato
	parallel     int
	open         map[string]*sceneWork
	events       chan workEvent
	starting     atomic.Pointer[sceneWork]
	prevJobStart func(string, provider.Job, provider.VideoRequest)
	picked       map[string]bool
	sent         map[string]bool
	rank         map[string]int
	halt         []sceneStop
	saveErr      error

	// gradino (story_steps.go): modeKeyframes, modeDraft o modeFinal; kind la
	// serie di riprese video che produce (story.TakeFinal o story.TakeDraft)
	// e vc il controllo che ne prepara le richieste (r.sc, o quello della
	// bozza); giudizi di aderenza: stima non arrotondata per scena, giudizi
	// già riservati e rapporti di questa esecuzione
	mode      string
	kind      string
	vc        *storyChecker
	judgeEst  map[string]float64
	judgeSent map[string]bool
	judged    map[string]*qcJudgeOut
}

// storyRunOptions sono i flag di giv story run.
type storyRunOptions struct {
	only                          string
	newKeyframe, newJob, newVoice bool
	voicesOnly                    bool
	kit                           string
	parallel                      int    // --parallel; 0: default.parallelo o 1
	mode                          string // --keyframes, --bozza; vuoto: definitivo
}

func runStory(ctx context.Context, path string, o storyRunOptions) (*storyRunManifest, error) {
	only, newKeyframe, newJob := o.only, o.newKeyframe, o.newJob
	s, err := loadStoryKit(path, o.kit)
	if err != nil {
		return nil, err
	}
	if only != "" {
		target := s.Scena(only)
		if target == nil {
			return nil, usageError("--scene %s: scena inesistente in %s", only, path)
		}
		if o.newVoice && !needsVoice(target) {
			return nil, usageError("--new-voice: la scena %s non ha un testo da far dire senza un file di voce", only)
		}
		if !target.Generata() && !needsVoice(target) {
			return nil, usageError("--scene %s: scena %s, niente da generare", only, target.Tipo)
		}
		if o.voicesOnly && !needsVoice(target) {
			return nil, usageError("--voices --scene %s: la scena non ha un testo da far dire senza un file di voce", only)
		}
		if newKeyframe && target.FotogrammaChiave == nil {
			return nil, usageError("--new-keyframe: la scena %s non ha fotogramma_chiave", only)
		}
		if err := modeScene(o.mode, target); err != nil {
			return nil, err
		}
	}
	if o.mode == modeDraft && s.Default.Bozza == nil {
		return nil, usageError("--bozza: %s non ha default.bozza (provider, modello e risoluzione della bozza)", path)
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
	r := &storyRunner{s: s, st: st, root: root, path: path, sc: newStoryChecker(s), forced: only, started: time.Now(),
		est: map[string]float64{}, planned: map[string]bool{}, thisRun: map[string]string{},
		voicesOnly: o.voicesOnly, voiceSent: map[string]bool{}, voiceThisRun: map[string]string{}, speech: map[string]speech.Synthesizer{},
		checkEst: map[string]float64{}, checkDone: map[string]bool{}, voiceChecks: map[string]*storyVoiceCheck{},
		parallel: parallelism(o.parallel, s), open: map[string]*sceneWork{}, events: make(chan workEvent, 2*maxParallel),
		picked: map[string]bool{}, sent: map[string]bool{}, rank: map[string]int{},
		mode: o.mode, kind: story.TakeFinal, judgeEst: map[string]float64{}, judgeSent: map[string]bool{}, judged: map[string]*qcJudgeOut{}}
	r.sc.state = st
	if o.mode == modeDraft {
		r.kind = story.TakeDraft // prima di --new-job: il job da lasciare è quello della bozza
	}
	if o.newVoice {
		r.newVoice = only
	}
	if s.KitData != nil {
		if r.kitRoot, err = os.OpenRoot(s.KitData.Dir); err != nil {
			return nil, usageError("kit %s: cartella non leggibile: %v", s.KitFile, err)
		}
		defer r.kitRoot.Close()
	}
	if newKeyframe {
		r.renew = only
	}
	if newJob {
		if err := r.abandonJob(only); err != nil {
			return nil, err
		}
	}
	if rs, v := removedSpend(s, st); len(rs) > 0 {
		fmt.Fprintf(os.Stderr, "spesa già registrata nello stato: %s; conta nel tetto\n", removedText(rs, v))
	}
	r.sc.out = filepath.Join(s.Dir, storyClipDir)
	r.vc = r.sc
	if o.mode == modeDraft {
		// bozze: richieste col profilo default.bozza, file in clip/bozza/
		r.vc = r.sc.draftChecker()
		r.vc.out = filepath.Join(s.Dir, filepath.FromSlash(story.DraftDir))
	}
	r.man = storyRunManifest{Command: "story run", Storyboard: path, State: st.Path(), Titolo: s.Titolo, CapUSD: s.TettoCostoUSD, Mode: o.mode}
	if r.parallel > 1 {
		r.man.Parallel = r.parallel
	}
	for i := range s.Scene {
		if sc := &s.Scene[i]; sc.Generata() && (only == "" || sc.ID == only) && !o.voicesOnly && r.inMode(sc) {
			r.order = append(r.order, sc)
		}
	}
	// approvazione: il definitivo di una scena da approvare non parte
	if err := r.gate(); err != nil {
		return nil, err
	}
	if err := r.preflight(); err != nil {
		return nil, err
	}
	// i job già partiti, di tutte le scene, si recuperano prima di ogni nuovo
	// avvio: il tetto conta così il loro addebito reale. Una scena il cui job
	// finisce male sul provider è fallita: non si rigenera da sola (nemmeno con
	// --scene in questa esecuzione) e le altre proseguono. Con --voices e
	// --keyframes nessun job video si tocca; con --bozza si recuperano quelli
	// delle bozze.
	if !o.voicesOnly && o.mode != modeKeyframes {
		if err := r.recoverAll(ctx); err != nil {
			return nil, r.stopped(err)
		}
	}
	// le voci dai testi prima dei video
	if err := r.makeVoices(ctx); err != nil {
		return nil, r.stopped(err)
	}
	// controllo.voce: ogni voce appena generata (o mai controllata) si
	// trascrive prima dei video; avvisa e basta
	r.checkVoices(ctx)
	// le scene video e avatar, in ordine di storyboard: fino a r.parallel
	// lavori aperti, avvii uno alla volta (story_parallel.go)
	if err := r.runScenes(ctx); err != nil {
		return nil, r.stopped(err)
	}
	// controllo.aderenza: le riprese nuove (o mai giudicate) si giudicano a
	// fine giro, una richiesta per ripresa; avvisa e basta
	r.judgeScenes(ctx)
	r.summary()
	r.man.voiceChecks = r.voiceChecks
	return &r.man, r.stopped(nil)
}

// failedNow: la scena è fallita sul provider (job finito male o senza video):
// entra nell'elenco di fine esecuzione invece di fermarla.
func (r *storyRunner) failedNow(id string) bool {
	if t := r.track(id); t == nil || t.Status != story.Fallito {
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
	// in ordine di lavoro (recuperi, poi scene dello storyboard), anche se in
	// parallelo sono finite in un altro ordine
	slices.SortStableFunc(r.failed, func(a, b string) int { return r.rankID(a) - r.rankID(b) })
	for _, id := range r.failed {
		redo := fmt.Sprintf("per rifarla: giv story run %s--scene %s %s", r.modeFlag(), id, r.path)
		if sc := r.s.Scena(id); sc == nil || !sc.Generata() {
			redo = "non è più una scena video dello storyboard"
		}
		errs = append(errs, fmt.Errorf("scena %s fallita sul provider (spesa contata): %s; %s", id, r.track(id).Error, redo))
	}
	return errors.Join(append(errs, err)...)
}

// preflight controlla tutto prima del primo avvio: job da recuperare, richieste
// rimaste senza esito, scene da generare (capacità, preset, stime sul catalogo
// letto adesso), fotogrammi di continua_da e tetto di spesa. Ogni rifiuto è
// codice 2 e nessun avvio.
func (r *storyRunner) preflight() error {
	if err := r.voicePreflight(); err != nil {
		return err
	}
	rejects, err := r.voiceCheckPreflight()
	if err != nil {
		return err
	}
	var launch []*story.Scene
	for _, scene := range r.pending() {
		if err := r.checkJob(r.jobOf(scene.ID)); err != nil {
			rejects = append(rejects, fmt.Errorf("scena %s: %w", scene.ID, err))
		}
	}
	if r.voicesOnly {
		// i job in corso non si recuperano, ma con un job non valido nello
		// stato non parte nessuna voce
		if len(rejects) > 0 {
			return storyError(r.st.Path(), errors.Join(rejects...))
		}
		return r.checkCap()
	}
	if id := r.abandon; id != "" {
		// il job lasciato con --new-job non si recupera ma si valida come gli
		// altri: con un job non valido nello stato non parte nessun avvio
		if err := r.checkJob(r.track(id).Job); err != nil {
			rejects = append(rejects, fmt.Errorf("scena %s: %w", id, err))
		}
	}
	for _, scene := range r.order {
		if r.mode != modeKeyframes && r.jobOf(scene.ID) != nil {
			continue
		}
		if r.uncertain(scene) && scene.ID != r.forced {
			rejects = append(rejects, fmt.Errorf("scena %s: l'ultima richiesta di generazione è rimasta senza esito o senza file salvato e può essere stata addebitata (contata nella spesa): controlla sul provider, poi rifalla con %s--scene %s", scene.ID, r.modeFlag(), scene.ID))
			continue
		}
		// con --keyframes anche le scene col fotogramma già fatto: uno
		// registrato che manca si rifà solo con --new-keyframe
		if r.toLaunch(scene) || r.mode == modeKeyframes {
			if err := r.staleKeyframe(scene); err != nil {
				rejects = append(rejects, err)
				continue
			}
		}
		if r.toLaunch(scene) {
			launch = append(launch, scene)
		}
	}
	if len(rejects) > 0 {
		return storyError(r.st.Path(), errors.Join(rejects...))
	}
	if err := r.plan(launch, true); err != nil {
		return err
	}
	if err := r.judgePlan(); err != nil {
		return err
	}
	return r.checkCap() // anche con le sole voci da fare
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
		if r.jobOf(scene.ID) != nil {
			if !scene.Generata() {
				scene = &story.Scene{ID: scene.ID, Tipo: story.Video}
			}
			out = append(out, scene)
		}
	}
	var rest []string
	for id := range r.st.Scenes {
		if !seen[id] && r.jobOf(id) != nil {
			rest = append(rest, id)
		}
	}
	slices.Sort(rest)
	for _, id := range rest {
		out = append(out, &story.Scene{ID: id, Tipo: story.Video})
	}
	return out
}

// jobOf è il job partito della scena, da recuperare; nil anche per la scena di
// --new-job, il cui job non si recupera.
func (r *storyRunner) jobOf(id string) *story.Job {
	t := r.track(id)
	if t == nil || id == r.abandon {
		return nil
	}
	return t.Job
}

// abandonJob prepara --new-job sulla scena id: il suo job partito non si
// recupera e si abbandona solo con la riserva della nuova richiesta, nella
// stessa scrittura dello stato (reserve); se nessuna richiesta parte, lo stato
// non cambia. L'addebito del job resta contato nella spesa.
func (r *storyRunner) abandonJob(id string) error {
	ss, t := r.st.Scenes[id], r.track(id)
	if t == nil || t.Job == nil {
		return usageError("--new-job: la scena %s non ha un job partito nello stato %s", id, r.st.Path())
	}
	j := t.Job
	if k := jobCharge(ss, r.chargeKind(id), j.ID); k < 0 {
		// come in recover: ogni job partito conta nella spesa
		ss.Charges = append(ss.Charges, r.jobChargeOf(r.s.Scena(id), j.ID))
	} else if ss.Charges[k].Status != "" {
		settle(ss, k, 0, 0) // la riserva del job lasciato resta contata alla stima
	}
	r.abandon = id
	fmt.Fprintf(os.Stderr, "scena %s: --new-job: il job %s (%s) non si recupera e resta contato nella spesa; nuova richiesta\n", id, j.ID, j.Provider)
	return nil
}

// checkJob controlla, senza richieste, un job del file di stato: provider con
// job video e indirizzo di attesa sull'host del provider (con --voices senza
// chiave video: jobChecker).
func (r *storyRunner) checkJob(j *story.Job) error {
	if err := checkProvider(j.Provider); err != nil {
		return err
	}
	if _, ok := defaultVideoModels[j.Provider]; !ok {
		return usageError("job %s: %s non ha job video", j.ID, j.Provider)
	}
	var p provider.Provider
	if r.voicesOnly {
		var err error
		if p, err = jobChecker(j.Provider); err != nil {
			return usageError("job %s: %v", j.ID, err)
		}
	} else {
		c, err := r.sc.client(j.Provider)
		if err != nil {
			return err
		}
		p = c.Provider
	}
	f, ok := p.(provider.JobFetcher)
	if !ok {
		return usageError("job %s: %s non sa recuperare i job video", j.ID, j.Provider)
	}
	if err := f.CheckJob(provider.Job{ID: j.ID, PollingURL: j.PollingURL}); err != nil {
		return usageError("job %s: %v", j.ID, err)
	}
	return nil
}

// jobChecker è il provider dei job video senza chiave, con le stesse origini
// di newProvider, solo per CheckJob: con --voices i job in corso si validano
// (nessuna richiesta, nessun recupero) e la chiave video non serve.
func jobChecker(name string) (provider.Provider, error) {
	switch name {
	case "gemini":
		c := gemini.New("")
		c.Base = baseURLs["gemini"]
		return c, nil
	case "fal":
		return fal.New("", falConfig)
	case "xai":
		return openaicompat.New("xai", baseURLs["xai"], "", openaicompat.Options{B64Param: true, Grok: true}), nil
	case "openrouter":
		return openaicompat.New("openrouter", baseURLs["openrouter"], "", openaicompat.Options{Router: true}), nil
	}
	return nil, fmt.Errorf("%s non ha job video", name)
}

// plan prepara le scene da generare e controlla il tetto: spesa già fatta più
// stime delle scene da generare (all) o della sola scena (dopo un job fallito).
func (r *storyRunner) plan(scenes []*story.Scene, all bool) error {
	if len(scenes) == 0 {
		return nil
	}
	plan := r.planScenes
	if r.mode == modeDraft {
		plan = r.planDrafts // story_steps.go
	}
	missing, err := plan(scenes)
	if err != nil {
		return err
	}
	cap := r.s.TettoCostoUSD
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
		return r.checkCap()
	}
	return nil
}

// planScenes prepara le scene del definitivo (o i soli fotogrammi chiave, con
// --keyframes) e restituisce le generazioni senza stima.
func (r *storyRunner) planScenes(scenes []*story.Scene) ([]string, error) {
	ids := map[string]bool{}
	for _, scene := range scenes {
		ids[scene.ID] = true
		r.sc.names[scene.ID] = fmt.Sprintf("%s-r%d", scene.ID, takeNumber(r.st.Scenes[scene.ID]))
	}
	out, err := r.sc.plan(r.path, ids)
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, c := range out.Scenes {
		r.planned[c.ID] = true
		scene := r.s.Scena(c.ID)
		if r.mode == modeKeyframes {
			// nessun video: conta solo il fotogramma chiave
			if r.needKeyframe(scene) && scene.FotogrammaChiave.CostoDichiaratoUSD == nil {
				missing = append(missing, c.ID+" (fotogramma_chiave)")
			}
			continue
		}
		switch {
		case c.CostEstUSD != nil:
			r.est[c.ID] = r.sc.exactEstimate(c)
		case c.CostDeclaredUSD != nil:
			r.est[c.ID] = *c.CostDeclaredUSD
		default:
			missing = append(missing, c.ID)
		}
		if job := r.sc.clips[c.ID]; job != nil {
			// la chiave di una scena avatar serve solo qui: senza, nessun avvio
			// e nessuna scrittura dello stato, come per le scene video
			if _, err := r.sc.client(job.provider); err != nil {
				return nil, fmt.Errorf("scena %s: %w", c.ID, err)
			}
		}
		if r.needKeyframe(scene) && scene.FotogrammaChiave.CostoDichiaratoUSD == nil {
			missing = append(missing, c.ID+" (fotogramma_chiave)")
		}
		if scene.ContinuaDa != "" {
			if err := storyFrames.Check(); err != nil {
				return nil, &cliError{code: exitError, msg: fmt.Sprintf("scena %s: continua_da: %v", c.ID, err)}
			}
			if err := r.continuaReady(scene, ids); err != nil {
				return nil, usageError("scena %s: %v", c.ID, err)
			}
		}
	}
	return missing, nil
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
	t := r.version(from) // stessa versione: bozza della bozza, definitivo del definitivo
	if t != nil && (t.Clip != nil || t.Job != nil) {
		return nil
	}
	if t != nil && t.Status == story.Fallito {
		return fmt.Errorf("continua_da %s: la scena %s è fallita sul provider: rifalla con %s--scene %s", from.ID, from.ID, r.modeFlag(), from.ID)
	}
	return fmt.Errorf("continua_da %s: la scena %s non ha ancora una clip (lancia giv story run %ssenza --scene)", from.ID, from.ID, r.modeFlag())
}

// toLaunch: la scena va generata (non ha un job da recuperare e non ha una clip,
// oppure è quella di --scene).
func (r *storyRunner) toLaunch(scene *story.Scene) bool {
	if r.mode == modeKeyframes {
		return r.needKeyframe(scene) // incerto rifiutato in preflight
	}
	if r.jobOf(scene.ID) != nil {
		return false
	}
	if scene.ID == r.forced {
		return true
	}
	t := r.track(scene.ID)
	if t != nil && (t.Status == story.Incerto || t.Status == story.Fallito) {
		return false // solo con --scene: incerta rifiutata in preflight, fallita elencata a fine esecuzione
	}
	return t == nil || t.Clip == nil || !r.exists(t.Clip.File)
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

// scene genera la scena se serve: torna quando il suo job è avviato (attesa e
// scaricamento li fa un lavoratore). Il suo job, se ce n'era uno, è già stato
// recuperato: la ripresa recuperata vale come nuova (anche con --scene).
func (r *storyRunner) scene(ctx context.Context, scene *story.Scene) error {
	ss := r.st.Scenes[scene.ID]
	if t := r.thisRun[scene.ID]; t == "recuperata" || t == "fallita" {
		r.sent[scene.ID] = true // niente da avviare: fuori dal conto delle richieste da fare
		if scene.ID == r.renew {
			fmt.Fprintf(os.Stderr, "scena %s: c'era un job già partito (%s in questa esecuzione): fotogramma chiave non rigenerato; per rifarla con uno nuovo: --scene %s --new-keyframe\n", scene.ID, t, scene.ID)
		}
		return nil
	}
	if !r.toLaunch(scene) {
		if r.mode == modeKeyframes {
			if scene.ID == r.forced {
				fmt.Fprintf(os.Stderr, "scena %s: fotogramma chiave già fatto, %s (per rigenerarlo: --new-keyframe)\n", scene.ID, ss.Keyframe.File)
			}
			return nil
		}
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
	switch t := r.track(scene.ID); {
	case r.uncertain(scene):
		fmt.Fprintf(os.Stderr, "scena %s: la richiesta precedente è rimasta senza esito (resta contata nella spesa): nuova richiesta\n", scene.ID)
	case r.mode == modeKeyframes:
	case t != nil && t.Status == story.Fallito:
		fmt.Fprintf(os.Stderr, "scena %s: il job precedente è fallito sul provider (resta contato nella spesa): nuova richiesta\n", scene.ID)
	case t != nil && t.Clip != nil && scene.ID != r.forced:
		fmt.Fprintf(os.Stderr, "scena %s: clip %s mancante, la scena si rigenera\n", scene.ID, t.Clip.File)
	}
	if err := r.checkCap(); err != nil {
		return err
	}
	return r.launch(ctx, scene)
}

// launch genera fotogramma chiave (se serve) e avvia la clip della scena.
// Prima di ogni richiesta a pagamento la cassa ricontrolla il tetto con la
// spesa reale e registra la generazione nel file di stato (stato incerto): se
// non si salva, la richiesta non parte. Poi lo stato si salva a ogni passo:
// all'avvio del job (partito, con l'id), alla fine (fatto) o all'errore.
func (r *storyRunner) launch(ctx context.Context, scene *story.Scene) error {
	ss := r.st.Scene(scene.ID)
	job := r.vc.jobs[scene.ID]
	tr := r.trackW(scene.ID, ss)
	fail := func(err error) error {
		tr.Error = err.Error()
		if tr.Status == "" {
			tr.Status = idle(tr) // niente inviato: la scena resta da fare
		}
		r.save()
		return err
	}
	if scene.Tipo == story.Avatar {
		return r.launchAvatar(ctx, scene, ss, fail)
	}
	// copie private degli ingressi: il provider legge solo file letti qui
	// dentro la cartella dello storyboard e controllati
	work, err := montage.TempDir("giv-story-") // rifiuta una TMPDIR condivisa senza sticky bit
	if err != nil {
		return err
	}
	var tmp tempInputs // ingressi da URL del fotogramma chiave, scaricati in file privati
	cleanup := func() {
		tmp.cleanup()
		os.RemoveAll(work)
	}
	owned := true // dopo startClip le copie sono del lavoro della clip
	defer func() {
		if owned {
			cleanup()
		}
	}()

	req := job.req
	req.Refs = slices.Clone(job.req.Refs)
	req.Keyframes = slices.Clone(job.req.Keyframes)
	shown := newManifestInputs(req) // come nello storyboard
	if err := r.staleKeyframe(scene); err != nil {
		return err // nessuna richiesta: il fotogramma si rigenera solo con --new-keyframe
	}
	if r.mode == modeKeyframes {
		return r.makeKeyframe(ctx, scene, ss, work, &tmp, fail) // nient'altro: nessun video
	}
	// fotogrammi intermedi da URL scaricati e controllati prima del fotogramma
	// chiave a pagamento: uno non valido ferma la scena senza spesa
	remote := provider.VideoRequest{LastFrame: req.LastFrame, Refs: req.Refs, Keyframes: req.Keyframes}
	if err := tmp.resolveURLs(&remote, "--keyframe"); err != nil {
		return fail(err)
	}
	req.Keyframes = remote.Keyframes
	switch {
	case scene.FotogrammaChiave != nil:
		if r.needKeyframe(scene) {
			if err := r.makeKeyframe(ctx, scene, ss, work, &tmp, fail); err != nil {
				return err
			}
		} else if scene.ID == r.forced {
			fmt.Fprintf(os.Stderr, "scena %s: riuso il fotogramma chiave %s (per rigenerarlo: --new-keyframe)\n", scene.ID, ss.Keyframe.File)
		}
		p, err := r.privateCopy(ss.Keyframe.File, work)
		if err != nil {
			return fail(fmt.Errorf("fotogramma_chiave: %w", err))
		}
		req.Image = p
		shown = newManifestInputs(provider.VideoRequest{Image: r.s.Path(ss.Keyframe.File), LastFrame: req.LastFrame, Refs: req.Refs, Keyframes: job.req.Keyframes})
	case scene.ContinuaDa != "":
		p, err := r.lastFrame(ctx, scene, work)
		if err != nil {
			return fail(err)
		}
		req.Image = p
		shown = newManifestInputs(provider.VideoRequest{Image: "ultimo fotogramma di " + scene.ContinuaDa, LastFrame: req.LastFrame, Refs: req.Refs, Keyframes: job.req.Keyframes})
	}
	for _, in := range videoInputs(&req) {
		if *in.path == "" || isURL(*in.path) || strings.HasPrefix(*in.path, work+string(filepath.Separator)) || slices.Contains(tmp, *in.path) {
			continue // URL e copie già private
		}
		p, err := r.privateLocal(*in.path, work)
		if err != nil {
			return fail(fmt.Errorf("%s: %w", in.flag, err))
		}
		*in.path = p
	}
	if err := checkVideoInputs(&req); err != nil {
		return fail(err)
	}
	job.req, job.inputs = req, shown
	dir := r.clipDir(scene.ID)
	job.save = func(slug string, res *provider.Result) ([]output.FileInfo, error) {
		return r.saveFiles(dir, slug, res.Media) // solo file nuovi in clip/ (o clip/bozza/), nessuna scrittura dello stato
	}
	owned = false
	return r.startClip(ctx, scene, ss, r.clipEst(job), job.run, fail, r.clipCommand(), cleanup)
}

// makeKeyframe genera il fotogramma chiave della scena, dalla riserva della
// cassa: la richiesta è quella del profilo del definitivo (r.sc), anche per
// la bozza, e il fotogramma vale per entrambe. Con --keyframes è tutto ciò
// che la scena chiede.
func (r *storyRunner) makeKeyframe(ctx context.Context, scene *story.Scene, ss *story.SceneState, work string, tmp *tempInputs, fail func(error) error) error {
	img := r.sc.images[scene.ID]
	inputs, err := r.privateInputs(img.req.Inputs, work, tmp)
	if err != nil {
		return fail(fmt.Errorf("fotogramma_chiave: %w", err))
	}
	ch := story.Charge{Kind: story.ChargeKeyframe, CostDeclaredUSD: scene.FotogrammaChiave.CostoDichiaratoUSD}
	after := r.afterKeyframe(ss)
	k, err := r.reserve(ss, ch, &ss.Track) // un fotogramma senza esito rende incerta la scena, come nella 0.12
	if err != nil {
		return err
	}
	if r.mode == modeKeyframes {
		r.sent[scene.ID] = true // riservato: il tetto non lo conta più fra le richieste da fare
	}
	if err := r.keyframe(ctx, scene, ss, k, img, inputs, after); err != nil {
		return fail(fmt.Errorf("fotogramma_chiave: %w", err))
	}
	if r.mode == modeKeyframes {
		r.thisRun[scene.ID] = "fotogramma"
	}
	return nil
}

// reserve è la riserva della cassa per il fotogramma chiave o la clip della
// scena: tetto ricontrollato con la spesa fatta fin qui e generazione ch
// registrata come incerta nel file di stato, prima di inviarla; se lo stato
// non si salva la richiesta non parte. Restituisce l'indice della riserva.
func (r *storyRunner) reserve(ss *story.SceneState, ch story.Charge, tr *story.Track) (int, error) {
	run := &ss.Track // serie di riprese video di questa esecuzione: definitivo o bozza
	if r.kind == story.TakeDraft {
		run = ss.TrackOf(story.TakeDraft)
	}
	return r.reserveCharge(ss, ch, func() func() {
		oldTr, oldRun := *tr, *run
		if j := run.Job; j != nil && ss == r.st.Scenes[r.abandon] {
			// --new-job: il job lasciato passa fra i precedenti con l'ultimo errore
			run.Previous = append(run.Previous, story.Take{Kind: story.TakeJob, JobID: j.ID, Error: run.Error, Done: time.Now()})
			run.Job = nil
		}
		tr.Status = story.Incerto
		if tr == run && r.mode != modeKeyframes {
			tr.Error = "" // l'errore del definitivo resta se la richiesta è d'altro (fotogramma della bozza, --keyframes)
		}
		return func() { *tr, *run = oldTr, oldRun }
	})
}

// keyframe genera il fotogramma chiave della scena (una sola richiesta, senza
// ritentativi) e lo registra: le riprese successive lo riusano finché il file
// c'è; con --new-keyframe il precedente passa fra le riprese precedenti. La
// generazione è già registrata come ultima di ss.Charges: se la risposta
// arriva (addebitata) ma il file non si salva, o l'esito non si sa, la riserva
// resta e la scena è incerta (si rifà solo con --scene). Se lo stato col
// fotogramma registrato non si salva, la scena si ferma prima del video.
func (r *storyRunner) keyframe(ctx context.Context, scene *story.Scene, ss *story.SceneState, k int, img *imageJob, inputs []string, after string) error {
	received := false
	img.req.Inputs = inputs
	img.save = func(slug string, res *provider.Result) ([]output.FileInfo, error) {
		received = true
		settle(ss, k, res.CostUSD, 0) // l'addebito al posto della riserva, prima del video
		return r.saveFiles(storyClipDir, slug, res.Media)
	}
	man, err := img.run(httpx.WithoutRetry(ctx))
	if err != nil {
		switch {
		case received:
			// arrivata e addebitata senza file salvato: resta incerta
		case uncertainStart(err):
			ss.MarkUncertain(k) // senza esito: forse addebitata, resta riservata
		default:
			// rifiutata (4xx) o mai partita: niente addebito, la scena resta da fare
			releaseCharge(ss, k)
			ss.Status = keyframeIdle(ss, after)
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
	ss.Status = keyframeIdle(ss, after) // fatto se la clip precedente resta in uso
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
	// se non si salva il video non parte
	if err := r.saveState(); err != nil {
		return fmt.Errorf("fotogramma chiave %s generato (addebito %.4f $) ma stato non salvato, nessuna richiesta video inviata: %w",
			rel, ss.Charges[k].CostUSD, err)
	}
	return nil
}

// idle è lo stato di una serie di riprese video senza richieste in corso:
// fatta se ha una clip in uso, altrimenti da fare.
func idle(t *story.Track) string {
	if t.Clip != nil {
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
			out[i], err = r.privateLocal(in, work)
		}
		if err != nil {
			return nil, fmt.Errorf("--input %s: %w", shownInput(in), err)
		}
	}
	return out, nil
}

// privateLocal è privateCopy di un ingresso locale come lo dà Path: un file
// del kit si legge solo dentro la cartella del kit, ogni altro solo dentro
// quella dello storyboard.
func (r *storyRunner) privateLocal(path, work string) (string, error) {
	if rel, ok := r.s.KitRel(path); ok && r.kitRoot != nil {
		return r.privateCopyFrom(r.kitRoot, "del kit", rel, work)
	}
	rel, err := r.s.Rel(path)
	if err != nil {
		return "", err
	}
	return r.privateCopy(rel, work)
}

// privateCopy legge rel (relativo alla cartella dello storyboard) solo dentro
// la cartella, anche seguendo i link, controlla che sia un'immagine entro i
// limiti e ne scrive una copia privata in work: il provider legge quella.
func (r *storyRunner) privateCopy(rel, work string) (string, error) {
	return r.privateCopyFrom(r.root, "dello storyboard", rel, work)
}

// privateCopyFrom è privateCopy da root (la cartella dello storyboard o del kit, in where).
func (r *storyRunner) privateCopyFrom(root *os.Root, where, rel, work string) (string, error) {
	f, err := root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("%s: file non leggibile dentro la cartella %s: %w", rel, where, err)
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
func (r *storyRunner) saveFiles(sub, slug string, media []provider.Media) ([]output.FileInfo, error) {
	sub = filepath.FromSlash(sub)
	if err := r.root.MkdirAll(sub, 0o755); err != nil {
		return nil, err
	}
	dir, err := r.root.OpenRoot(sub)
	if err != nil {
		return nil, fmt.Errorf("cartella %s: %w", sub, err)
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
			return nil, fmt.Errorf("%s: %w", filepath.Join(sub, name), err)
		}
		_, err = f.Write(m.Data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(sub, name), err)
		}
		files = append(files, output.FileInfo{Path: filepath.Join(r.s.Dir, sub, name), Mime: m.Mime, Bytes: len(m.Data)})
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
		t := r.version(from) // stessa versione: bozza o definitivo
		if t == nil || t.Clip == nil {
			return "", fmt.Errorf("continua_da %s: %w", from.ID, story.ErrNotDone)
		}
		rel = t.Clip.File
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
	tr, kind := r.trackW(scene.ID, ss), r.chargeKind(scene.ID)
	if file == "" {
		tr.Error = "nessun video nella risposta"
		tr.Status, tr.Job = story.Fallito, nil // job finito, forse addebitato: si rifà solo con --scene
		r.settleJob(ss, kind, man, false)
		r.save()
		return errors.New(tr.Error)
	}
	rel, err := r.s.Rel(file)
	if err != nil {
		return err
	}
	if tr.Clip != nil {
		tr.Previous = append(tr.Previous, *tr.Clip)
	}
	tr.Clip = &story.Take{File: rel, JobID: man.JobID, Done: time.Now()}
	tr.Status, tr.Job, tr.Error = story.Fatto, nil, ""
	r.settleJob(ss, kind, man, true)
	r.save()
	return nil
}

// settleJob registra la generazione del job finito di man al posto della sua
// riserva: con video, l'addebito riportato o, senza, la stima dai consumi
// (gemini, contatori validati) se non è più bassa; senza video resta alla
// stima (forse addebitato).
func (r *storyRunner) settleJob(ss *story.SceneState, kind string, man manifest, video bool) {
	for i := range ss.Charges {
		if c := ss.Charges[i]; c.Kind == kind && c.JobID != "" && c.JobID == man.JobID {
			if video {
				settle(ss, i, man.CostUSD, man.usageEstUSD)
			} else {
				settle(ss, i, 0, 0)
			}
		}
	}
}

// summary compila il manifest finale dallo stato: per scena stima e addebito.
func (r *storyRunner) summary() {
	for i := range r.s.Scene {
		scene := &r.s.Scene[i]
		row := sceneRun{ID: scene.ID, Tipo: scene.Tipo, VoiceRun: r.voiceThisRun[scene.ID]}
		ss := r.st.Scenes[scene.ID]
		if ss != nil && ss.Voice != nil && needsVoice(scene) {
			row.Voice = ss.Voice.File
		}
		if !scene.Generata() {
			row.File = scene.File
			if ss != nil && needsVoice(scene) {
				// la voce dal testo: la sua spesa è della scena
				var est, cost float64
				for _, c := range ss.Charges {
					if !c.OfVoice() {
						continue
					}
					if c.CostEstUSD != nil {
						est += *c.CostEstUSD
					}
					cost += c.CostUSD
				}
				row.CostEstUSD, row.CostUSD = round4(est), round4(cost)
				r.man.CostEstUSD += est
				r.man.CostUSD += cost
			}
			r.man.Scenes = append(r.man.Scenes, row)
			continue
		}
		row.ThisRun = r.thisRun[scene.ID]
		row.Judgement = r.judged[scene.ID]
		if t := r.track(scene.ID); t != nil {
			// con --bozza stato, file e riprese delle bozze
			row.Status = t.Status
			if t.Clip != nil {
				row.File = t.Clip.File
				row.Takes = r.takeNumber(scene.ID, ss) - 1
			}
		}
		if ss != nil {
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
	var runEst, runSpent float64
	for _, ss := range r.st.Scenes {
		for _, c := range ss.Charges {
			if c.At.Before(r.started) {
				continue
			}
			switch {
			case c.CostEstUSD != nil:
				runEst += *c.CostEstUSD
			case c.CostDeclaredUSD != nil:
				runEst += *c.CostDeclaredUSD
			}
			runSpent += c.Spent()
		}
	}
	r.man.RunCostEstUSD, r.man.RunSpentUSD = round4(runEst), round4(runSpent)
	var removed float64
	r.man.RemovedScenes, removed = removedSpend(r.s, r.st)
	r.man.RemovedSpentUSD = round4(removed)
}
