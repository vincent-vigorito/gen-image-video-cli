package story

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	Fallito = "fallito" // errore; con un job, la prossima esecuzione prova prima a recuperarlo
	Incerto = "incerto" // richiesta di generazione inviata senza esito: forse addebitata, si rifà solo con --scene
	DaFare  = "da_fare" // niente in corso e nessuna clip nuova (es. fotogramma chiave fatto, clip no)
)

// Tipi di generazione pagata.
const (
	ChargeClip     = "clip"
	ChargeKeyframe = "fotogramma"
)

// State è lo stato di giv story run: per ogni scena cosa è fatto, quale job è
// partito e quanto è stato speso. Fa fede la contabilità privata dell'utente
// (PrivateStatePath); il file accanto allo storyboard (StatePath) ne è la copia,
// letta da giv story cut, e deve coincidere.
type State struct {
	Version    int                    `json:"version"`
	Storyboard string                 `json:"storyboard,omitempty"` // percorso assoluto, link risolti
	Scenes     map[string]*SceneState `json:"scenes"`

	path    string // file accanto allo storyboard
	private string // contabilità privata; "" per uno stato letto con LoadState
}

// SceneState è lo stato di una scena video.
type SceneState struct {
	Status   string   `json:"status"`
	Clip     *Take    `json:"clip,omitempty"`     // ripresa in uso
	Keyframe *Take    `json:"keyframe,omitempty"` // fotogramma chiave, riusato alle riprese successive
	Job      *Job     `json:"job,omitempty"`      // job avviato e non ancora scaricato
	Error    string   `json:"error,omitempty"`    // ultimo errore
	Previous []Take   `json:"previous,omitempty"` // riprese sostituite: i file restano su disco
	Charges  []Charge `json:"charges,omitempty"`  // generazioni pagate o forse pagate
}

// Take è un file generato, relativo alla cartella dello storyboard.
type Take struct {
	File  string    `json:"file"`
	JobID string    `json:"job_id,omitempty"`
	Done  time.Time `json:"done"`
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

// Spent è la spesa di tutte le scene (riprese precedenti e job falliti compresi).
func (st *State) Spent() float64 {
	var total float64
	for _, sc := range st.Scenes {
		for _, c := range sc.Charges {
			total += c.Spent()
		}
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

// PrivatePath è il percorso della contabilità privata ("" se lo stato è stato
// letto con LoadState).
func (st *State) PrivatePath() string { return st.private }

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
	st := newState(path)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(st); err != nil {
		return nil, fmt.Errorf("file di stato %s: %w", path, jsonError(data, err))
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("file di stato %s: altro testo dopo lo stato", path)
	}
	if err := st.validate(); err != nil {
		return nil, fmt.Errorf("file di stato %s non valido (correggilo o toglilo: le scene fatte si rifarebbero):\n%w", path, err)
	}
	if st.Scenes == nil {
		st.Scenes = map[string]*SceneState{}
	}
	return st, nil
}

// PrivateStatePath è la contabilità privata dello storyboard: un file per
// storyboard (percorso assoluto, link risolti) in <config utente>/giv/story,
// cartella 0700 che chi scrive nella cartella dello storyboard non tocca.
func PrivateStatePath(storyboard string) (string, error) {
	real, err := realPath(storyboard)
	if err != nil {
		return "", err
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "giv", "story")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s non è una cartella", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	sum := sha256.Sum256([]byte(real))
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".json"), nil
}

func realPath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// ErrMismatch: il file di stato accanto allo storyboard non coincide con la
// contabilità privata, o ha job che questa non conosce.
var ErrMismatch = errors.New("file di stato diverso dalla contabilità privata")

// OpenRunState apre lo stato di giv story run per lo storyboard: blocca la
// contabilità privata (una sola esecuzione per storyboard), la legge e la
// confronta col file accanto allo storyboard, che deve coincidere. Senza
// contabilità privata (progetto copiato, cartella spostata) adotta il file
// accanto, spesa compresa, solo se non ha job né richieste in corso: imported
// lo dice. unlock toglie il blocco.
func OpenRunState(storyboard string) (st *State, unlock func(), imported bool, err error) {
	priv, err := PrivateStatePath(storyboard)
	if err != nil {
		return nil, nil, false, fmt.Errorf("contabilità privata: %w", err)
	}
	real, err := realPath(storyboard)
	if err != nil {
		return nil, nil, false, err
	}
	release, err := lockFile(strings.TrimSuffix(priv, ".json")+".lock", storyboard)
	if err != nil {
		return nil, nil, false, err
	}
	defer func() {
		if err != nil {
			release()
		}
	}()
	shared := StatePath(storyboard)
	mirror, err := readState(shared)
	hasMirror := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, err
	}
	st, err = readState(priv)
	switch {
	case errors.Is(err, os.ErrNotExist):
		st = newState(priv)
		if hasMirror && len(mirror.Scenes) > 0 {
			if err := mirror.importable(); err != nil {
				return nil, nil, false, fmt.Errorf("%w: %s senza contabilità privata (copiato, importato o di un altro utente):\n%w", ErrMismatch, shared, err)
			}
			st.Scenes, imported = mirror.Scenes, true
		}
	case err != nil:
		return nil, nil, false, err
	case hasMirror && !sameState(st, mirror):
		return nil, nil, false, fmt.Errorf("%w: %s non coincide con %s (modificato da fuori o non aggiornato all'ultimo salvataggio): toglilo, giv story run lo riscrive dalla contabilità privata",
			ErrMismatch, shared, priv)
	}
	st.Storyboard, st.path, st.private = real, shared, priv
	return st, release, imported, nil
}

