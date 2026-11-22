package speech

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// CharTime è il tempo di un carattere pronunciato, in secondi dall'inizio.
type CharTime struct {
	Char  string  `json:"c"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// TimingsFile è il file <nome>.tempi.json scritto accanto al WAV: il testo
// scritto, la durata misurata e i tempi per carattere come li dà il provider
// (con un dizionario sono i caratteri di ciò che la voce ha detto).
type TimingsFile struct {
	Provider  string     `json:"provider"`
	Text      string     `json:"text"`
	DurationS float64    `json:"duration_s"`
	Chars     []CharTime `json:"chars"`
}

// MaxTimingsFileBytes: lettura di un file dei tempi.
const MaxTimingsFileBytes = 8 << 20

// timeSlack: tolleranza sui tempi oltre la durata del WAV.
const timeSlack = 0.5

// CheckTimings controlla i tempi rispetto alla durata misurata: numeri finiti
// e non negativi, inizio non oltre la fine, inizi in ordine, nessun tempo
// oltre la durata (con mezzo secondo di tolleranza). Un errore dice perché
// vanno scartati (i sottotitoli ripiegano sulla durata).
func CheckTimings(ts []CharTime, duration float64) error {
	if len(ts) == 0 {
		return errors.New("nessun tempo")
	}
	prev := 0.0
	for i, t := range ts {
		for _, v := range []float64{t.Start, t.End} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return fmt.Errorf("tempo %d non valido", i+1)
			}
		}
		if t.End < t.Start {
			return fmt.Errorf("tempo %d: fine prima dell'inizio", i+1)
		}
		if t.Start+1e-6 < prev {
			return fmt.Errorf("tempo %d fuori ordine", i+1)
		}
		if duration > 0 && t.End > duration+timeSlack {
			return fmt.Errorf("tempo %d oltre la durata dell'audio", i+1)
		}
		prev = t.Start
	}
	return nil
}

// MarshalTimings scrive il file dei tempi.
func MarshalTimings(tf TimingsFile) ([]byte, error) {
	if tf.Chars == nil {
		tf.Chars = []CharTime{}
	}
	data, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ParseTimings legge un file dei tempi scritto da giv: campi sconosciuti
// rifiutati, tempi controllati con CheckTimings.
func ParseTimings(data []byte) (*TimingsFile, error) {
	if len(data) > MaxTimingsFileBytes {
		return nil, fmt.Errorf("file dei tempi oltre %d MiB", MaxTimingsFileBytes>>20)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var tf TimingsFile
	if err := dec.Decode(&tf); err != nil {
		return nil, fmt.Errorf("file dei tempi non valido: %v", err)
	}
	if dec.More() {
		return nil, errors.New("file dei tempi non valido: dati in più")
	}
	if math.IsNaN(tf.DurationS) || math.IsInf(tf.DurationS, 0) || tf.DurationS < 0 {
		return nil, errors.New("file dei tempi: durata non valida")
	}
	if len(tf.Chars) > 0 {
		if err := CheckTimings(tf.Chars, tf.DurationS); err != nil {
			return nil, fmt.Errorf("file dei tempi: %v", err)
		}
	}
	return &tf, nil
}
