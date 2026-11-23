package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"gen-image-video-cli/internal/judge"
	"gen-image-video-cli/internal/story"
)

// Gradini di giv story run (0.13): --keyframes fa solo i fotogrammi chiave
// che mancano, --bozza le bozze video col profilo default.bozza (file in
// clip/bozza/, stato in SceneState.Draft, spesa di tipo story.ChargeDraft), e
// senza nessuno dei due il definitivo, che con approvazione nello
// storyboard parte solo per le scene approvate (definitiveGate). Il
// fotogramma chiave è condiviso: lo stesso per bozza e definitivo. Con
// controllo.aderenza le riprese nuove si giudicano a fine giro. Tutto passa
// dallo stesso coordinatore e dalla stessa cassa.

const (
	modeFinal     = ""
	modeKeyframes = "keyframes"
	modeDraft     = "bozza"
)

// runMode è il gradino scelto coi flag: --keyframes e --bozza non vanno
// insieme, né con le voci; --keyframes non ha job da abbandonare.
func runMode(keyframes, draft, voicesOnly, newVoice, newJob bool) (string, error) {
	var mode, flag string
	switch {
	case keyframes && draft:
		return "", usageError("--keyframes e --bozza insieme: scegline uno")
	case keyframes:
		mode, flag = modeKeyframes, "--keyframes"
	case draft:
		mode, flag = modeDraft, "--bozza"
	default:
		return modeFinal, nil
	}
	if voicesOnly || newVoice {
		return "", usageError("%s: niente voci (--voices, --new-voice): le voci si fanno senza %s", flag, flag)
	}
	if keyframes && newJob {
		return "", usageError("--keyframes genera solo i fotogrammi chiave: niente --new-job")
	}
	return mode, nil
}

// modeScene controlla la scena di --scene per il gradino.
func modeScene(mode string, sc *story.Scene) error {
	switch {
	case mode == modeDraft && sc.Tipo != story.Video:
		return usageError("--bozza --scene %s: le bozze sono solo delle scene video", sc.ID)
	case mode == modeKeyframes && (sc.Tipo != story.Video || sc.FotogrammaChiave == nil):
		return usageError("--keyframes --scene %s: la scena non è video con fotogramma_chiave", sc.ID)
	}
	return nil
}

// inMode: la scena generata è da trattare in questo gradino.
func (r *storyRunner) inMode(sc *story.Scene) bool {
	switch r.mode {
	case modeDraft:
		return sc.Tipo == story.Video
	case modeKeyframes:
		return sc.Tipo == story.Video && sc.FotogrammaChiave != nil
	}
	return true
}

// modeFlag è il flag del gradino nei comandi suggeriti, con lo spazio dopo.
func (r *storyRunner) modeFlag() string {
	if r.mode == modeFinal {
		return ""
	}
	return "--" + r.mode + " "
}

// drafts: le riprese video di questa esecuzione sono bozze (--bozza).
func (r *storyRunner) drafts(string) bool { return r.kind == story.TakeDraft }

// track è la serie di riprese video della scena id che l'esecuzione produce:
// il definitivo, o la bozza con --bozza (nil se non ce n'è ancora una); nil
// senza stato della scena.
func (r *storyRunner) track(id string) *story.Track {
	ss := r.st.Scenes[id]
	switch {
	case ss == nil:
		return nil
	case r.drafts(id):
		return ss.Draft
	}
	return &ss.Track
}

// trackW è track da scrivere: la serie delle bozze si crea se manca.
func (r *storyRunner) trackW(id string, ss *story.SceneState) *story.Track {
	if r.drafts(id) {
		return ss.TrackOf(story.TakeDraft)
	}
	return &ss.Track
}

// version è la serie da cui parte continua_da: della stessa versione (bozza o
// definitivo); un'avatar ha solo il definitivo.
func (r *storyRunner) version(from *story.Scene) *story.Track {
	if ss := r.st.Scenes[from.ID]; ss != nil && from.Tipo == story.Avatar {
		return &ss.Track
	}
	return r.track(from.ID)
}

