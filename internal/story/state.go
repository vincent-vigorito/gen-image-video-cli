package story

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gen-image-video-cli/internal/speech"
)

// StateVersion è la versione del formato del file di stato.
const StateVersion = 1

// Stati di una scena nel file di stato.
const (
	Fatto   = "fatto"   // clip scaricata
	Partito = "partito" // job avviato, risultato non ancora scaricato: si recupera, non si rilancia
	Fallito = "fallito" // job accettato dal provider e finito male (o senza video): spesa contata, si rifà solo con --scene
	Incerto = "incerto" // richiesta di generazione inviata senza esito: forse addebitata, si rifà solo con --scene
	DaFare  = "da_fare" // niente in corso né addebitato senza clip (es. fotogramma chiave fatto, avvio rifiutato): si riprova
)

// Tipi di generazione pagata.
const (
	ChargeClip     = "clip" // video definitivo (anche ogni clip degli stati 0.12)
	ChargeKeyframe = "fotogramma"
	ChargeVoice    = "voce" // voce fuori campo dal testo della scena
	// trascrizione della voce generata (controllo.voce), della scena come la voce
	ChargeTranscribe = "trascrizione"
	ChargeDraft      = "bozza"    // video di bozza (default.bozza)
	ChargeJudge      = "giudizio" // giudizio di aderenza di una ripresa (controllo.aderenza)
)

// chargeKinds sono i tipi di Charge.Kind ammessi nel file di stato.
var chargeKinds = []string{ChargeClip, ChargeKeyframe, ChargeVoice, ChargeTranscribe, ChargeDraft, ChargeJudge}

// Tipi di ripresa di una scena: ognuna con file, job, spesa e giudizi propri.
// Le riprese degli stati 0.12 sono definitive.
const (
	TakeKeyframe = ChargeKeyframe // fotogramma chiave (SceneState.Keyframe)
	TakeDraft    = "bozza"        // bozza video (SceneState.Draft), in DraftDir
	TakeFinal    = "definitivo"   // video definitivo (SceneState.Track)
)

// DraftDir è la cartella delle bozze video, relativa a quella dello
// storyboard: distinta da quella dei definitivi.
const DraftDir = "clip/bozza"

// Stati di una riserva (Charge.Status). Ogni richiesta pagata è una sola voce
// di Charges, scritta nello stato prima dell'invio: l'addebito prende il posto
// della riserva nella stessa voce (Settle), così niente si conta due volte.
// Vuoto: voce registrata (risposta arrivata o job accettato), come tutte le
// voci degli stati 0.12.
const (
	ChargeOpen      = "aperta"  // riserva scritta prima dell'invio, esito non ancora noto; dopo un arresto vale come incerta
	ChargeUncertain = "incerta" // invio senza esito: forse addebitata, resta riservata; nessun nuovo invio senza scelta esplicita
)

// OfVoice: l'addebito è della voce della scena (la voce o la sua
// trascrizione), non della sua clip.
func (c Charge) OfVoice() bool {
	return c.Kind == ChargeVoice || c.Kind == ChargeTranscribe
}

// TakeJob è il kind, in previous, di un job partito lasciato con --new-job:
// solo id ed errore, senza file (il suo addebito resta in charges).
const TakeJob = "job"

// TakeVoice è il kind, in previous, di una voce sostituita con --new-voice.
const TakeVoice = "voce"

// State è lo stato di giv story run, nel file accanto allo storyboard
// (StatePath): per ogni scena cosa è fatto, quale job è partito e quanto è
// stato speso. Il tetto di spesa conta gli addebiti registrati qui: togliendo
// il file il conto riparte da zero.
type State struct {
	Version int                    `json:"version"`
	Scenes  map[string]*SceneState `json:"scenes"`

	path string // file accanto allo storyboard
}

