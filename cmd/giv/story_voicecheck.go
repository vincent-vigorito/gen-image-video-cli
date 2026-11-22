package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/speech"
	"gen-image-video-cli/internal/story"
)

// Controllo della voce nello storyboard (controllo.voce, V-T8): giv story run
// trascrive ogni voce generata da giv e non ancora controllata (come giv qc
// --voice, con xai) e la confronta col testo della scena. Ogni trascrizione è
// a pagamento: stimata nel piano e nel tetto, riservata nello stato prima
// della richiesta, una sola richiesta mai ripetuta; un esito incerto resta
// contato e non si rilancia (la voce si ricontrolla solo se cambia il file).
// Il controllo avvisa e basta (K6): una voce con parole diverse non si
// rigenera e non ferma i video.

// voiceCheckProvider è il provider della trascrizione (solo xai).
const voiceCheckProvider = "xai"

// voiceControlOn dice se lo storyboard chiede il controllo della voce.
func voiceControlOn(s *story.Storyboard) bool {
	return s.Controllo != nil && s.Controllo.Voce
}

// storyVoiceCheck è il controllo della voce di una scena fatto (o tentato) in
// questa esecuzione: i controlli voice e language, la trascrizione e il
// confronto; skipped dice perché non c'è.
type storyVoiceCheck struct {
	checks     []montage.QCCheck
	transcript *qcTranscript
	comparison *speech.Comparison
	skipped    string
}

// voiceChecksToDo sono, in ordine, le scene di cui trascrivere la voce: quelle
// con la voce da generare in questa esecuzione e quelle con una voce generata
// mai controllata (o controllata su un altro file); con --scene solo quella.
// Una trascrizione incerta della voce in uso non si rilancia: avviso.
func (r *storyRunner) voiceChecksToDo() []*story.Scene {
	var out []*story.Scene
	for i := range r.s.Scene {
		scene := &r.s.Scene[i]
		if !needsVoice(scene) || (r.forced != "" && scene.ID != r.forced) {
			continue
		}
		if slices.Contains(r.voiceOrder, scene) {
			out = append(out, scene)
			continue
		}
		ss := r.st.Scenes[scene.ID]
		if ss == nil || ss.Voice == nil || ss.VoiceStatus != story.Fatto {
			continue
		}
		if vc := ss.VoiceCheck; vc != nil && vc.File == ss.Voice.File {
			if vc.Status == story.Incerto {
				fmt.Fprintf(os.Stderr, "avviso: scena %s: %s\n", scene.ID, voiceCheckUncertain(ss.Voice.File))
			}
			continue
		}
		out = append(out, scene)
	}
	return out
}

// voiceCheckUncertain è l'avviso per una trascrizione rimasta senza esito.
func voiceCheckUncertain(voice string) string {
	return fmt.Sprintf("controllo della voce: la trascrizione di %s è rimasta senza esito e può essere stata addebitata (contata nella spesa): non si rilancia; per controllarla a mano giv qc --voice %s --script \"<testo della scena>\"", voice, voice)
}