// chargeKind è il tipo di generazione dei video della scena id.
func (r *storyRunner) chargeKind(id string) string {
	if r.drafts(id) {
		return story.ChargeDraft
	}
	return story.ChargeClip
}

// jobChargeOf è la generazione di un job partito che nello stato non ha la
// sua: conta nella spesa (il costo dichiarato della scena vale solo per il
// definitivo).
func (r *storyRunner) jobChargeOf(scene *story.Scene, jobID string) story.Charge {
	ch := story.Charge{Kind: r.chargeKind(scene.ID), JobID: jobID, At: time.Now()}
	if !r.drafts(scene.ID) {
		ch.CostDeclaredUSD = scene.CostoDichiaratoUSD
	}
	return ch
}

// clipDir è la cartella dei video della scena id, relativa allo storyboard.
func (r *storyRunner) clipDir(id string) string {
	if r.drafts(id) {
		return story.DraftDir
	}
	return storyClipDir
}

// clipCommand è il comando dei video di questa esecuzione nel registro.
func (r *storyRunner) clipCommand() string {
	if r.kind == story.TakeDraft {
		return "story bozza"
	}
	return "story video"
}

// clipEst è la stima non arrotondata da riservare per il video del job.
func (r *storyRunner) clipEst(job *videoJob) *float64 {
	if p := job.plan; r.kind == story.TakeDraft && p.costExact == nil {
		return p.CostEstUSD
	}
	return job.plan.costExact
}

// takeNumber è il numero della prossima ripresa video della scena id.
func (r *storyRunner) takeNumber(id string, ss *story.SceneState) int {
	if r.drafts(id) {
		return draftTakes(ss) + 1
	}
	return takeNumber(ss)
}

// slug è il nome (senza data ed estensione) del prossimo video della scena.
func (r *storyRunner) slug(id string, ss *story.SceneState) string {
	if r.drafts(id) {
		return draftName(id, ss)
	}
	return fmt.Sprintf("%s-r%d", id, takeNumber(ss))
}

// uncertain: l'ultima richiesta a pagamento della scena per questo gradino è
// rimasta senza esito (si rifà solo con --scene). Un fotogramma chiave senza
// esito rende incerta la scena (stato del definitivo, come nella 0.12) e
// ferma ogni gradino che lo chiede; la bozza ha il suo stato.
func (r *storyRunner) uncertain(scene *story.Scene) bool {
	ss := r.st.Scenes[scene.ID]
	if ss == nil {
		return false
	}
	keyframe := r.needKeyframe(scene) && ss.Status == story.Incerto
	switch r.mode {
	case modeKeyframes:
		return keyframe
	case modeDraft:
		return keyframe || (ss.Draft != nil && ss.Draft.Status == story.Incerto)
	}
	return ss.Status == story.Incerto
}

// afterKeyframe è lo stato del definitivo dopo un fotogramma chiave finito
// senza esito incerto, vuoto per idle: nel definitivo che prosegue col video
// segue la clip (come nella 0.12); con --keyframes, con --bozza e con
// l'approvazione (il fotogramma nuovo la rende vecchia: nessun video) il
// definitivo resta com'era (fallito, partito, incerto per la sua clip), salvo
// l'incerto del solo fotogramma, ora rifatto con --scene. Una clip senza esito
// resta tale anche dopo: la sua riserva lo dice (finalUncertain), anche dopo
// un arresto.
func (r *storyRunner) afterKeyframe(scene *story.Scene, ss *story.SceneState) string {
	if r.mode == modeFinal && !approvalGated(r.s, scene) {
		return ""
	}
	switch ss.Status {
	case "", story.DaFare, story.Fatto:
		return ""
	case story.Incerto:
		if _, clip := finalUncertain(ss); !clip {
			return "" // senza esito solo il fotogramma
		}
		// stati 0.12: la riserva della clip non ha stato, ora è incerta, così
		// il fotogramma registrato dopo non la confonde
		if c := lastCharge(ss, story.ChargeClip); c >= 0 && ss.Charges[c].Status == "" {
			ss.MarkUncertain(c)
		}
	}
	return ss.Status
}