// SceneState è lo stato di una scena: generazione della clip (scene video e
// avatar) e voce dal testo (scene con testo, di ogni tipo). Una scena con la
// sola voce ha status vuoto. I campi della 0.12 (status, clip, job, error,
// previous) sono la ripresa definitiva (Track); bozze e approvazione si
// aggiungono solo quando servono.
type SceneState struct {
	// definitivo: status, clip in uso, job avviato e non ancora scaricato,
	// ultimo errore e, in previous, riprese, fotogrammi chiave, voci e job
	// sostituiti (i file restano su disco)
	Track
	Keyframe *Take    `json:"keyframe,omitempty"` // fotogramma chiave, riusato alle riprese successive
	Charges  []Charge `json:"charges,omitempty"`  // generazioni pagate o forse pagate, di ogni tipo di ripresa

	// voce dal testo della scena (giv story run): file in voce/, impronta di
	// testo e impostazioni con cui è stata fatta, stato della richiesta
	Voice       *Take  `json:"voice,omitempty"`
	VoiceKey    string `json:"voice_key,omitempty"`
	VoiceStatus string `json:"voice_status,omitempty"` // fatto, incerto, da_fare; vuoto: mai chiesta
	VoiceError  string `json:"voice_error,omitempty"`
	// controllo della voce generata (controllo.voce): l'ultima trascrizione
	VoiceCheck *VoiceCheck `json:"voice_check,omitempty"`

	// Draft sono le bozze video della scena (default.bozza), con stato, job e
	// riprese propri; la spesa è in Charges col tipo ChargeDraft.
	Draft *Track `json:"draft,omitempty"`
	// Approval è l'approvazione della scena (approvazione nello storyboard):
	// vale finché CheckApproval non dice il contrario.
	Approval *Approval `json:"approval,omitempty"`
}

// Track è una serie di riprese video di una scena (definitivo o bozza):
// stato, ripresa in uso, job avviato e non ancora scaricato, ultimo errore e
// riprese e job sostituiti.
type Track struct {
	Status   string `json:"status"` // fatto, partito, fallito, incerto, da_fare
	Clip     *Take  `json:"clip,omitempty"`
	Job      *Job   `json:"job,omitempty"`
	Error    string `json:"error,omitempty"`
	Previous []Take `json:"previous,omitempty"`
}

// TrackOf è la serie di riprese video di tipo kind (TakeFinal o TakeDraft),
// quella delle bozze creata se manca; nil per un altro tipo.
func (sc *SceneState) TrackOf(kind string) *Track {
	switch kind {
	case TakeFinal:
		return &sc.Track
	case TakeDraft:
		if sc.Draft == nil {
			sc.Draft = &Track{Status: DaFare}
		}
		return sc.Draft
	}
	return nil
}

// Current è la ripresa in uso di tipo kind (TakeKeyframe, TakeDraft,
// TakeFinal); nil se non c'è.
func (sc *SceneState) Current(kind string) *Take {
	switch kind {
	case TakeKeyframe:
		return sc.Keyframe
	case TakeFinal:
		return sc.Clip
	case TakeDraft:
		if sc.Draft != nil {
			return sc.Draft.Clip
		}
	}
	return nil
}

// TakeByFile è la ripresa della scena (in uso o sostituita, di ogni tipo) col
// file indicato, per legarle un giudizio; nil se non c'è. Il puntatore vale
// finché non cambiano le riprese della scena.
func (sc *SceneState) TakeByFile(file string) *Take {
	if file == "" {
		return nil
	}
	for _, t := range []*Take{sc.Clip, sc.Keyframe, sc.Voice} {
		if t != nil && t.File == file {
			return t
		}
	}
	if d := sc.Draft; d != nil {
		if d.Clip != nil && d.Clip.File == file {
			return d.Clip
		}
		for i := range d.Previous {
			if d.Previous[i].File == file {
				return &d.Previous[i]
			}
		}
	}
	for i := range sc.Previous {
		if sc.Previous[i].File == file {
			return &sc.Previous[i]
		}
	}
	return nil
}

