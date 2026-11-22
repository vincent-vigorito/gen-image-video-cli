package story

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
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
	ChargeClip     = "clip"
	ChargeKeyframe = "fotogramma"
)

// TakeJob è il kind, in previous, di un job partito lasciato con --new-job:
// solo id ed errore, senza file (il suo addebito resta in charges).
const TakeJob = "job"

// State è lo stato di giv story run, nel file accanto allo storyboard
// (StatePath): per ogni scena cosa è fatto, quale job è partito e quanto è
// stato speso. Il tetto di spesa conta gli addebiti registrati qui: togliendo
// il file il conto riparte da zero.
type State struct {
	Version int                    `json:"version"`
	Scenes  map[string]*SceneState `json:"scenes"`

	path string // file accanto allo storyboard
}

// SceneState è lo stato di una scena video.
type SceneState struct {
	Status   string   `json:"status"`
	Clip     *Take    `json:"clip,omitempty"`     // ripresa in uso
	Keyframe *Take    `json:"keyframe,omitempty"` // fotogramma chiave, riusato alle riprese successive
	Job      *Job     `json:"job,omitempty"`      // job avviato e non ancora scaricato
	Error    string   `json:"error,omitempty"`    // ultimo errore
	Previous []Take   `json:"previous,omitempty"` // riprese, fotogrammi chiave e job sostituiti: i file restano su disco
	Charges  []Charge `json:"charges,omitempty"`  // generazioni pagate o forse pagate
}

// Take è un file generato, relativo alla cartella dello storyboard.
type Take struct {
	Kind  string    `json:"kind,omitempty"` // in previous: "fotogramma" per un fotogramma chiave sostituito, "job" per un job lasciato, vuoto per una clip
	File  string    `json:"file,omitempty"` // sempre, tranne che per un job lasciato
	JobID string    `json:"job_id,omitempty"`
	Error string    `json:"error,omitempty"` // job lasciato: ultimo errore del recupero
	Done  time.Time `json:"done"`
}

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
	Kind            string    `json:"kind"` // clip o fotogramma
	JobID           string    `json:"job_id,omitempty"`
	CostEstUSD      *float64  `json:"cost_est_usd,omitempty"`
	CostDeclaredUSD *float64  `json:"cost_declared_usd,omitempty"`
	CostUSD         float64   `json:"cost_usd,omitempty"` // addebito riportato dal provider
	At              time.Time `json:"at"`
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

// Spent è la spesa della scena (riprese precedenti e job falliti compresi).
func (sc *SceneState) Spent() float64 {
	var total float64
	for _, c := range sc.Charges {
		total += c.Spent()
	}
	return total
}

// Spent è la spesa di tutte le scene (riprese precedenti e job falliti compresi).
func (st *State) Spent() float64 {
	var total float64
	for _, sc := range st.Scenes {
		total += sc.Spent()
	}
	return total
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

// OpenRunState apre lo stato di giv story run per lo storyboard, bloccandolo
// (una sola esecuzione per storyboard): il file accanto allo storyboard, vuoto
// se non c'è ancora. unlock toglie il blocco.
func OpenRunState(storyboard string) (st *State, unlock func(), err error) {
	path := StatePath(storyboard)
	release, err := lockFile(strings.TrimSuffix(path, ".json")+".lock", storyboard)
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
		default:
			fail("%s: stato %q sconosciuto (fatto, partito, fallito, incerto, da_fare)", where, sc.Status)
		}
		takeOK(where+" clip", sc.Clip)
		takeOK(where+" keyframe", sc.Keyframe)
		for i := range sc.Previous {
			takeOK(fmt.Sprintf("%s previous n. %d", where, i+1), &sc.Previous[i], ChargeKeyframe, TakeJob)
		}
		if j := sc.Job; j != nil {
			if j.Provider == "" || !ValidJobID(j.ID) {
				fail("%s: job senza provider o con id non valido", where)
			}
		}
		for i, c := range sc.Charges {
			w := fmt.Sprintf("%s charges n. %d", where, i+1)
			if c.Kind != ChargeClip && c.Kind != ChargeKeyframe {
				fail("%s: kind %q sconosciuto", w, c.Kind)
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

// lockFile impedisce due giv story run sullo stesso storyboard: crea lock in
// modo esclusivo accanto allo stato; unlock lo toglie.
func lockFile(lock, storyboard string) (unlock func(), err error) {
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("%w: c'è %s (un altro giv story run su %s in corso; se non è così, toglilo)", ErrLocked, lock, storyboard)
	}
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
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