// finalUncertain dice quale richiesta del definitivo incerto è rimasta senza
// esito: il fotogramma chiave (kf), la clip (clip) o entrambe, dalle rispettive
// riserve (l'ultima di ogni tipo aperta o incerta; aperta vale incerta dopo un
// arresto). Senza riserve con stato (stati 0.12, risposta arrivata senza file
// salvato) è senza esito l'ultima richiesta registrata; senza nessuna, la
// clip.
func finalUncertain(ss *story.SceneState) (kf, clip bool) {
	if ss.Status != story.Incerto {
		return false, false
	}
	k, c := lastCharge(ss, story.ChargeKeyframe), lastCharge(ss, story.ChargeClip)
	kf = k >= 0 && ss.Charges[k].Status != ""
	clip = c >= 0 && ss.Charges[c].Status != ""
	if kf || clip {
		return kf, clip
	}
	if k > c {
		return true, false
	}
	return false, true
}

// lastCharge è l'indice dell'ultima generazione di tipo kind della scena, -1
// se non c'è.
func lastCharge(ss *story.SceneState, kind string) int {
	for i := len(ss.Charges) - 1; i >= 0; i-- {
		if ss.Charges[i].Kind == kind {
			return i
		}
	}
	return -1
}

// keyframeIdle è lo stato del definitivo dopo il fotogramma chiave (after di
// afterKeyframe): fatto se la clip precedente resta in uso, altrimenti da fare.
func keyframeIdle(ss *story.SceneState, after string) string {
	if after != "" {
		return after
	}
	return idle(&ss.Track)
}

// gate toglie dalle scene da trattare quelle il cui definitivo aspetta
// l'approvazione (approvazione nello storyboard, definitiveGate): né
// fotogramma chiave né video, elencate in awaiting_approval; con --scene una
// scena da approvare è codice 2. Una scena con continua_da da una scena in
// attesa aspetta con lei. Valgono solo per gli avvii: riprese già fatte e job
// già partiti restano. Con --keyframes e --bozza nessun gate.
func (r *storyRunner) gate() error {
	if r.mode != modeFinal || !r.s.Approvazione {
		return nil
	}
	var keep []*story.Scene
	for _, scene := range r.order {
		why := ""
		if r.toLaunch(scene) {
			var err error
			if why, err = r.gateWhy(scene); err != nil {
				return err
			}
		}
		if why == "" {
			keep = append(keep, scene)
			continue
		}
		r.await(scene.ID, "niente fotogramma chiave né video", why)
	}
	r.order = keep
	return nil
}

// gateWhy è il motivo per cui il definitivo della scena non parte adesso
// (vuoto se parte): approvazione mancante o non più valida, o continua_da da
// una scena che aspetta l'approvazione. Con --scene è codice 2.
func (r *storyRunner) gateWhy(scene *story.Scene) (string, error) {
	if from := scene.ContinuaDa; from != "" && r.waiting[from] {
		return fmt.Sprintf("continua_da %s: la scena %s aspetta l'approvazione", from, from), nil
	}
	err := definitiveGate(r.s, r.st, scene)
	switch {
	case err == nil:
		return "", nil
	case scene.ID == r.forced:
		return "", usageError("%v: per approvarla giv story approve %s %s", err, r.path, scene.ID)
	}
	return strings.TrimPrefix(err.Error(), "scena "+scene.ID+": "), nil
}

// await registra la scena come da approvare: nessuna richiesta (what dice
// cosa non parte), fuori dal conto delle richieste da fare, elencata in
// awaiting_approval.
func (r *storyRunner) await(id, what, why string) {
	r.waiting[id], r.sent[id] = true, true
	delete(r.judgeEst, id)
	r.thisRun[id] = "da approvare"
	r.man.AwaitingApproval = append(r.man.AwaitingApproval, id)
	fmt.Fprintf(os.Stderr, "scena %s: da approvare, %s (%s): giv story approve %s %s\n", id, what, why, r.path, id)
}