// VoiceCheck è la trascrizione di una voce generata, confrontata col testo
// della scena (controllo.voce). Una trascrizione incerta (forse addebitata)
// non si rilancia: la voce si controlla di nuovo solo se cambia il file
// (--new-voice) o a mano con giv qc --voice.
type VoiceCheck struct {
	File    string    `json:"file"`              // voce trascritta, relativa alla cartella dello storyboard
	Status  string    `json:"status"`            // fatto o incerto
	Result  string    `json:"result,omitempty"`  // fatto: ok, avviso o difetto (parole e lingua)
	Message string    `json:"message,omitempty"` // esito del confronto, o errore della trascrizione incerta
	At      time.Time `json:"at"`
	// Comparison è il riepilogo del confronto col testo (fatto; assente negli
	// stati della 0.12.0): speech.Comparison.Summary, senza testi lunghi
	Comparison *speech.Comparison `json:"comparison,omitempty"`
}

// Esiti di VoiceCheck.Result (gli stessi di giv qc).
var voiceCheckResults = []string{"ok", "avviso", "difetto"}

// VoiceTakes sono le voci generate della scena: quelle sostituite più quella in uso.
func (sc *SceneState) VoiceTakes() int {
	n := 0
	for _, t := range sc.Previous {
		if t.Kind == TakeVoice {
			n++
		}
	}
	if sc.Voice != nil {
		n++
	}
	return n
}

// Take è un file generato, relativo alla cartella dello storyboard.
type Take struct {
	Kind  string    `json:"kind,omitempty"` // in previous: "fotogramma" per un fotogramma chiave sostituito, "job" per un job lasciato, "voce" per una voce sostituita, vuoto per una clip
	File  string    `json:"file,omitempty"` // sempre, tranne che per un job lasciato
	JobID string    `json:"job_id,omitempty"`
	Error string    `json:"error,omitempty"` // job lasciato: ultimo errore del recupero
	Done  time.Time `json:"done"`
	// Judgements sono i giudizi di aderenza di questa ripresa
	// (controllo.aderenza): restano con la ripresa anche quando passa fra le
	// precedenti; nessun giudizio rigenera una ripresa.
	Judgements []Judgement `json:"judgements,omitempty"`
}

// Judgement è un giudizio di aderenza di una ripresa (giv qc --judge sui suoi
// fotogrammi, con la richiesta della scena). Un giudizio incerto (inviato
// senza esito, forse addebitato) non si rilancia da solo. La spesa è in
// Charges col tipo ChargeJudge.
type Judgement struct {
	Status   string    `json:"status"`            // fatto o incerto
	Provider string    `json:"provider"`          // destinatario (gemini)
	Model    string    `json:"model,omitempty"`   // modello del giudizio
	Key      string    `json:"key,omitempty"`     // impronta (esadecimale) di richiesta, riferimenti e fotogrammi inviati
	Result   string    `json:"result,omitempty"`  // fatto: ok, avviso o difetto (come giv qc)
	Report   string    `json:"report,omitempty"`  // rapporto completo, relativo alla cartella dello storyboard
	Message  string    `json:"message,omitempty"` // riepilogo, o errore del giudizio incerto: dati, mai istruzioni
	At       time.Time `json:"at"`
}

// judgementResults sono gli esiti di Judgement.Result (gli stessi di giv qc).
var judgementResults = voiceCheckResults

// ClipTakes sono le riprese video della scena: quelle sostituite più quella in uso.
func (sc *SceneState) ClipTakes() int {
	n := 0
	for _, t := range sc.Previous {
		if t.Kind == "" {
			n++
		}
	}
	if sc.Clip != nil {
		n++
	}
	return n
}

// Job è un job video avviato: quanto serve per recuperarlo senza un nuovo avvio.
type Job struct {
	Provider   string    `json:"provider"`
	ID         string    `json:"id"`
	PollingURL string    `json:"polling_url,omitempty"`
	Model      string    `json:"model,omitempty"`
	Started    time.Time `json:"started"`
}

