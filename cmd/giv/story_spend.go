package main

import (
	"fmt"
	"os"
	"slices"
	"time"

	"gen-image-video-cli/internal/story"
)

// La cassa di giv story run: un solo punto per ogni richiesta a pagamento
// (fotogrammi chiave, clip, voci, trascrizioni). Il conto sta nello stato:
// addebiti registrati, riserve aperte (scritte prima dell'invio) e incerte
// (invio senza esito) sono le Charge di ogni scena, anche delle scene tolte
// (State.Spent, una voce per richiesta); la cassa ci somma le stime di ciò
// che resta da chiedere in questa esecuzione, nuova richiesta compresa, e
// confronta col tetto sui valori non arrotondati. I tetti sono due e valgono
// entrambi: tetto_costo_usd dello storyboard, sulla spesa registrata nello
// stato, e --max-cost, sulle sole riserve e addebiti di questa esecuzione
// (ofThisRun); il primo dei due che si supera ferma gli avvii. La usa solo il
// coordinatore, unico scrittore dello stato: i lavoratori non toccano né lo
// stato né il conto.

// capped: c'è almeno un tetto (tetto_costo_usd o --max-cost): ogni richiesta
// vuole una stima, altrimenti codice 2 prima di ogni avvio.
func (r *storyRunner) capped() bool {
	return r.s.TettoCostoUSD != nil || r.runCap != nil
}

// capName è il tetto da nominare nei rifiuti per una stima che manca:
// tetto_costo_usd se c'è, altrimenti --max-cost.
func (r *storyRunner) capName() string {
	if r.s.TettoCostoUSD != nil {
		return "tetto_costo_usd"
	}
	return "--max-cost"
}

// ofThisRun: la generazione è stata riservata (o registrata) in questa
// esecuzione, quindi conta per --max-cost; i job recuperati restano
// dell'esecuzione che li ha avviati.
func (r *storyRunner) ofThisRun(c story.Charge) bool {
	return !c.At.Before(r.started)
}

// runSpent è la spesa di questa esecuzione per --max-cost: addebiti, riserve
// aperte e incerte riservate da questa esecuzione (anche di scene poi tolte),
// a piena precisione.
func (r *storyRunner) runSpent() float64 {
	var total float64
	for _, ss := range r.st.Scenes {
		if ss == nil {
			continue
		}
		for _, c := range ss.Charges {
			if r.ofThisRun(c) {
				total += c.Spent()
			}
		}
	}
	return total
}

// capBreach è il tetto superato: nome, valore, spesa che conta per lui
// (what dice quale) e stima delle richieste ancora da fare.
type capBreach struct {
	name, what       string
	cap, spent, next float64
}

// overCap è il primo dei due tetti che la spesa più la stima non arrotondata
// delle richieste ancora da fare (capNext) supera: tetto_costo_usd con la
// spesa registrata nello stato (addebiti e riserve aperte, anche delle scene
// tolte), poi --max-cost con quella di questa esecuzione (runSpent). nil se
// nessuno lo è, o senza tetti.
func (r *storyRunner) overCap() *capBreach {
	if !r.capped() {
		return nil
	}
	next := r.capNext()
	if cap := r.s.TettoCostoUSD; cap != nil {
		if spent := r.st.Spent(); spent+next > *cap {
			return &capBreach{name: "tetto_costo_usd", what: "spesa già fatta", cap: *cap, spent: spent, next: next}
		}
	}
	if cap := r.runCap; cap != nil {
		if spent := r.runSpent(); spent+next > *cap {
			return &capBreach{name: "--max-cost", what: "spesa di questo giro", cap: *cap, spent: spent, next: next}
		}
	}
	return nil
}

// checkCap ferma prima di un avvio se la spesa più la stima delle richieste
// ancora da fare supera uno dei due tetti: codice 2, nessun nuovo avvio.
func (r *storyRunner) checkCap() error {
	b := r.overCap()
	if b == nil {
		return nil
	}
	removed := ""
	if rs, v := removedSpend(r.s, r.st); len(rs) > 0 && b.name == "tetto_costo_usd" {
		removed = " (di cui " + removedText(rs, v) + ")"
	}
	return usageError("%s %.4f $: %s %.4f $%s più stima delle generazioni da fare %.4f $ lo supera; nessun nuovo avvio, stato in %s",
		b.name, b.cap, b.what, round4(b.spent), removed, round4(b.next), r.st.Path())
}

// capNext è la stima non arrotondata delle generazioni ancora da chiedere:
// voci, trascrizioni (controllo.voce), scene video la cui clip (o bozza) non è
// ancora riservata in questa esecuzione (sent), col fotogramma chiave da fare
// (con --keyframes solo questo), e giudizi di aderenza non ancora riservati.
// Una richiesta già riservata conta solo con la sua riserva nello stato, mai
// due volte.
func (r *storyRunner) capNext() float64 {
	next := r.voiceNext() + r.voiceCheckNext()
	for _, scene := range r.order {
		if !r.sent[scene.ID] && r.toLaunch(scene) {
			if r.mode != modeKeyframes {
				next += r.est[scene.ID]
			}
			if r.needKeyframe(scene) {
				next += *scene.FotogrammaChiave.CostoDichiaratoUSD
			}
		}
		if est, ok := r.judgeEst[scene.ID]; ok && !r.judgeSent[scene.ID] {
			next += est
		}
	}
	return next
}