// takesAwaiting elenca in awaiting_approval, a fine giro --bozza, le scene
// del giro la cui bozza ora aspetta giv story approve (approvalStatus da
// approvare: mai approvata o approvazione non più valida), come le elenca
// giv story check; a fine giro --keyframes, se si approva il fotogramma
// chiave (storyboard senza default.bozza), quelle il cui fotogramma la
// aspetta. Locale: nessuna richiesta.
func (r *storyRunner) takesAwaiting() {
	switch {
	case r.mode == modeDraft:
	case r.mode == modeKeyframes && approvalTake(r.s) == story.TakeKeyframe:
	default:
		return
	}
	for _, scene := range r.order {
		if !approvalGated(r.s, scene) {
			continue
		}
		if st, _ := approvalStatus(r.s, r.st, scene, r.exists); st == apprPending {
			r.man.AwaitingApproval = append(r.man.AwaitingApproval, scene.ID)
		}
	}
}

// lastGate ricontrolla l'approvazione subito prima della riserva di ogni video
// definitivo: un fotogramma chiave rigenerato in questa esecuzione
// (--new-keyframe, made) o un file cambiato nel frattempo la rendono vecchia.
// Se non vale più il video non parte e la scena aspetta la nuova
// approvazione, fuori dal conto (true).
func (r *storyRunner) lastGate(scene *story.Scene, made bool) bool {
	if r.mode != modeFinal {
		return false
	}
	err := definitiveGate(r.s, r.st, scene)
	if err == nil {
		return false
	}
	what := "nessun video"
	if made {
		what = "fotogramma chiave nuovo da vedere, nessun video"
	}
	r.await(scene.ID, what, strings.TrimPrefix(err.Error(), "scena "+scene.ID+": "))
	return true
}

// otherJobs valida offline, senza chiave, i job dell'altra serie di riprese
// di ogni scena dello stato (la bozza nel definitivo, con --keyframes e con
// --voices; il definitivo con --bozza), che questa esecuzione non recupera:
// con un job non valido nello stato non parte nessun avvio, come per quelli
// da recuperare (preflight).
func (r *storyRunner) otherJobs() []error {
	var errs []error
	for _, id := range slices.Sorted(maps.Keys(r.st.Scenes)) {
		ss := r.st.Scenes[id]
		if ss == nil {
			continue
		}
		other, name := ss.Draft, "bozza"
		if r.drafts(id) {
			other, name = &ss.Track, "definitivo"
		}
		if other == nil || other.Job == nil {
			continue
		}
		if err := r.validJob(other.Job, true); err != nil {
			errs = append(errs, fmt.Errorf("scena %s (%s): %w", id, name, err))
		}
	}
	return errs
}

// regate ricontrolla, subito prima del suo avvio, il definitivo di una scena
// con continua_da: la clip di partenza registrata in questa esecuzione
// (PinSource) può aver reso vecchia la sua approvazione, o la scena di
// partenza può essere da approvare. Se non parte, salta come da approvare
// (skip) e resta fuori dal conto; con --scene è codice 2.
func (r *storyRunner) regate(scene *story.Scene) (skip bool, err error) {
	if r.mode != modeFinal || !r.s.Approvazione || scene.ContinuaDa == "" {
		return false, nil
	}
	why, err := r.gateWhy(scene)
	if err != nil || why == "" {
		return false, err
	}
	r.await(scene.ID, "niente fotogramma chiave né video", why)
	return true, nil
}

// planDrafts prepara le bozze delle scene (profilo default.bozza, file
// <id>-b<N> in clip/bozza/) e, dove serve, il fotogramma chiave condiviso, col
// profilo e il nome del definitivo; restituisce le generazioni senza stima.
// La bozza non ha costo dichiarato: senza stima da listino, con un tetto è
// codice 2.
func (r *storyRunner) planDrafts(scenes []*story.Scene) ([]string, error) {
	ids := map[string]bool{}
	for _, scene := range scenes {
		ids[scene.ID] = true
	}
	var missing []string
	var rejects []error
	for _, scene := range scenes {
		r.planned[scene.ID] = true
		err := r.planDraft(scene, ids, &missing)
		if err == nil {
			continue
		}
		if code, _ := exitCode(err); code != exitUsage {
			return nil, fmt.Errorf("scena %s: %w", scene.ID, err)
		}
		rejects = append(rejects, fmt.Errorf("scena %s: %w", scene.ID, err))
	}
	if len(rejects) > 0 {
		return nil, storyError(r.path, errors.Join(rejects...))
	}
	return missing, nil
}