// Charge è una generazione avviata: per il tetto conta l'addebito riportato
// dal provider, altrimenti la stima, altrimenti il costo dichiarato.
type Charge struct {
	Kind            string    `json:"kind"` // clip, fotogramma, voce, trascrizione, bozza o giudizio
	JobID           string    `json:"job_id,omitempty"`
	CostEstUSD      *float64  `json:"cost_est_usd,omitempty"`
	CostDeclaredUSD *float64  `json:"cost_declared_usd,omitempty"`
	CostUSD         float64   `json:"cost_usd,omitempty"` // addebito riportato dal provider
	At              time.Time `json:"at"`
	// Status è lo stato della riserva: aperta, incerta, o vuoto per una voce
	// registrata (ChargeOpen, ChargeUncertain)
	Status string `json:"status,omitempty"`
}

// Reserve aggiunge ch alle generazioni della scena come riserva aperta e ne
// restituisce l'indice in Charges: va salvata (Save) prima dell'invio, e
// conta per il tetto la sua stima (o il costo dichiarato) finché non arriva
// l'addebito.
func (sc *SceneState) Reserve(ch Charge) int {
	ch.Status = ChargeOpen
	if ch.At.IsZero() {
		ch.At = time.Now()
	}
	sc.Charges = append(sc.Charges, ch)
	return len(sc.Charges) - 1
}

// Settle registra la riserva k: risposta arrivata o job accettato. Un
// addebito riportato (costUSD oltre 0) prende il posto della stima nella
// stessa voce; 0 (non riportato) lascia contare la stima.
func (sc *SceneState) Settle(k int, costUSD float64) {
	c := &sc.Charges[k]
	c.Status = ""
	if costUSD > 0 {
		c.CostUSD = costUSD
	}
}

// MarkUncertain: l'invio della riserva k è senza esito (forse addebitato): la
// riserva resta e la richiesta non si rifà senza una scelta esplicita.
func (sc *SceneState) MarkUncertain(k int) {
	sc.Charges[k].Status = ChargeUncertain
}

// Release toglie la riserva k: richiesta rifiutata o mai partita (anche un
// caricamento fallito prima del job), nessun addebito. Gli indici delle voci
// dopo k scalano di uno.
func (sc *SceneState) Release(k int) {
	sc.Charges = slices.Delete(sc.Charges, k, k+1)
}

// Ledger è il conto per il tetto: ogni richiesta pagata è una sola voce di
// Charges, contata una volta, a piena precisione (niente arrotondamenti).
type Ledger struct {
	ChargedUSD   float64 `json:"charged_usd"`   // addebiti riportati dai provider
	EstimatedUSD float64 `json:"estimated_usd"` // voci registrate senza addebito riportato: stima o costo dichiarato
	ReservedUSD  float64 `json:"reserved_usd"`  // riserve aperte o incerte senza addebito riportato
	TotalUSD     float64 `json:"total_usd"`     // la somma voce per voce: quello che conta per il tetto (Spent)
}

func (l *Ledger) add(c Charge) {
	v := c.Spent()
	switch {
	case c.CostUSD > 0:
		l.ChargedUSD += v
	case c.Status != "":
		l.ReservedUSD += v
	default:
		l.EstimatedUSD += v
	}
	l.TotalUSD += v
}

func (l *Ledger) merge(o Ledger) {
	l.ChargedUSD += o.ChargedUSD
	l.EstimatedUSD += o.EstimatedUSD
	l.ReservedUSD += o.ReservedUSD
	l.TotalUSD += o.TotalUSD
}

// Ledger è il conto della scena: tutte le sue voci, di ogni tipo di ripresa
// (riprese precedenti, bozze superate, giudizi e job falliti compresi).
func (sc *SceneState) Ledger() Ledger {
	var l Ledger
	for _, c := range sc.Charges {
		l.add(c)
	}
	return l
}

