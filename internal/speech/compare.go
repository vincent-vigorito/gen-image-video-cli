package speech

import (
	"errors"
	"sort"
	"strings"
)

// Confronto fra il testo atteso di una voce e la sua trascrizione (giv qc
// --voice): parola per parola, senza maiuscole, punteggiatura e accenti,
// tenendo conto del dizionario delle pronunce (una parola detta come la sua
// pronuncia vale come la parola scritta).

// maxCompareCells: celle della tabella del confronto (parole attese × parole
// trascritte), per tenere memoria e tempo entro pochi MB e frazioni di secondo.
const maxCompareCells = 4 << 20

// WordDiff è una differenza del confronto: parola attesa e parola sentita
// (una delle due vuota per le mancanti e quelle in più), con la posizione
// (da 1) nel testo atteso; per le parole in più quella dopo cui stanno.
type WordDiff struct {
	Expected string `json:"expected,omitempty"`
	Heard    string `json:"heard,omitempty"`
	Position int    `json:"position"`
}

// Comparison è l'esito del confronto.
type Comparison struct {
	Words     int        `json:"words"`   // parole del testo atteso
	Matched   int        `json:"matched"` // parole attese sentite uguali
	Ratio     float64    `json:"ratio"`   // Matched / Words
	Different []WordDiff `json:"different"`
	Missing   []WordDiff `json:"missing"`
	Extra     []WordDiff `json:"extra"`
}

// Same dice se il testo sentito coincide con l'atteso.
func (c *Comparison) Same() bool {
	return len(c.Different) == 0 && len(c.Missing) == 0 && len(c.Extra) == 0
}

// Compare confronta il testo atteso (script, come scritto) con quello
// trascritto (heard). Nel trascritto una pronuncia del dizionario (non IPA)
// torna la sua parola scritta. Errore solo se i testi sono troppo lunghi per
// il confronto.
func Compare(script, heard string, d Dictionary) (*Comparison, error) {
	exp := compareWords(script)
	got := compareWords(heard)
	got = unpronounce(got, d)
	n, m := len(exp), len(got)
	if (n+1)*(m+1) > maxCompareCells {
		return nil, errors.New("testi troppo lunghi per il confronto parola per parola")
	}
	c := &Comparison{Words: n, Different: []WordDiff{}, Missing: []WordDiff{}, Extra: []WordDiff{}}
	// distanza di modifica a parole, con la tabella intera per ricostruire
	// le differenze
	w := m + 1
	dist := make([]int32, (n+1)*w)
	for i := 0; i <= n; i++ {
		dist[i*w] = int32(i)
	}
	for j := 0; j <= m; j++ {
		dist[j] = int32(j)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			sub := dist[(i-1)*w+j-1]
			if exp[i-1].key != got[j-1].key {
				sub++
			}
			dist[i*w+j] = min(sub, dist[(i-1)*w+j]+1, dist[i*w+j-1]+1)
		}
	}
	var diffs []WordDiff
	for i, j := n, m; i > 0 || j > 0; {
		switch {
		case i > 0 && j > 0 && exp[i-1].key == got[j-1].key && dist[i*w+j] == dist[(i-1)*w+j-1]:
			c.Matched++
			i, j = i-1, j-1
		case i > 0 && j > 0 && dist[i*w+j] == dist[(i-1)*w+j-1]+1:
			diffs = append(diffs, WordDiff{Expected: exp[i-1].text, Heard: got[j-1].text, Position: i})
			i, j = i-1, j-1
		case i > 0 && dist[i*w+j] == dist[(i-1)*w+j]+1:
			c.Missing = append(c.Missing, WordDiff{Expected: exp[i-1].text, Position: i})
			i--
		default:
			c.Extra = append(c.Extra, WordDiff{Heard: got[j-1].text, Position: i})
			j--
		}
	}
	c.Different = append(c.Different, diffs...)
	for _, l := range [][]WordDiff{c.Different, c.Missing, c.Extra} {
		sort.SliceStable(l, func(a, b int) bool { return l[a].Position < l[b].Position })
	}
	if n > 0 {
		c.Ratio = float64(c.Matched) / float64(n)
	} else if m == 0 {
		c.Ratio = 1
	}
	return c, nil
}