// planDraft prepara la bozza della scena (r.vc.jobs) e il fotogramma chiave
// che le manca (r.sc.images); ids sono le scene di questo giro.
func (r *storyRunner) planDraft(scene *story.Scene, ids map[string]bool, missing *[]string) error {
	ss, d, id := r.st.Scenes[scene.ID], r.vc, scene.ID
	d.names[id] = draftName(id, ss)
	o, err := d.videoOptions(scene)
	if err != nil {
		return fmt.Errorf("default.bozza: %w", err)
	}
	o.Out, o.Name = d.out, d.names[id]
	fmt.Fprintf(os.Stderr, "scena %s: bozza %s/%s\n", id, o.Provider, o.Model)
	job, err := prepareVideo(o)
	if err != nil {
		return fmt.Errorf("default.bozza: %w", err)
	}
	d.jobs[id] = job
	switch p := job.plan; {
	case p.costExact != nil:
		r.est[id] = *p.costExact
	case p.CostEstUSD != nil:
		r.est[id] = *p.CostEstUSD
	default:
		*missing = append(*missing, id+" (bozza)")
	}
	if r.needKeyframe(scene) {
		// lo stesso fotogramma del definitivo: profilo e nome del definitivo
		r.sc.names[id] = fmt.Sprintf("%s-r%d", id, takeNumber(ss))
		fo, err := r.sc.videoOptions(scene)
		if err != nil {
			return fmt.Errorf("fotogramma_chiave: %w", err)
		}
		if _, err := r.sc.keyframe(scene, fo); err != nil {
			return fmt.Errorf("fotogramma_chiave: %w", err)
		}
		if scene.FotogrammaChiave.CostoDichiaratoUSD == nil {
			*missing = append(*missing, id+" (fotogramma_chiave)")
		}
	}
	if scene.ContinuaDa != "" {
		if err := storyFrames.Check(); err != nil {
			return &cliError{code: exitError, msg: fmt.Sprintf("continua_da: %v", err)}
		}
		if err := r.continuaReady(scene, ids); err != nil {
			return usageError("%v", err)
		}
	}
	return nil
}

// judgePlan mette nel conto del giro i giudizi di aderenza
// (controllo.aderenza): per ogni scena video da generare, da recuperare o con
// la ripresa ancora da giudicare, la stima preventiva (storyJudgePlanUSD);
// con un tetto un giudizio non stimabile è codice 2 prima di ogni avvio.
// Con --keyframes nessun giudizio.
func (r *storyRunner) judgePlan() error {
	if !judgeControlOn(r.s) || r.mode == modeKeyframes {
		return nil
	}
	var missing []string
	for _, scene := range r.order {
		if !storyJudged(scene) {
			continue
		}
		if !r.toLaunch(scene) && r.jobOf(scene.ID) == nil {
			if due, _, err := storyJudgeDue(r.s, r.st, scene.ID, r.kind); err != nil || !due {
				continue // avvisi a fine giro
			}
		}
		usd, ok, why := storyJudgePlanUSD(r.s, r.st, scene)
		if !ok {
			missing = append(missing, "scena "+scene.ID+": "+why)
			continue
		}
		r.judgeEst[scene.ID] = usd
	}
	if len(missing) == 0 {
		return nil
	}
	if r.capped() {
		return usageError("%s: con %s ogni giudizio di aderenza vuole una stima; non stimabile: %s (togli il tetto o controllo.aderenza)",
			r.path, r.capName(), strings.Join(missing, "; "))
	}
	fmt.Fprintf(os.Stderr, "avviso: giudizio di aderenza senza stima del costo (non contato nel totale): %s\n", strings.Join(missing, "; "))
	return nil
}

