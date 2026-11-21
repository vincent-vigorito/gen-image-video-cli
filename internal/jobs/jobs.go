// Package jobs tiene lo stato dei job video già partiti (id e indirizzo di
// attesa), per riprenderne l'attesa e scaricarne il risultato senza un nuovo
// avvio (giv job fetch, ripresa degli storyboard).
package jobs

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

// State è lo stato di un job video, una riga JSON del file dei job.
type State struct {
	Provider   string    `json:"provider"`
	ID         string    `json:"id"`
	PollingURL string    `json:"polling_url,omitempty"`
	Model      string    `json:"model,omitempty"`
	Prompt     string    `json:"prompt,omitempty"` // prompt inviato
	Started    time.Time `json:"started"`
}

// ErrNotFound: nel file non c'è un job con quel provider e quell'id.
var ErrNotFound = errors.New("job non trovato")

// ErrNotRegular: il file dei job è un symlink o non è un file regolare.
var ErrNotRegular = errors.New("non è un file regolare: rifiutato")

// Append aggiunge lo stato di un job in fondo al file (JSONL, creato con
// permessi 0600 se manca; un file esistente leggibile da altri torna 0600).
func Append(path string, st State) error {
	f, err := openRegular(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := f.Chmod(0o600); err != nil {
			return err
		}
	}
	return json.NewEncoder(f).Encode(st)
}

// openRegular apre path solo se è un file regolare e non un symlink: il
// descrittore aperto deve essere lo stesso file che path indica senza seguire
// link (controllo dopo l'apertura, prima di leggere o scrivere).
func openRegular(path string, flag int) (*os.File, error) {
	if li, err := os.Lstat(path); err == nil && !li.Mode().IsRegular() {
		return nil, &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
	}
	f, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil {
		var li os.FileInfo
		if li, err = os.Lstat(path); err == nil && (!fi.Mode().IsRegular() || !li.Mode().IsRegular() || !os.SameFile(fi, li)) {
			err = &os.PathError{Op: "open", Path: path, Err: ErrNotRegular}
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Find cerca nel file l'ultimo stato del job id di provider; ErrNotFound se
// il file non esiste o non lo contiene. Le righe illeggibili si saltano.
func Find(path, provider, id string) (State, error) {
	f, err := openRegular(path, os.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return State{}, err
	}
	var found *State
	for _, l := range strings.Split(string(data), "\n") {
		var st State
		if strings.TrimSpace(l) == "" || json.Unmarshal([]byte(l), &st) != nil {
			continue
		}
		if st.Provider == provider && st.ID == id {
			found = &st
		}
	}
	if found == nil {
		return State{}, ErrNotFound
	}
	return *found, nil
}
