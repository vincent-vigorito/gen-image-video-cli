package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/judge"
	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/story"
)

// Giudizio di aderenza nello storyboard (controllo.aderenza, spento di
// partenza): lo stesso servizio di giv qc --judge (prepareJudge,
// judgeCheck.run) su una ripresa di una scena video, con la richiesta della
// scena (descrizione dei personaggi e prompt, o le inquadrature, come nella
// richiesta del video) e il primo riferimento locale. Le scene avatar non
// hanno una richiesta visiva da confrontare (niente prompt) e non si
// giudicano. Il giudizio si lega
// alla ripresa giudicata (story.Take.Judgements) e si recupera dallo stato
// senza reinvio; la spesa passa dal cassiere di giv story run (riserva
// story.ChargeJudge salvata prima dell'invio, una sola richiesta, addebito al
// posto della riserva). Un giudizio incerto non si rilancia da solo. Nessun
// giudizio rigenera una ripresa: l'autore decide.

// judgeControlOn dice se lo storyboard chiede il giudizio di aderenza.
func judgeControlOn(s *story.Storyboard) bool {
	return s.Controllo != nil && s.Controllo.Aderenza
}

// judgeKeyVersion entra nell'impronta: cambia se cambia il modo di giudicare.
const judgeKeyVersion = "giv-judge-1"

// storyJudged dice se la scena si giudica: solo le scene video (un'avatar
// non ha una richiesta visiva).
func storyJudged(sc *story.Scene) bool { return sc != nil && sc.Tipo == story.Video }

// storyJudgePrompt è la richiesta della scena da confrontare, come nella
// richiesta del video: descrizione dei personaggi del kit davanti al prompt,
// oppure alle inquadrature in ordine, con i loro secondi.
func storyJudgePrompt(s *story.Storyboard, sc *story.Scene) (string, error) {
	text := strings.TrimSpace(sc.Prompt)
	if len(sc.Inquadrature) > 0 {
		parts := make([]string, 0, len(sc.Inquadrature))
		for i, q := range sc.Inquadrature {
			parts = append(parts, fmt.Sprintf("inquadratura %d (%d s): %s", i+1, q.Durata, strings.TrimSpace(q.Prompt)))
		}
		text = strings.Join(parts, "; ")
	}
	if text == "" {
		return "", errors.New("la scena non ha un prompt da confrontare")
	}
	prompt := strings.TrimSpace(s.ConDescrizione(sc, text))
	if n := utf8.RuneCountInString(prompt); n > judge.MaxPromptChars {
		return "", fmt.Errorf("richiesta della scena di %d caratteri: il giudizio ne accetta al massimo %d", n, judge.MaxPromptChars)
	}
	return prompt, nil
}

// storyJudgeInputs sono la richiesta della scena e il riferimento del
// giudizio, senza leggere i file: il primo file locale (.png, .jpg, .webp)
// degli insiemi in usa, altrimenti il fotogramma chiave in uso, altrimenti
// immagine; gli indirizzi non valgono. ref è "" senza riferimento.
func storyJudgeInputs(s *story.Storyboard, ss *story.SceneState, sc *story.Scene) (prompt, ref string, err error) {
	prompt, err = storyJudgePrompt(s, sc)
	if err != nil {
		return "", "", err
	}
	var cands []string
	for _, name := range sc.Usa {
		cands = append(cands, s.Riferimenti[name]...)
	}
	if ss != nil && ss.Keyframe != nil {
		cands = append(cands, ss.Keyframe.File)
	}
	cands = append(cands, sc.Immagine)
	for _, c := range cands {
		if c == "" || story.IsURL(c) || !judgeImageExt(c) {
			continue
		}
		// un file del kit (già assoluto) resta chiuso nella cartella del kit,
		// ogni altro in quella dello storyboard
		if rel, ok := s.KitRel(c); ok && s.KitData != nil {
			if err := s.KitData.CheckPath(rel); err != nil {
				return "", "", fmt.Errorf("riferimento kit:%s: %v", rel, err)
			}
			return prompt, c, nil
		}
		p, err := s.CheckFile(c)
		if err != nil {
			return "", "", fmt.Errorf("riferimento %s: %v", c, err)
		}
		return prompt, p, nil
	}
	return prompt, "", nil
}