// Ledger è il conto di tutte le scene dello stato, anche di quelle non più
// nello storyboard: addebiti registrati più riserve aperte e incerte.
func (st *State) Ledger() Ledger {
	var l Ledger
	for _, id := range sortedKeys(st.Scenes) {
		if sc := st.Scenes[id]; sc != nil {
			l.merge(sc.Ledger())
		}
	}
	return l
}

// SpentOn è la spesa della scena nelle generazioni dei tipi kinds (Charge.Kind).
func (sc *SceneState) SpentOn(kinds ...string) float64 {
	var total float64
	for _, c := range sc.Charges {
		if slices.Contains(kinds, c.Kind) {
			total += c.Spent()
		}
	}
	return total
}

// Spent è quanto conta la generazione per il tetto.
func (c Charge) Spent() float64 {
	switch {
	case c.CostUSD > 0:
		return c.CostUSD
	case c.CostEstUSD != nil:
		return *c.CostEstUSD
	case c.CostDeclaredUSD != nil:
		return *c.CostDeclaredUSD
	}
	return 0
}

// Spent è la spesa della scena (riprese precedenti e job falliti compresi):
// Ledger().TotalUSD.
func (sc *SceneState) Spent() float64 {
	return sc.Ledger().TotalUSD
}

// Spent è la spesa di tutte le scene (riprese precedenti, job falliti e scene
// tolte compresi), riserve aperte e incerte comprese: Ledger().TotalUSD.
func (st *State) Spent() float64 {
	return st.Ledger().TotalUSD
}

// Scene restituisce lo stato della scena id, creandolo se manca.
func (st *State) Scene(id string) *SceneState {
	if st.Scenes == nil {
		st.Scenes = map[string]*SceneState{}
	}
	sc, ok := st.Scenes[id]
	if !ok {
		sc = &SceneState{}
		st.Scenes[id] = sc
	}
	return sc
}

// Path è il percorso del file di stato accanto allo storyboard.
func (st *State) Path() string { return st.path }

// StatePath è il file di stato dello storyboard: spot.json -> spot.stato.json.
func StatePath(storyboard string) string {
	return strings.TrimSuffix(storyboard, filepath.Ext(storyboard)) + ".stato.json"
}

// LoadState legge il file di stato in path (vuoto se non esiste). Un file
// illeggibile, non regolare o con valori non validi è un errore: nessuna scena
// si avvia.
func LoadState(path string) (*State, error) {
	st, err := readState(path)
	if errors.Is(err, os.ErrNotExist) {
		return newState(path), nil
	}
	return st, err
}

func newState(path string) *State {
	return &State{Version: StateVersion, Scenes: map[string]*SceneState{}, path: path}
}