// judgeScenes giudica a fine giro (controllo.aderenza), in ordine di
// storyboard, la ripresa in uso della serie di questa esecuzione (definitivo
// o bozza) di ogni scena video che ne ha una ancora da giudicare. Avvisa e
// basta: un giudizio non rigenera mai una ripresa e non ferma l'esecuzione.
func (r *storyRunner) judgeScenes(ctx context.Context) {
	if !judgeControlOn(r.s) || r.mode == modeKeyframes {
		return
	}
	for _, scene := range r.order {
		if ctx.Err() != nil {
			return
		}
		if r.waiting[scene.ID] {
			continue // da approvare: niente di nuovo da giudicare
		}
		if err := r.judgeScene(ctx, scene); err != nil {
			fmt.Fprintf(os.Stderr, "avviso: scena %s: giudizio di aderenza: %s\n", scene.ID, strings.ReplaceAll(localErrMsg(err), "\n", " "))
		}
	}
}

// judgeScene giudica la ripresa della scena se è dovuto (storyJudgeDue: mai
// con un giudizio incerto, nessun reinvio senza una scelta esplicita): una
// sola richiesta, dalla cassa (tetto ricontrollato, riserva e giudizio
// incerto legato alla ripresa salvati insieme prima dell'invio), poi
// l'addebito al posto della riserva e il giudizio al posto di quello incerto.
// Senza esito la riserva resta e il giudizio resta incerto; rifiutato o mai
// partito, riserva e giudizio incerto si tolgono.
func (r *storyRunner) judgeScene(ctx context.Context, scene *story.Scene) error {
	due, why, err := storyJudgeDue(r.s, r.st, scene.ID, r.kind)
	if why != "" {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", why)
	}
	if err != nil || !due {
		return err
	}
	j, err := prepareStoryJudge(r.s, r.st, scene.ID, r.kind)
	if err != nil {
		return err
	}
	ss := r.st.Scenes[scene.ID]
	if ss.TakeByFile(j.File) == nil {
		return fmt.Errorf("ripresa %s non nello stato", j.File)
	}
	ch := story.Charge{Kind: story.ChargeJudge}
	if est, ok := j.EstimateUSD(); ok {
		ch.CostEstUSD = &est
		r.judgeEst[scene.ID] = est // il tetto conta la stima di questa richiesta
	} else if r.capped() {
		return usageError("non stimabile con %s: nessun giudizio inviato", r.capName())
	}
	k, err := r.reserveCharge(ss, ch, func() func() {
		t := ss.TakeByFile(j.File)
		t.Judgements = append(t.Judgements, j.Pending(time.Now()))
		n := len(t.Judgements)
		return func() { t.Judgements = t.Judgements[:n-1] }
	})
	if err != nil {
		return fmt.Errorf("nessun giudizio inviato: %w", err)
	}
	r.judgeSent[scene.ID] = true
	res, err := j.Send(ctx)
	t := ss.TakeByFile(j.File)
	i := -1
	for n := range t.Judgements {
		if g := t.Judgements[n]; g.Key == j.Key && g.Status == story.Incerto {
			i = n // l'ultimo: quello appena legato
		}
	}
	if err != nil {
		if judgeUncertainErr(err) {
			// forse addebitato: la riserva resta, il giudizio resta incerto
			ss.MarkUncertain(k)
			if i >= 0 {
				t.Judgements[i].Message = judge.SafeText(err.Error(), maxJudgeSummary)
			}
		} else {
			releaseCharge(ss, k)
			if i >= 0 {
				t.Judgements = slices.Delete(t.Judgements, i, i+1)
			}
		}
		r.save()
		return err
	}
	settleJudge(ss, k, res.UsageUSD, res.FromUsage)
	if i >= 0 {
		t.Judgements[i] = res.Judgement
	}
	r.save()
	r.man.Judgements++
	r.judged[scene.ID] = res.Out
	return nil
}