// reserveCharge è la riserva di una richiesta a pagamento, prima del suo
// unico POST: tetto ricontrollato (checkCap), poi ch fra le generazioni della
// scena come riserva aperta e apply (lo stato della richiesta: incerta), in
// una sola scrittura atomica dello stato. Prima registra gli esiti già
// arrivati dei lavori aperti (addebiti reali, errori): dopo l'errore di una
// scena nessuna riserva (errHalted). Se lo stato non si salva, o non si
// è salvato una volta in questa esecuzione, niente cambia (apply restituisce
// come annullarsi) e la richiesta non parte. Restituisce l'indice della
// riserva in ss.Charges, per settle, releaseCharge e MarkUncertain.
func (r *storyRunner) reserveCharge(ss *story.SceneState, ch story.Charge, apply func() (undo func())) (int, error) {
	r.poll()
	if len(r.halt) > 0 {
		return 0, errHalted
	}
	if err := r.checkCap(); err != nil {
		return 0, err
	}
	if r.saveErr != nil {
		return 0, fmt.Errorf("stato non salvato in questa esecuzione, nessuna nuova richiesta inviata: %w", r.saveErr)
	}
	undo := apply()
	ch.At = time.Now()
	k := ss.Reserve(ch)
	if err := r.st.Save(); err != nil {
		ss.Release(k)
		undo()
		r.saveErr = err
		return 0, fmt.Errorf("stato non salvato, nessuna richiesta inviata: %w", err)
	}
	return k, nil
}

// releaseCharge toglie la riserva k: richiesta rifiutata (4xx), mai partita o
// caricamento non riuscito prima del job, quindi nessun addebito.
func releaseCharge(ss *story.SceneState, k int) {
	ss.Release(k)
}

// settle registra la generazione k con l'addebito appena noto, al posto della
// riserva: il costo riportato dal provider prende il posto della stima;
// senza, la stima dai consumi riportati (usageUSD) la sostituisce solo se non
// è più bassa, a piena precisione; una clip senza stima resta al costo
// dichiarato. Con costo e consumi a zero la generazione resta alla sua stima.
func settle(ss *story.SceneState, k int, costUSD, usageUSD float64) {
	ss.Settle(k, costUSD)
	c := &ss.Charges[k]
	if costUSD > 0 || usageUSD <= 0 || (c.CostEstUSD == nil && (c.Kind == story.ChargeClip || c.Kind == story.ChargeDraft)) {
		return
	}
	if c.CostEstUSD == nil || usageUSD > *c.CostEstUSD {
		u := usageUSD
		c.CostEstUSD = &u
	}
}

// settleJudge registra la riserva k di un giudizio di aderenza con il costo
// calcolato dai consumi (token riportati da gemini per la tariffa del
// listino: il cost_usage_usd del registro) al posto della stima, anche se è
// più basso: per il giudizio è il costo vero. Con i consumi incompleti il
// giudizio dà come costo la stima per eccesso, che quindi resta; 0 (non
// calcolabile) lascia la stima della riserva. fromUsage dice che usageUSD
// viene dai token: lo stato lo segna (CostFromUsage), l'importo e i totali
// restano gli stessi.
func settleJudge(ss *story.SceneState, k int, usageUSD float64, fromUsage bool) {
	ss.Settle(k, 0)
	if usageUSD > 0 {
		u := usageUSD
		ss.Charges[k].CostEstUSD = &u
		ss.Charges[k].CostFromUsage = fromUsage
	}
}

// jobCharge è l'indice della generazione di tipo kind (clip o bozza) del job
// id, -1 se non c'è.
func jobCharge(ss *story.SceneState, kind, id string) int {
	return slices.IndexFunc(ss.Charges, func(ch story.Charge) bool { return ch.Kind == kind && ch.JobID != "" && ch.JobID == id })
}

// saveState scrive lo stato; dopo un errore nessuna nuova richiesta parte in
// questa esecuzione (reserveCharge), le richieste già partite si concludono.
func (r *storyRunner) saveState() error {
	if err := r.st.Save(); err != nil {
		if r.saveErr == nil {
			r.saveErr = err
		}
		return err
	}
	return nil
}

// save è saveState con l'errore come avviso: l'ultima generazione registrata
// prima dell'invio resta nel file di stato (il job anche in giv-jobs.jsonl).
func (r *storyRunner) save() {
	if err := r.saveState(); err != nil {
		fmt.Fprintf(os.Stderr, "avviso: %v\n", err)
	}
}