// readState legge e controlla un file di stato; os.ErrNotExist se manca.
func readState(path string) (*State, error) {
	if fi, err := os.Lstat(path); err != nil {
		return nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("file di stato %s: non è un file regolare (link o altro): rifiutato", path)
	}
	data, err := readLimited(path)
	if err != nil {
		return nil, err
	}
	// un file che c'è è un oggetto con version e scenes espliciti: null, un
	// oggetto vuoto o senza scene non valgono come stato nuovo
	if t := bytes.TrimSpace(data); len(t) == 0 || t[0] != '{' {
		return nil, fmt.Errorf("file di stato %s: non è un oggetto JSON", path)
	}
	st := &State{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(st); err != nil {
		return nil, fmt.Errorf("file di stato %s: %w", path, jsonError(data, err))
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("file di stato %s: altro testo dopo lo stato", path)
	}
	if err := st.validate(); err != nil {
		return nil, fmt.Errorf("file di stato %s non valido (correggilo o toglilo: le scene fatte si rifarebbero e la spesa ripartirebbe da zero):\n%w", path, err)
	}
	st.path = path
	return st, nil
}

// OpenRunState apre lo stato di giv story run per lo storyboard, bloccandolo:
// OpenLockedState(storyboard, "run").
func OpenRunState(storyboard string) (st *State, unlock func(), err error) {
	return OpenLockedState(storyboard, "run")
}

// OpenLockedState apre lo stato dello storyboard per il comando che lo scrive
// (run, approve), bloccandolo: un solo comando alla volta per storyboard, con
// lo stesso blocco (<nome>.stato.lock). Il file accanto allo storyboard,
// vuoto se non c'è ancora; unlock toglie il blocco.
func OpenLockedState(storyboard, command string) (st *State, unlock func(), err error) {
	path := StatePath(storyboard)
	release, err := lockFile(strings.TrimSuffix(path, ".json")+".lock", storyboard, command)
	if err != nil {
		return nil, nil, err
	}
	if st, err = LoadState(path); err != nil {
		release()
		return nil, nil, err
	}
	return st, release, nil
}

func (st *State) validate() error {
	if st.Version != StateVersion {
		return fmt.Errorf("versione %d mancante o non riconosciuta (attesa %d)", st.Version, StateVersion)
	}
	if st.Scenes == nil {
		return errors.New("scenes mancante")
	}
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	takeOK := func(where string, t *Take, kinds ...string) {
		if t == nil {
			return
		}
		if t.Kind != "" && !slices.Contains(kinds, t.Kind) {
			fail("%s: kind %q non valido", where, t.Kind)
		}
		if t.Kind == TakeJob {
			if t.File != "" || !ValidJobID(t.JobID) {
				fail("%s: job lasciato con file o senza job_id valido", where)
			}
			return
		}
		if t.Error != "" {
			fail("%s: error vale solo per un job lasciato", where)
		}
		if err := localPath(t.File); t.File == "" || err != nil {
			fail("%s: file %q non valido", where, t.File)
		}
		if t.JobID != "" && !ValidJobID(t.JobID) {
			fail("%s: job_id non valido", where)
		}
		for i, j := range t.Judgements {
			if err := j.check(); err != nil {
				fail("%s judgements n. %d: %v", where, i+1, err)
			}
		}
	}
	trackOK := func(where string, t *Track, previous ...string) {
		switch t.Status {
		case Fatto:
			if t.Clip == nil {
				fail("%s: fatta senza clip", where)
			}
		case Partito:
			if t.Job == nil {
				fail("%s: partita senza job", where)
			}
		case Fallito, Incerto, DaFare:
		default:
			fail("%s: stato %q sconosciuto (fatto, partito, fallito, incerto, da_fare)", where, t.Status)
		}
		takeOK(where+" clip", t.Clip)
		for i := range t.Previous {
			takeOK(fmt.Sprintf("%s previous n. %d", where, i+1), &t.Previous[i], previous...)
		}
		if j := t.Job; j != nil {
			if j.Provider == "" || !ValidJobID(j.ID) {
				fail("%s: job senza provider o con id non valido", where)
			}
		}
	}
	for _, id := range sortedKeys(st.Scenes) {
		sc := st.Scenes[id]
		where := "scena " + strconv.Quote(id)
		if !idRe.MatchString(id) {
			fail("%s: id non valido", where)
			continue
		}
		if sc == nil {
			fail("%s: stato vuoto", where)
			continue
		}
		switch sc.Status {
		case Fatto:
			if sc.Clip == nil {
				fail("%s: fatta senza clip", where)
			}
		case Partito:
			if sc.Job == nil {
				fail("%s: partita senza job", where)
			}
		case Fallito, Incerto, DaFare:
		case "":
			// solo voce: nessuna generazione della clip
			if sc.Clip != nil || sc.Job != nil || sc.Keyframe != nil {
				fail("%s: status mancante", where)
			}
		default:
			fail("%s: stato %q sconosciuto (fatto, partito, fallito, incerto, da_fare)", where, sc.Status)
		}
		switch sc.VoiceStatus {
		case Fatto:
			if sc.Voice == nil {
				fail("%s: voce fatta senza file", where)
			}
		case "", Incerto, DaFare:
		default:
			fail("%s: voice_status %q sconosciuto (fatto, incerto, da_fare)", where, sc.VoiceStatus)
		}
		if k := sc.VoiceKey; k != "" && !voiceKeyRe.MatchString(k) {
			fail("%s: voice_key non valida", where)
		}
		if vc := sc.VoiceCheck; vc != nil {
			if err := localPath(vc.File); vc.File == "" || err != nil {
				fail("%s voice_check: file %q non valido", where, vc.File)
			}
			switch vc.Status {
			case Fatto:
				if !slices.Contains(voiceCheckResults, vc.Result) {
					fail("%s voice_check: result %q sconosciuto (ok, avviso, difetto)", where, vc.Result)
				}
			case Incerto:
				if vc.Result != "" {
					fail("%s voice_check: result vale solo per una trascrizione fatta", where)
				}
				if vc.Comparison != nil {
					fail("%s voice_check: comparison vale solo per una trascrizione fatta", where)
				}
			default:
				fail("%s voice_check: status %q sconosciuto (fatto, incerto)", where, vc.Status)
			}
			if c := vc.Comparison; c != nil {
				if err := c.CheckSummary(); err != nil {
					fail("%s voice_check comparison: %v", where, err)
				}
			}
		}
		takeOK(where+" clip", sc.Clip)
		takeOK(where+" keyframe", sc.Keyframe)
		takeOK(where+" voice", sc.Voice)
		for i := range sc.Previous {
			takeOK(fmt.Sprintf("%s previous n. %d", where, i+1), &sc.Previous[i], ChargeKeyframe, TakeJob, TakeVoice)
		}
		if j := sc.Job; j != nil {
			if j.Provider == "" || !ValidJobID(j.ID) {
				fail("%s: job senza provider o con id non valido", where)
			}
		}
		if d := sc.Draft; d != nil {
			// bozze: solo clip e job lasciati, mai fotogrammi o voci
			trackOK(where+" draft", d, TakeJob)
		}
		if a := sc.Approval; a != nil {
			if err := a.check(); err != nil {
				fail("%s approval: %v", where, err)
			}
		}
		for i, c := range sc.Charges {
			w := fmt.Sprintf("%s charges n. %d", where, i+1)
			if !slices.Contains(chargeKinds, c.Kind) {
				fail("%s: kind %q sconosciuto", w, c.Kind)
			}
			switch c.Status {
			case "", ChargeOpen, ChargeUncertain:
			default:
				fail("%s: status %q sconosciuto (aperta, incerta)", w, c.Status)
			}
			for _, v := range []*float64{c.CostEstUSD, c.CostDeclaredUSD, &c.CostUSD} {
				if v != nil && !nonNegative(*v) {
					fail("%s: importo non valido", w)
					break
				}
			}
		}
	}
	return errors.Join(errs...)
}

// check controlla un giudizio letto dallo stato.
func (j Judgement) check() error {
	var errs []error
	switch j.Status {
	case Fatto:
		if !slices.Contains(judgementResults, j.Result) {
			errs = append(errs, fmt.Errorf("result %q sconosciuto (ok, avviso, difetto)", j.Result))
		}
	case Incerto:
		if j.Result != "" {
			errs = append(errs, errors.New("result vale solo per un giudizio fatto"))
		}
	default:
		errs = append(errs, fmt.Errorf("status %q sconosciuto (fatto, incerto)", j.Status))
	}
	if !nameRe.MatchString(j.Provider) {
		errs = append(errs, errors.New("provider mancante o non valido"))
	}
	if j.Model != "" && !ValidJobID(j.Model) {
		errs = append(errs, errors.New("model non valido"))
	}
	if j.Key != "" && !voiceKeyRe.MatchString(j.Key) {
		errs = append(errs, errors.New("key non valida"))
	}
	if j.Report != "" {
		if err := localPath(j.Report); err != nil {
			errs = append(errs, fmt.Errorf("report %q non valido", j.Report))
		}
	}
	return errors.Join(errs...)
}

// nameRe è il nome di un provider nello stato: minuscole, cifre e trattini.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// voiceKeyRe è l'impronta della voce: esadecimale, da 8 a 64 cifre.
var voiceKeyRe = regexp.MustCompile(`^[0-9a-f]{8,64}$`)

// ValidJobID: da 1 a 256 caratteri ASCII stampabili, senza spazi (come giv job fetch).
func ValidJobID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r >= 0x7f {
			return false
		}
	}
	return true
}