// voiceCheckPreflight stima le trascrizioni da fare (sulla durata stimata
// dai caratteri: quella misurata vale alla richiesta) e prepara il client:
// con un tetto, una trascrizione non stimabile ferma tutto (codice 2), come
// la chiave xai che manca. Restituisce i rifiuti.
func (r *storyRunner) voiceCheckPreflight() ([]error, error) {
	if !voiceControlOn(r.s) {
		return nil, nil
	}
	var rejects []error
	for _, scene := range r.voiceChecksToDo() {
		secs := voiceDuration(storyVoiceRequest(r.s, scene))
		if est, why := speech.EstimateTranscribe(voiceCheckProvider, secs, time.Now()); est != nil {
			r.checkEst[scene.ID] = est.Exact
		} else if r.s.TettoCostoUSD != nil {
			rejects = append(rejects, fmt.Errorf("scena %s: controllo della voce non stimabile (%s): con tetto_costo_usd non parte", scene.ID, why))
			continue
		} else {
			fmt.Fprintf(os.Stderr, "avviso: scena %s: controllo della voce senza stima del costo: %s\n", scene.ID, why)
			r.checkEst[scene.ID] = 0
		}
		r.checkOrder = append(r.checkOrder, scene)
	}
	if len(rejects) > 0 || len(r.checkOrder) == 0 {
		return rejects, nil
	}
	p, err := newProvider(voiceCheckProvider)
	if err != nil {
		return nil, fmt.Errorf("controllo.voce: trascrizione con %s: %w", voiceCheckProvider, err)
	}
	t, ok := p.(speech.Transcriber)
	if !ok {
		return nil, usageError("controllo.voce: trascrizione non disponibile con %s", voiceCheckProvider)
	}
	r.transcriber = t
	return nil, nil
}

// voiceCheckNext è la stima non arrotondata delle trascrizioni ancora da chiedere.
func (r *storyRunner) voiceCheckNext() float64 {
	var next float64
	for _, scene := range r.checkOrder {
		if !r.checkDone[scene.ID] {
			next += r.checkEst[scene.ID]
		}
	}
	return next
}

// voiceMade aggiorna la stima della trascrizione con la durata della voce
// appena generata.
func (r *storyRunner) voiceMade(id string, durationS float64) {
	if _, ok := r.checkEst[id]; !ok || durationS <= 0 {
		return
	}
	if est, _ := speech.EstimateTranscribe(voiceCheckProvider, durationS, time.Now()); est != nil && est.Exact > r.checkEst[id] {
		r.checkEst[id] = est.Exact
	}
}

// checkVoices trascrive, in ordine, le voci da controllare. Ogni esito è un
// avviso nel controllo: nessuna voce si rigenera e i video proseguono.
func (r *storyRunner) checkVoices(ctx context.Context) {
	for _, scene := range r.checkOrder {
		if ctx.Err() != nil {
			return
		}
		r.checkVoice(ctx, scene)
	}
}