// judgeImageExt: il riferimento del giudizio è un .png, .jpg o .webp.
func judgeImageExt(p string) bool {
	switch strings.ToLower(p[strings.LastIndex(p, ".")+1:]) {
	case "png", "jpg", "jpeg", "webp":
		return true
	}
	return false
}

// fileSHA256 è l'impronta del contenuto di un file normale.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: non è un file normale", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// storyJudgeKey è l'impronta di un giudizio: modello, richiesta, contenuto
// del riferimento e della ripresa. Lo stesso giudizio della stessa ripresa ha
// la stessa impronta.
func storyJudgeKey(model, prompt, ref, clip string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n%d:%s\n", judgeKeyVersion, model, len(prompt), prompt)
	for _, p := range []string{ref, clip} {
		sum := "-"
		if p != "" {
			s, err := fileSHA256(p)
			if err != nil {
				return "", err
			}
			sum = s
		}
		fmt.Fprintf(h, "%s\n", sum)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// storyJudgeTake è la ripresa in uso di tipo kind (story.TakeFinal o
// story.TakeDraft) della scena id e il suo file controllato (nella cartella
// dello storyboard); story.ErrNotDone se non c'è.
func storyJudgeTake(s *story.Storyboard, st *story.State, id, kind string) (*story.Take, string, error) {
	if kind != story.TakeFinal && kind != story.TakeDraft {
		return nil, "", fmt.Errorf("ripresa %q: si giudicano solo bozza e definitivo", kind)
	}
	ss := st.Scenes[id]
	var t *story.Take
	if ss != nil {
		t = ss.Current(kind)
	}
	if t == nil || t.File == "" {
		return nil, "", fmt.Errorf("scena %s: %s: %w", id, kind, story.ErrNotDone)
	}
	p, err := s.CheckFile(t.File)
	if err != nil {
		return nil, "", fmt.Errorf("scena %s: %s %s: %v", id, kind, t.File, err)
	}
	return t, p, nil
}

// storyJudgeDue dice se la ripresa in uso di tipo kind della scena id va
// giudicata (controllo.aderenza acceso, scena video con la ripresa): no in
// silenzio senza controllo, per le scene non video e senza la ripresa; no
// con why (avviso) se ha un giudizio incerto (nessun reinvio senza una scelta
// esplicita) o se la scena non si può giudicare (senza prompt, riferimento
// fuori cartella); no se ha già un giudizio fatto con la stessa impronta.
// err solo se la ripresa o il riferimento non si leggono. Gratuito: legge i
// file per l'impronta, nessun ffmpeg, nessuna chiamata.
func storyJudgeDue(s *story.Storyboard, st *story.State, id, kind string) (due bool, why string, err error) {
	sc := s.Scena(id)
	if !judgeControlOn(s) || !storyJudged(sc) {
		return false, "", nil
	}
	t, clip, err := storyJudgeTake(s, st, id, kind)
	if errors.Is(err, story.ErrNotDone) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	prompt, ref, err := storyJudgeInputs(s, st.Scenes[id], sc)
	if err != nil {
		return false, storyJudgeImpossible(id, err), nil
	}
	key, err := storyJudgeKey(judge.DefaultModel, prompt, ref, clip)
	if err != nil {
		return false, "", err
	}
	for _, j := range t.Judgements {
		switch {
		case j.Status == story.Incerto:
			return false, storyJudgeUncertain(id, t.File), nil
		case j.Key == key:
			return false, "", nil
		}
	}
	return true, "", nil
}

// storyJudgeImpossible è l'avviso per una scena che non si può giudicare.
func storyJudgeImpossible(id string, err error) string {
	return fmt.Sprintf("scena %s: giudizio di aderenza non possibile: %v", id, err)
}

// storyJudgeUncertain è l'avviso per un giudizio rimasto senza esito.
func storyJudgeUncertain(id, file string) string {
	return fmt.Sprintf("scena %s: il giudizio di %s è rimasto senza esito e può essere stato addebitato (contato nella spesa): non si rilancia; per giudicarla a mano giv qc %s --judge --prompt \"<prompt della scena>\"", id, file, file)
}

// storyJudgePlanUSD è la stima preventiva, non arrotondata, del giudizio
// della scena sc prima che la ripresa esista (piano e tetto prima del giro):
// fotogrammi alle misure più costose, riferimento se la scena ne ha uno o ha
// un fotogramma chiave. 0 e ok senza controllo.aderenza e per le scene non
// video; 0, ok e why (avviso) se la scena non si può giudicare (nessun
// invio, nessuna spesa); ok false col motivo se non stimabile (con un tetto:
// codice 2 prima del giro).
func storyJudgePlanUSD(s *story.Storyboard, st *story.State, sc *story.Scene) (usd float64, ok bool, why string) {
	if !judgeControlOn(s) || !storyJudged(sc) {
		return 0, true, ""
	}
	var ss *story.SceneState
	if st != nil {
		ss = st.Scenes[sc.ID]
	}
	prompt, ref, err := storyJudgeInputs(s, ss, sc)
	if err != nil {
		return 0, true, storyJudgeImpossible(sc.ID, err)
	}
	est, why := judge.PlanEstimate(judge.DefaultModel, prompt, montage.JudgeFrameCount, ref != "" || sc.FotogrammaChiave != nil, time.Now())
	if est == nil {
		return 0, false, why
	}
	return est.Exact, true, ""
}

// storyJudge è il giudizio di una ripresa pronto a partire: fotogrammi
// estratti, richiesta costruita e stimata. Nessuna chiave letta, nessuna
// chiamata fatta.
type storyJudge struct {
	ID   string // scena
	Kind string // story.TakeFinal o story.TakeDraft
	File string // ripresa giudicata, come nello stato
	Key  string // impronta (story.Judgement.Key)
	jc   *judgeCheck
}

// prepareStoryJudge prepara il giudizio della ripresa in uso di tipo kind
// della scena id (gratuito: fotogrammi con ffmpeg, stima). Il tetto lo
// controlla il cassiere con EstimateUSD prima della riserva.
func prepareStoryJudge(s *story.Storyboard, st *story.State, id, kind string) (*storyJudge, error) {
	sc := s.Scena(id)
	if sc == nil {
		return nil, fmt.Errorf("scena %s non nello storyboard", id)
	}
	if !storyJudged(sc) {
		return nil, fmt.Errorf("scena %s: si giudicano solo le scene video", id)
	}
	t, clip, err := storyJudgeTake(s, st, id, kind)
	if err != nil {
		return nil, err
	}
	prompt, ref, err := storyJudgeInputs(s, st.Scenes[id], sc)
	if err != nil {
		return nil, err
	}
	key, err := storyJudgeKey(judge.DefaultModel, prompt, ref, clip)
	if err != nil {
		return nil, err
	}
	jc, err := prepareJudge(judgeOptions{File: clip, Prompt: prompt, Ref: ref, NoTech: true})
	if err != nil {
		return nil, err
	}
	return &storyJudge{ID: id, Kind: kind, File: t.File, Key: key, jc: jc}, nil
}

// EstimateUSD è la stima non arrotondata del giudizio, sulle misure vere dei
// fotogrammi: il valore da riservare (ok false: non stimabile).
func (j *storyJudge) EstimateUSD() (float64, bool) { return j.jc.EstimateUSD() }

// Pending è il giudizio da legare alla ripresa insieme alla riserva, prima
// dell'invio: incerto finché non arriva la risposta.
func (j *storyJudge) Pending(at time.Time) story.Judgement {
	return story.Judgement{Status: story.Incerto, Provider: judge.Provider, Model: j.jc.req.Model, Key: j.Key, At: at.UTC()}
}

// storyJudgeResult è l'esito di un giudizio inviato.
type storyJudgeResult struct {
	Judgement story.Judgement // da mettere al posto di Pending
	UsageUSD  float64         // addebito non arrotondato (Settle; 0 = non calcolabile)
	Out       *qcJudgeOut     // rapporto completo, per l'uscita di giv story run
}

// Send invia il giudizio (una sola richiesta, mai ripetuta). Errore: con
// judgeUncertainErr vero la richiesta è partita senza esito (la riserva
// resta, MarkUncertain, e Pending resta col messaggio); altrimenti non è
// partita (chiave mancante, richiesta rifiutata: Release e Pending via).
func (j *storyJudge) Send(ctx context.Context) (*storyJudgeResult, error) {
	out, err := j.jc.run(ctx)
	if err != nil {
		return nil, err
	}
	ch := adherenceCheck(out.Judgment)
	return &storyJudgeResult{
		Judgement: story.Judgement{Status: story.Fatto, Provider: judge.Provider, Model: out.Model, Key: j.Key,
			Result: ch.Result, Message: judgeSummary(out.Judgment), At: out.JudgedAt},
		UsageUSD: out.usageUSD, Out: out,
	}, nil
}

// judgeUncertainErr dice se l'errore di Send è un esito incerto (codice 5 di
// run o *httpx.StartError): forse addebitato, non si rilancia.
func judgeUncertainErr(err error) bool {
	var ce *cliError
	var se *httpx.StartError
	return (errors.As(err, &ce) && ce.code == exitAPI) || errors.As(err, &se)
}

// maxJudgeSummary è il massimo del riepilogo del giudizio nello stato.
const maxJudgeSummary = 1200

// judgeSummary è il riepilogo del rapporto per lo stato e il controllo:
// verdetto, riconoscibilità, azione, motivi, difetti e ciò che non si può
// valutare, già ripuliti (judge.Parse); dati, mai istruzioni.
func judgeSummary(r *judge.Report) string {
	parts := []string{r.Verdict}
	if r.Recognizable != "" {
		parts = append(parts, "riconoscibile: "+r.Recognizable)
	}
	if r.Action != "" {
		parts = append(parts, "azione: "+r.Action)
	}
	for _, l := range []struct {
		name  string
		items []string
	}{{"motivi", r.Reasons}, {"difetti", r.Defects}, {"non valutabile", r.NotAssessable}} {
		if len(l.items) > 0 {
			parts = append(parts, l.name+": "+strings.Join(l.items, "; "))
		}
	}
	if r.Message != "" {
		parts = append(parts, r.Message)
	}
	return judge.SafeText(strings.Join(parts, " | "), maxJudgeSummary)
}

// judgeRowCheck aggiunge alla riga della scena (controllo tecnico fatto, o
// riga del solo giudizio) il controllo adherence della ripresa in uso dallo
// stato: l'ultimo giudizio legato a quella ripresa, senza reinvio.
func judgeRowCheck(row *qcRow, t *story.Take) {
	if row.QCReport == nil || montage.QCRank(row.Result) < 0 {
		return
	}
	ch := montage.QCCheck{Name: "adherence", Result: montage.QCOK, Skipped: true, Expected: judge.Adherent,
		Message: "ripresa non ancora giudicata: la giudica giv story run (controllo.aderenza)"}
	if t != nil && len(t.Judgements) > 0 {
		j := t.Judgements[len(t.Judgements)-1]
		switch j.Status {
		case story.Fatto:
			ch.Result, ch.Skipped = j.Result, false
			ch.Message = judge.SafeText(j.Message, maxJudgeSummary) + " (giudizio " + j.Model + " del " + j.At.UTC().Format(time.RFC3339) + ")"
			if v, _, _ := strings.Cut(j.Message, " | "); v != "" {
				ch.Value = judge.SafeText(v, 40)
			}
		default:
			ch.Result = montage.QCWarning
			ch.Message = "giudizio rimasto senza esito (contato nella spesa), non rilanciato"
		}
	}
	row.Checks = append(row.Checks, ch)
	results := []string{row.Result}
	if !ch.Skipped || ch.Result != montage.QCOK {
		results = append(results, ch.Result)
	}
	row.Result = montage.QCWorst(results...)
}