// Save scrive lo stato in modo atomico e durevole: un errore vuol dire che
// nessuna nuova richiesta deve partire.
func (st *State) Save() error {
	data, err := st.marshal()
	if err != nil {
		return err
	}
	if err := writeAtomic(st.path, data); err != nil {
		return fmt.Errorf("file di stato %s: %w", st.path, err)
	}
	return nil
}

func (st *State) marshal() ([]byte, error) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// writeAtomic: file temporaneo nella stessa cartella, sync e rename (che
// sostituisce la voce senza seguire link), poi sync della cartella perché il
// rename resti anche dopo uno spegnimento; leggibile solo dall'utente.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // dopo il rename non c'è più
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// ErrLocked: un'altra esecuzione usa lo stesso storyboard.
var ErrLocked = errors.New("storyboard in uso")

// lockFile impedisce due comandi che scrivono lo stato (giv story run,
// approve) sullo stesso storyboard: crea lock in modo esclusivo accanto allo
// stato, col pid e il comando; unlock lo toglie.
func lockFile(lock, storyboard, command string) (unlock func(), err error) {
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("%w: c'è %s (un altro giv story run o approve su %s in corso; se non è così, toglilo)", ErrLocked, lock, storyboard)
	}
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(f, "%d %s\n", os.Getpid(), command)
	f.Close()
	return func() { os.Remove(lock) }, nil
}