// generatedVoiceFile è la voce in uso della scena id, l'unico file che il
// controllo della voce carica verso il provider: solo il nome che scrive
// saveVoice (voce/<id>-v<N>.wav della scena; un altro file dello storyboard
// indicato dallo stato è rifiutato), in una cartella voce vera dentro quella
// dello storyboard e come file normale, senza link né sulla cartella né sul
// file.
func (r *storyRunner) generatedVoiceFile(id string) (string, error) {
	ss := r.st.Scenes[id]
	if ss == nil || ss.Voice == nil {
		return "", fmt.Errorf("scena %s: voce: %w", id, story.ErrNotDone)
	}
	dir, name, _ := strings.Cut(ss.Voice.File, "/")
	if dir != storyVoiceDir || !isVoiceTake(id, name) {
		return "", fmt.Errorf("voce %q: non è una voce generata da giv story run (%s/%s-v<N>.wav)", ss.Voice.File, storyVoiceDir, id)
	}
	fi, err := r.root.Lstat(storyVoiceDir)
	if err != nil {
		return "", fmt.Errorf("cartella %s: %w", storyVoiceDir, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s: non è una cartella (un link non vale)", storyVoiceDir)
	}
	vd, err := r.root.OpenRoot(storyVoiceDir)
	if err != nil {
		return "", fmt.Errorf("cartella %s: %w", storyVoiceDir, err)
	}
	defer vd.Close()
	fi, err = vd.Lstat(name)
	if err != nil {
		return "", fmt.Errorf("voce %s: %w", ss.Voice.File, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("voce %s: non è un file normale (un link non vale)", ss.Voice.File)
	}
	p := filepath.Join(r.s.Dir, storyVoiceDir, name)
	if pi, err := os.Stat(p); err != nil || !os.SameFile(fi, pi) {
		return "", fmt.Errorf("voce %s: il percorso non porta al file della cartella %s", ss.Voice.File, storyVoiceDir)
	}
	return p, nil
}

// isVoiceTake: name è <id>-v<N>.wav con N intero positivo scritto come lo
// scrive saveVoice.
func isVoiceTake(id, name string) bool {
	num, ok := strings.CutPrefix(name, id+"-v")
	if !ok {
		return false
	}
	num, ok = strings.CutSuffix(num, ".wav")
	if !ok {
		return false
	}
	n, err := strconv.Atoi(num)
	return err == nil && n > 0 && strconv.Itoa(n) == num
}

// checkVoice trascrive la voce in uso della scena: controlli gratuiti e stima
// sul file (prepareVoiceCheck), tetto sul valore non arrotondato (spesa fatta
// più questa trascrizione più le generazioni ancora da fare: i video hanno la
// precedenza sul controllo), riserva e voice_check incerto scritti nello stato
// prima della richiesta, una sola richiesta. Un esito incerto resta
// riservato; una richiesta rifiutata o un caricamento non completo tolgono la
// riserva (nessun addebito).
func (r *storyRunner) checkVoice(ctx context.Context, scene *story.Scene) {
	id := scene.ID
	// fatta o saltata, non è più fra le trascrizioni da fare
	defer func() { r.checkDone[id] = true }()
	skip := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		fmt.Fprintf(os.Stderr, "avviso: scena %s: controllo della voce: %s\n", id, msg)
		r.voiceChecks[id] = &storyVoiceCheck{skipped: msg}
	}
	ss := r.st.Scenes[id]
	file, err := r.generatedVoiceFile(id)
	if err != nil {
		skip("non fatto: %v", err)
		return
	}
	req := storyVoiceRequest(r.s, scene)
	o := qcVoiceOptions{Provider: voiceCheckProvider, File: file, Script: req.Text, Lang: req.Language, Dict: req.Dict, client: r.transcriber}
	cap := r.s.TettoCostoUSD
	if cap != nil {
		// con un tetto la durata va misurata sui campioni e la stima c'è
		o.MaxCost = cap
	}
	fmt.Fprintf(os.Stderr, "scena %s: controllo della voce %s…\n", id, ss.Voice.File)
	vc, err := prepareVoiceCheck(o)
	if err != nil {
		skip("non fatto: %s", strings.ReplaceAll(err.Error(), "--max-cost", "tetto_costo_usd"))
		return
	}
	ch := story.Charge{Kind: story.ChargeTranscribe, At: time.Now()}
	if vc.est != nil {
		e := vc.est.Exact
		ch.CostEstUSD = &e
	}
	if cap != nil {
		r.checkEst[id] = *ch.CostEstUSD // misurata sul file: al posto di quella dai caratteri
		if spent, next := r.st.Spent(), r.capNext(0); spent+next > *cap {
			skip("non fatto: spesa già fatta %.4f $ più generazioni da fare %.4f $ (trascrizione di %.6f $ compresa) oltre tetto_costo_usd %.4f $",
				round4(spent), round4(next), vc.est.USD, *cap)
			return
		}
	}
	prev := ss.VoiceCheck
	ss.Charges = append(ss.Charges, ch)
	k := len(ss.Charges) - 1
	ss.VoiceCheck = &story.VoiceCheck{File: ss.Voice.File, Status: story.Incerto, At: ch.At}
	if err := r.st.Save(); err != nil {
		ss.Charges, ss.VoiceCheck = ss.Charges[:k], prev
		skip("stato non salvato, nessuna trascrizione inviata: %v", err)
		return
	}
	out, err := vc.run(ctx)
	if err != nil {
		// incerta è solo la richiesta partita senza esito (run: codice 5 o
		// StartError); un caricamento non completo o un 4xx sono ordinari
		// (decisione 17), quindi non httpx.Start, che li direbbe incerti
		var ce *cliError
		var se *httpx.StartError
		if (errors.As(err, &ce) && ce.code == exitAPI) || errors.As(err, &se) {
			// forse addebitata: resta contata e incerta, non si rilancia
			ss.VoiceCheck.Message = err.Error()
			r.save()
			skip("%v (contata nella spesa); %s", err, voiceCheckUncertain(ss.Voice.File))
			return
		}
		// rifiutata, mai partita o caricamento non completo: nessun addebito
		ss.Charges = slices.Delete(ss.Charges, k, k+1)
		ss.VoiceCheck = prev
		r.save()
		skip("trascrizione non riuscita (nessun addebito): %v", err)
		return
	}
	if c := &ss.Charges[k]; out.usageUSD > 0 && (c.CostEstUSD == nil || out.usageUSD > *c.CostEstUSD) {
		// addebito dalla durata riportata: prende il posto della riserva solo
		// se non è più basso, a piena precisione
		u := out.usageUSD
		c.CostEstUSD = &u
	}
	res := &storyVoiceCheck{transcript: &out.Transcript, comparison: out.Comparison}
	for _, c := range out.Checks {
		if c.Name == "voice" || c.Name == "language" {
			res.checks = append(res.checks, c)
		}
	}
	ss.VoiceCheck.Result, ss.VoiceCheck.Message = voiceCheckResult(res.checks)
	ss.VoiceCheck.Status = story.Fatto
	r.voiceChecks[id] = res
	r.man.VoiceChecks++
	if err := r.st.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "avviso: scena %s: controllo della voce fatto ma stato non salvato (resta incerto): %v\n", id, err)
	}
}

