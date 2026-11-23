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
// addebiti registrati e riserve aperte (richieste inviate senza addebito
// noto) sono le Charge di ogni scena, anche delle scene tolte
// (State.Spent); la cassa ci somma le stime di ciò che resta da chiedere in
// questa esecuzione, nuova richiesta compresa, e confronta col tetto sui
// valori non arrotondati. La usa solo il coordinatore, unico scrittore dello
// stato: i lavoratori non toccano né lo stato né il conto.

// capOver: spesa registrata (addebiti e riserve aperte) e stima non
// arrotondata delle richieste ancora da fare (capNext); over se insieme
// superano il tetto. Senza tetto over è sempre falso.
func (r *storyRunner) capOver() (spent, next float64, over bool) {
	cap := r.s.TettoCostoUSD
	if cap == nil {
		return 0, 0, false
	}
	spent, next = r.st.Spent(), r.capNext()
	return spent, next, spent+next > *cap
}

// checkCap ferma prima di un avvio se la spesa registrata più la stima delle
// richieste ancora da fare supera il tetto: codice 2, nessun nuovo avvio.
func (r *storyRunner) checkCap() error {
	spent, next, over := r.capOver()
	if !over {
		return nil
	}
	removed := ""
	if rs, v := removedSpend(r.s, r.st); len(rs) > 0 {
		removed = " (di cui " + removedText(rs, v) + ")"
	}
	return usageError("tetto_costo_usd %.4f $: spesa già fatta %.4f $%s più stima delle generazioni da fare %.4f $ lo supera; nessun nuovo avvio, stato in %s",
		*r.s.TettoCostoUSD, round4(spent), removed, round4(next), r.st.Path())
}

// capNext è la stima non arrotondata delle generazioni ancora da chiedere:
// voci, trascrizioni (controllo.voce) e scene video la cui clip non è ancora
// riservata in questa esecuzione (sent), col fotogramma chiave da fare. Una
// scena già riservata conta solo con la sua riserva nello stato, mai due volte.
func (r *storyRunner) capNext() float64 {
	next := r.voiceNext() + r.voiceCheckNext()
	for _, scene := range r.order {
		if !r.sent[scene.ID] && r.toLaunch(scene) {
			next += r.est[scene.ID]
			if r.needKeyframe(scene) {
				next += *scene.FotogrammaChiave.CostoDichiaratoUSD
			}
		}
	}
	return next
}

// reserveCharge è la riserva di una richiesta a pagamento, prima del suo
// unico POST: tetto ricontrollato (checkCap), poi ch fra le generazioni della
// scena e apply (lo stato della richiesta: incerta), in una sola scrittura
// atomica dello stato. Se lo stato non si salva, o non si è salvato una
// volta in questa esecuzione, niente cambia (apply restituisce come
// annullarsi) e la richiesta non parte. Restituisce l'indice della riserva in
// ss.Charges, per settleCharge e releaseCharge.
func (r *storyRunner) reserveCharge(ss *story.SceneState, ch story.Charge, apply func() (undo func())) (int, error) {
	if err := r.checkCap(); err != nil {
		return 0, err
	}
	if r.saveErr != nil {
		return 0, fmt.Errorf("stato non salvato in questa esecuzione, nessuna nuova richiesta inviata: %w", r.saveErr)
	}
	undo := apply()
	ch.At = time.Now()
	ss.Charges = append(ss.Charges, ch)
	if err := r.st.Save(); err != nil {
		ss.Charges = ss.Charges[:len(ss.Charges)-1]
		undo()
		r.saveErr = err
		return 0, fmt.Errorf("stato non salvato, nessuna richiesta inviata: %w", err)
	}
	return len(ss.Charges) - 1, nil
}

// releaseCharge toglie la riserva k: richiesta rifiutata (4xx), mai partita o
// caricamento non riuscito prima del job, quindi nessun addebito.
func releaseCharge(ss *story.SceneState, k int) {
	ss.Charges = slices.Delete(ss.Charges, k, k+1)
}

// settleCharge porta nella riserva c l'addebito appena noto: il costo
// riportato dal provider prende il posto della stima; senza, la stima dai
// consumi riportati (usageUSD) la sostituisce solo se non è più bassa, a
// piena precisione. Una clip senza stima resta al costo dichiarato.
func settleCharge(c *story.Charge, costUSD, usageUSD float64) {
	if costUSD > 0 {
		c.CostUSD = costUSD
		return
	}
	if usageUSD <= 0 {
		return
	}
	if c.CostEstUSD == nil && c.Kind == story.ChargeClip {
		return
	}
	if c.CostEstUSD == nil || usageUSD > *c.CostEstUSD {
		u := usageUSD
		c.CostEstUSD = &u
	}
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