// cword è una parola del confronto: come appare e la sua forma normalizzata.
type cword struct{ text, key string }

// compareWords divide un testo in parole (lettere e cifre; apostrofi e
// trattini separano) con la forma normalizzata: minuscole, senza accenti.
func compareWords(s string) []cword {
	var out []cword
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return !wordRune(r) }) {
		out = append(out, cword{text: f, key: foldWord(f)})
	}
	return out
}

// foldWord è la forma di confronto di una parola: minuscole e lettere latine
// senza accenti (la trascrizione può mettere o togliere un accento).
func foldWord(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if f, ok := accentFold[r]; ok {
			r = f
		}
		b.WriteRune(r)
	}
	return b.String()
}

var accentFold = func() map[rune]rune {
	m := map[rune]rune{}
	for base, list := range map[rune]string{
		'a': "àáâäãåā", 'e': "èéêëē", 'i': "ìíîïī", 'o': "òóôöõō", 'u': "ùúûüū",
		'c': "ç", 'n': "ñ", 'y': "ýÿ",
	} {
		for _, r := range list {
			m[r] = base
		}
	}
	return m
}()

// unpronounce rimette nel testo trascritto le parole scritte del dizionario
// al posto delle loro pronunce (a parola intera, la più lunga prima; le
// pronunce IPA fra barre non si sentono come testo e si saltano).
func unpronounce(got []cword, d Dictionary) []cword {
	if len(d) == 0 || len(got) == 0 {
		return got
	}
	type entry struct {
		from []string // pronuncia, normalizzata
		to   []cword  // parola scritta
	}
	var entries []entry
	for _, k := range d.keys() {
		v := strings.TrimSpace(d[k])
		if strings.HasPrefix(v, "/") {
			continue
		}
		from := compareWords(v)
		to := compareWords(k)
		if len(from) == 0 || len(to) == 0 {
			continue
		}
		keys := make([]string, len(from))
		for i, w := range from {
			keys[i] = w.key
		}
		entries = append(entries, entry{keys, to})
	}
	sort.SliceStable(entries, func(i, j int) bool { return len(entries[i].from) > len(entries[j].from) })
	var out []cword
	for i := 0; i < len(got); {
		matched := false
		for _, e := range entries {
			if i+len(e.from) > len(got) {
				continue
			}
			ok := true
			for k, key := range e.from {
				if got[i+k].key != key {
					ok = false
					break
				}
			}
			if ok {
				out = append(out, e.to...)
				i += len(e.from)
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, got[i])
			i++
		}
	}
	return out
}

// LanguageMatches dice se la lingua rilevata dalla trascrizione corrisponde a
// quella attesa (codice BCP-47): stesso codice principale, oppure il nome
// della lingua in inglese o in italiano per i codici più comuni. Una lingua
// non riportata o "auto" attesa non si controllano (true).
func LanguageMatches(expected, detected string) bool {
	e := strings.ToLower(strings.TrimSpace(expected))
	g := strings.ToLower(strings.TrimSpace(detected))
	if e == "" || e == "auto" || g == "" {
		return true
	}
	ep, _, _ := strings.Cut(strings.ReplaceAll(e, "_", "-"), "-")
	gp, _, _ := strings.Cut(strings.ReplaceAll(g, "_", "-"), "-")
	if ep == gp {
		return true
	}
	for _, name := range languageNames[ep] {
		if g == name {
			return true
		}
	}
	return false
}

var languageNames = map[string][]string{
	"it": {"italian", "italiano", "ita"},
	"en": {"english", "inglese", "eng"},
	"fr": {"french", "francese", "fra"},
	"de": {"german", "tedesco", "deu", "ger"},
	"es": {"spanish", "spagnolo", "spa"},
	"pt": {"portuguese", "portoghese", "por"},
}