// CheckFile controlla un file prodotto o letto da giv story (relativo alla
// cartella dello storyboard, dentro anche seguendo i link, esistente) e ne
// restituisce il percorso rispetto alla cartella corrente.
func (s *Storyboard) CheckFile(p string) (string, error) {
	if err := s.checkPath(p, false); err != nil {
		return "", err
	}
	return s.Path(p), nil
}

// Rel è il percorso di un file sotto la cartella dello storyboard, nella forma
// del file di stato.
func (s *Storyboard) Rel(path string) (string, error) {
	rel, err := filepath.Rel(s.Dir, path)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s fuori dalla cartella dello storyboard", path)
	}
	return filepath.ToSlash(rel), nil
}

// ErrNotDone: la scena non ha ancora una clip.
var ErrNotDone = errors.New("scena non ancora generata")

// ClipFile è la clip in uso della scena video id (per giv story cut e
// continua_da), rispetto alla cartella corrente e controllata come i file
// dello storyboard; ErrNotDone se la scena non ha ancora una clip. Una nuova
// ripresa in corso (stato partito o fallito) lascia in uso la precedente.
func (st *State) ClipFile(s *Storyboard, id string) (string, error) {
	sc := st.Scenes[id]
	if sc == nil || sc.Clip == nil {
		return "", fmt.Errorf("scena %s: %w", id, ErrNotDone)
	}
	p, err := s.CheckFile(sc.Clip.File)
	if err != nil {
		return "", fmt.Errorf("scena %s: clip %w", id, err)
	}
	return p, nil
}

// VoiceFile è la voce generata in uso della scena id (per giv story cut),
// rispetto alla cartella corrente e controllata come i file dello storyboard;
// ErrNotDone se la scena non ha ancora una voce.
func (st *State) VoiceFile(s *Storyboard, id string) (string, error) {
	sc := st.Scenes[id]
	if sc == nil || sc.Voice == nil {
		return "", fmt.Errorf("scena %s: voce: %w", id, ErrNotDone)
	}
	p, err := s.CheckFile(sc.Voice.File)
	if err != nil {
		return "", fmt.Errorf("scena %s: voce %w", id, err)
	}
	return p, nil
}