// importable: un file di stato senza contabilità privata si adotta solo se non
// ha job né richieste dall'esito incerto, che nessuno saprebbe più contare.
func (st *State) importable() error {
	var errs []error
	for _, id := range sortedKeys(st.Scenes) {
		if sc := st.Scenes[id]; sc.Job != nil || sc.Status == Incerto {
			errs = append(errs, fmt.Errorf("scena %s: job o richiesta in corso (%s): recuperalo con giv job fetch dalla cartella in cui è partito, poi togli il file", id, sc.Status))
		}
	}
	return errors.Join(errs...)
}

func sameState(a, b *State) bool {
	x, err1 := a.marshal()
	y, err2 := b.marshal()
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

func (st *State) validate() error {
	if st.Version != StateVersion {
		return fmt.Errorf("versione %d non riconosciuta (attesa %d)", st.Version, StateVersion)
	}
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	takeOK := func(where string, t *Take) {
		if t == nil {
			return
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
			takeOK(fmt.Sprintf("%s previous n. %d", where, i+1), &sc.Previous[i])
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

// ErrMirror: la contabilità privata è salvata, la copia accanto allo
// storyboard no (un avviso: la prossima esecuzione chiede di toglierla).
var ErrMirror = errors.New("copia dello stato non salvata")

// Save scrive in modo atomico la contabilità privata, poi la copia accanto
// allo storyboard. Un errore sulla contabilità privata vuol dire che nessuna
// nuova richiesta deve partire; uno sulla sola copia è ErrMirror.
func (st *State) Save() error {
	data, err := st.marshal()
	if err != nil {
		return err
	}
	if st.private != "" {
		if err := writeAtomic(st.private, data); err != nil {
			return fmt.Errorf("contabilità privata %s: %w", st.private, err)
		}
	}
	if err := writeAtomic(st.path, data); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrMirror, st.path, err)
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
// sostituisce la voce senza seguire link), leggibile solo dall'utente.
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
	return os.Rename(tmp, path)
}

// ErrLocked: un'altra esecuzione usa lo stesso storyboard.
var ErrLocked = errors.New("storyboard in uso")

// lockFile impedisce due giv story run sullo stesso storyboard: crea lock in
// modo esclusivo nella cartella privata; unlock lo toglie.
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
