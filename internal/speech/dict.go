package speech

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// Limiti del dizionario delle pronunce (campo replace di xAI).
const (
	MaxDictEntries    = 200
	MaxDictKeyChars   = 100
	MaxDictValueChars = 128
	MaxDictFileBytes  = 64 << 10
)

// Dictionary sono le pronunce: parola o frase scritta -> come dirla
// (grafia rifatta, oppure IPA fra barre: "/ˈanja/"). Il confronto è a parola
// intera e senza distinguere le maiuscole; vince la corrispondenza più lunga.
type Dictionary map[string]string

// ParseDictionary legge un dizionario JSON: un oggetto piatto di stringhe
// ({"Swerpify": "suèrpifai"}), senza chiavi ripetute (nemmeno a meno delle
// maiuscole), poi Check.
func ParseDictionary(data []byte) (Dictionary, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("pronunce: serve un oggetto JSON {\"parola\": \"come dirla\"}")
	}
	d := Dictionary{}
	seen := map[string]string{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("pronunce: JSON non valido: %v", err)
		}
		k, _ := kt.(string)
		vt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("pronunce: JSON non valido: %v", err)
		}
		v, ok := vt.(string)
		if !ok {
			return nil, fmt.Errorf("pronunce: il valore di %q deve essere una stringa", k)
		}
		low := strings.ToLower(k)
		if prev, dup := seen[low]; dup {
			return nil, fmt.Errorf("pronunce: %q e %q sono la stessa parola (il confronto non distingue le maiuscole)", prev, k)
		}
		seen[low] = k
		d[k] = v
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return nil, errors.New("pronunce: JSON non valido")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("pronunce: dati in più dopo l'oggetto JSON")
	}
	if err := d.Check(); err != nil {
		return nil, err
	}
	return d, nil
}

// ReadDictionaryFile legge un file di pronunce indicato dall'utente: file
// normale, al massimo MaxDictFileBytes.
func ReadDictionaryFile(path string) (Dictionary, error) {
	data, err := ReadSmallFile(path, MaxDictFileBytes)
	if err != nil {
		return nil, fmt.Errorf("pronunce %s: %v", path, err)
	}
	d, err := ParseDictionary(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return d, nil
}

// ReadSmallFile legge un file normale di al massimo limit byte. L'apertura è
// senza blocco (una FIFO o un device non fermano la CLI) e il tipo si
// controlla sul descrittore aperto.
func ReadSmallFile(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("non è un file normale")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("oltre %d KiB", limit>>10)
	}
	return data, nil
}

// Check controlla i limiti di xAI: al massimo 200 voci; chiave di lettere,
// cifre, apostrofi e spazi fino a 100 caratteri; valore non vuoto fino a 128,
// senza caratteri di controllo.
func (d Dictionary) Check() error {
	if len(d) > MaxDictEntries {
		return fmt.Errorf("pronunce: %d voci, al massimo %d", len(d), MaxDictEntries)
	}
	seen := map[string]string{}
	for _, k := range d.keys() {
		v := d[k]
		if strings.TrimSpace(k) == "" || !utf8.ValidString(k) {
			return errors.New("pronunce: parola vuota o non valida")
		}
		if n := utf8.RuneCountInString(k); n > MaxDictKeyChars {
			return fmt.Errorf("pronunce: %q ha %d caratteri, al massimo %d", short(k), n, MaxDictKeyChars)
		}
		for _, r := range k {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' && r != '\'' && r != '’' {
				return fmt.Errorf("pronunce: %q: nella parola solo lettere, cifre, apostrofi e spazi", short(k))
			}
		}
		low := strings.ToLower(k)
		if prev, dup := seen[low]; dup {
			return fmt.Errorf("pronunce: %q e %q sono la stessa parola (il confronto non distingue le maiuscole)", prev, k)
		}
		seen[low] = k
		if strings.TrimSpace(v) == "" || !utf8.ValidString(v) {
			return fmt.Errorf("pronunce: %q senza pronuncia", short(k))
		}
		if n := utf8.RuneCountInString(v); n > MaxDictValueChars {
			return fmt.Errorf("pronunce: la pronuncia di %q ha %d caratteri, al massimo %d", short(k), n, MaxDictValueChars)
		}
		for _, r := range v {
			if unicode.IsControl(r) {
				return fmt.Errorf("pronunce: la pronuncia di %q ha caratteri di controllo", short(k))
			}
		}
	}
	return nil
}

// keys sono le chiavi in ordine, per messaggi stabili.
func (d Dictionary) keys() []string {
	ks := make([]string, 0, len(d))
	for k := range d {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Merge restituisce le voci di d con sopra quelle di over (vince over, anche
// a meno delle maiuscole): kit sotto, storyboard o --pronuncia sopra.
func (d Dictionary) Merge(over Dictionary) Dictionary {
	if len(d) == 0 && len(over) == 0 {
		return nil
	}
	out := Dictionary{}
	for k, v := range d {
		out[k] = v
	}
	for k, v := range over {
		for old := range out {
			if strings.EqualFold(old, k) {
				delete(out, old)
			}
		}
		out[k] = v
	}
	return out
}

// Apply sostituisce nel testo le parole del dizionario, a parola intera e
// senza distinguere le maiuscole, la più lunga per prima: per i provider
// senza dizionario (gemini) e per i confronti col testo trascritto.
func (d Dictionary) Apply(text string) string {
	if len(d) == 0 {
		return text
	}
	type entry struct {
		key []rune
		val string
	}
	entries := make([]entry, 0, len(d))
	for _, k := range d.keys() {
		entries = append(entries, entry{[]rune(k), d[k]})
	}
	sort.SliceStable(entries, func(i, j int) bool { return len(entries[i].key) > len(entries[j].key) })
	rs := []rune(text)
	var b strings.Builder
	for i := 0; i < len(rs); {
		if i == 0 || !wordRune(rs[i-1]) {
			matched := false
			for _, e := range entries {
				n := len(e.key)
				if i+n <= len(rs) && (i+n == len(rs) || !wordRune(rs[i+n])) && strings.EqualFold(string(rs[i:i+n]), string(e.key)) {
					b.WriteString(e.val)
					i += n
					matched = true
					break
				}
			}
			if matched {
				continue
			}
		}
		b.WriteRune(rs[i])
		i++
	}
	return b.String()
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// short accorcia un testo per i messaggi.
func short(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}
