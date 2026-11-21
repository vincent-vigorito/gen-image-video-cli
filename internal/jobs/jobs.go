// Package jobs tiene lo stato dei job video già partiti (id e indirizzo di
// attesa), per riprenderne l'attesa e scaricarne il risultato senza un nuovo
// avvio (giv job fetch, ripresa degli storyboard).
package jobs

import (
	"encoding/json"
	"errors"
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

// Append aggiunge lo stato di un job in fondo al file (JSONL, creato se manca,
// leggibile solo dall'utente).
func Append(path string, st State) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(st)
}

// Find cerca nel file l'ultimo stato del job id di provider; ErrNotFound se
// il file non esiste o non lo contiene. Le righe illeggibili si saltano.
func Find(path, provider, id string) (State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNotFound
	}
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
