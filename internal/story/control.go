package story

import (
	"path/filepath"
	"strings"
)

// ControlPath è il file del controllo dei file (blocco controllo, tecnico)
// accanto allo storyboard: <nome>.controllo.json, riscritto da giv story run
// e cut.
func ControlPath(storyboard string) string {
	return strings.TrimSuffix(storyboard, filepath.Ext(storyboard)) + ".controllo.json"
}

// WriteControl scrive il file del controllo come lo stato: file temporaneo
// nella stessa cartella e rename (un link al suo posto si sostituisce, non si
// segue), leggibile solo dall'utente.
func WriteControl(path string, data []byte) error {
	return writeAtomic(path, data)
}