// voiceCheckResult riassume i controlli voice e language per lo stato: il
// peggiore degli esiti e i messaggi di quelli non ok.
func voiceCheckResult(checks []montage.QCCheck) (string, string) {
	var results, msgs []string
	for _, c := range checks {
		results = append(results, c.Result)
		if c.Message != "" && (c.Result != montage.QCOK || c.Name == "voice") {
			msgs = append(msgs, c.Message)
		}
	}
	return montage.QCWorst(results...), strings.Join(msgs, "; ")
}

// voiceRowChecks aggiunge alla riga della voce di una scena (controllo
// tecnico già fatto) il controllo della voce: quello di questa esecuzione o
// quello registrato nello stato per la stessa voce.
func voiceRowChecks(row *voiceRow, ss *story.SceneState, now *storyVoiceCheck) {
	if row.QCReport == nil || montage.QCRank(row.Result) < 0 {
		return
	}
	var add []montage.QCCheck
	vc := ss.VoiceCheck
	switch {
	case now != nil && now.skipped != "":
		add = []montage.QCCheck{{Name: "voice", Result: montage.QCWarning, Skipped: true, Message: now.skipped}}
	case now != nil:
		add = now.checks
		row.Transcript, row.Comparison = now.transcript, now.comparison
	case vc != nil && ss.Voice != nil && vc.File == ss.Voice.File && vc.Status == story.Fatto:
		add = []montage.QCCheck{{Name: "voice", Result: vc.Result, Message: strings.TrimPrefix(vc.Message+"; ", "; ") +
			"trascrizione del " + vc.At.UTC().Format(time.RFC3339)}}
	case vc != nil && ss.Voice != nil && vc.File == ss.Voice.File:
		add = []montage.QCCheck{{Name: "voice", Result: montage.QCWarning, Skipped: true, Message: "trascrizione rimasta senza esito (contata nella spesa), non rilanciata"}}
	default:
		add = []montage.QCCheck{{Name: "voice", Result: montage.QCOK, Skipped: true, Message: "voce non ancora trascritta: la controlla giv story run"}}
	}
	row.Checks = append(row.Checks, add...)
	results := []string{row.Result}
	for _, c := range add {
		if !c.Skipped || c.Result != montage.QCOK {
			results = append(results, c.Result)
		}
	}
	row.Result = montage.QCWorst(results...)
}
