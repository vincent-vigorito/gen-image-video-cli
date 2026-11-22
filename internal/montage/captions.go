package montage

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gen-image-video-cli/internal/speech"
)

// Sottotitoli dai testi (sottotitoli "auto" di giv story cut): il testo
// scritto di ogni voce diviso in sottotitoli di al più due righe che stanno
// nella larghezza del formato, spezzati alla fine delle frasi e alla
// punteggiatura, coi tempi della voce (per carattere, come li dà xAI) o, se
// mancano o non tornano, distribuiti sulla durata della voce, con un avviso.
// Nei sottotitoli resta la grafia scritta: il dizionario delle pronunce serve
// solo ad allineare i tempi, che sono di ciò che la voce ha detto. I testi
// passano solo dal file .ass (ASS, EscapeASS), mai dal grafo dei filtri.

const (
	captionMinChars = 12   // caratteri per riga, mai meno
	captionMaxChars = 60   // caratteri per riga, mai di più
	captionCharEm   = 0.6  // larghezza media di un carattere, in altezze del font
	captionMinS     = 0.8  // durata minima di un sottotitolo coi tempi della voce
	captionTinyS    = 0.05 // durata sotto cui un sottotitolo si allunga comunque
	captionMaxRunes = 5000 // testo di una voce
)

// CaptionVoice è il testo di una voce del montato.
type CaptionVoice struct {
	// Text è il testo scritto, reso alla lettera; un a capo chiude un
	// sottotitolo.
	Text string
	// Start sono i secondi dall'inizio del montato in cui parte la voce.
	Start float64
	// Duration è la durata della voce nel montato, in secondi: i sottotitoli
	// restano dentro e, senza tempi, vi si distribuiscono. 0: fino all'ultimo
	// tempo (servono i tempi).
	Duration float64
	// Timings sono i tempi per carattere di ciò che la voce ha detto, in
	// secondi dall'inizio della voce; nil: assenti.
	Timings []speech.CharTime
	// Dict sono le pronunce usate per la voce: i tempi seguono il testo detto.
	Dict speech.Dictionary
}

// CaptionLayout sono le misure del formato in cui vanno i sottotitoli.
type CaptionLayout struct {
	Width, Height int
	Margins       Margins // margini sicuri (SafeMargins del formato)
	// Style è lo stile dei sottotitoli; vuoto: DefaultCaptionStyle; con Size
	// 0 vale la dimensione di DefaultCaptionStyle.
	Style TextStyle
}

// Caption è un sottotitolo: una o due righe separate da "\n" e i tempi nel
// montato.
type Caption struct {
	Text  string  `json:"text"`
	Start float64 `json:"start_s"`
	End   float64 `json:"end_s"`
	Voice int     `json:"voice"` // indice della voce in Captions, da 0
}

// DefaultCaptionStyle è lo stile di default dei sottotitoli per un quadro
// w×h: bianco con bordo nero, alto 1/18 del lato corto.
func DefaultCaptionStyle(w, h int) TextStyle {
	return DefaultTextStyle(min(w, h))
}

// style è lo stile effettivo di l.
func (l CaptionLayout) style() TextStyle {
	if l.Style == (TextStyle{}) {
		return DefaultCaptionStyle(l.Width, l.Height)
	}
	st := l.Style
	if st.Size == 0 {
		st.Size = DefaultCaptionStyle(l.Width, l.Height).Size
	}
	return st
}

// check controlla le misure di l.
func (l CaptionLayout) check() error {
	if l.Width < 16 || l.Width > maxSide || l.Height < 16 || l.Height > maxSide {
		return invalid("sottotitoli: quadro %dx%d fuori dai limiti", l.Width, l.Height)
	}
	if err := l.Margins.Check(); err != nil {
		return invalid("sottotitoli: %v", err)
	}
	if st := l.style(); st.Size < 1 || st.Size > l.Height {
		return invalid("sottotitoli: dimensione %d: da 1 a %d pixel", st.Size, l.Height)
	}
	return nil
}

// CaptionChars sono i caratteri per riga dei sottotitoli in l: la larghezza
// dentro i margini divisa per quella media di un carattere, da
// captionMinChars a captionMaxChars.
func CaptionChars(l CaptionLayout) int {
	_, _, left, right := l.Margins.Pixels(l.Width, l.Height)
	size := l.style().Size
	if size < 1 {
		return captionMinChars
	}
	n := int(math.Floor(float64(l.Width-left-right) / (captionCharEm * float64(size))))
	return max(captionMinChars, min(captionMaxChars, n))
}

// captionWord è una parola del testo scritto: il testo e la posizione
// (rune, fine esclusa) nel testo.
type captionWord struct {
	text string
	a, b int
	n    int  // caratteri visibili
	stop bool // chiude una frase (. ! ? …) o una riga del testo (a capo)
	soft bool // seguita da una pausa (, ; : o un trattino)
}

// captionClosers sono le chiusure dopo la punteggiatura (virgolette, parentesi).
const captionClosers = "\"'»)]”’"

// visibleLen conta i caratteri di s senza quelli di controllo (EscapeASS li toglie).
func visibleLen(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x20 && r != 0x7f && (r < 0x80 || r >= 0xa0) && r != 0x2028 && r != 0x2029 {
			n++
		}
	}
	return n
}

// lineBreak dice se r è un a capo.
func lineBreak(r rune) bool {
	return r == '\n' || r == '\r' || r == 0x85 || r == 0x2028 || r == 0x2029
}

// captionWords divide text (già in rune) in parole separate da spazi; le
// parole più lunghe di maxChars si spezzano in pezzi da maxChars.
func captionWords(text []rune, maxChars int) []captionWord {
	var ws []captionWord
	for i := 0; i < len(text); {
		if unicode.IsSpace(text[i]) || lineBreak(text[i]) {
			if lineBreak(text[i]) && len(ws) > 0 {
				ws[len(ws)-1].stop = true
			}
			i++
			continue
		}
		j := i
		for j < len(text) && !unicode.IsSpace(text[j]) && !lineBreak(text[j]) {
			j++
		}
		word := string(text[i:j])
		last := strings.TrimRight(word, captionClosers)
		r, _ := utf8.DecodeLastRuneInString(last)
		w := captionWord{
			stop: strings.ContainsRune(".!?…", r),
			soft: strings.ContainsRune(",;:", r) || last == "-" || last == "–" || last == "—",
		}
		// pezzi da maxChars caratteri visibili per le parole troppo lunghe
		start := i
		for k, n := i, 0; k < j; k++ {
			if visibleLen(string(text[k])) == 1 {
				n++
			}
			if n == maxChars && k+1 < j {
				ws = append(ws, captionWord{text: string(text[start : k+1]), a: start, b: k + 1, n: n})
				start, n = k+1, 0
			}
		}
		w.text, w.a, w.b = string(text[start:j]), start, j
		if w.n = visibleLen(w.text); w.n > 0 {
			ws = append(ws, w)
		} else if w.stop && len(ws) > 0 {
			ws[len(ws)-1].stop = true
		}
		i = j
	}
	return ws
}

// wordsLen è la lunghezza di una riga con le parole ws separate da spazi.
func wordsLen(ws []captionWord) int {
	n := 0
	for i, w := range ws {
		if i > 0 {
			n++
		}
		n += w.n
	}
	return n
}

// captionWrap divide le parole di un sottotitolo in righe di al più maxChars
// caratteri: una riga se ci stanno, altrimenti due, spezzate dopo la
// punteggiatura se possibile e il più possibile uguali. ok è false se non
// stanno in due righe.
func captionWrap(ws []captionWord, maxChars int) (lines [][]captionWord, ok bool) {
	if wordsLen(ws) <= maxChars {
		return [][]captionWord{ws}, true
	}
	best, bestScore := -1, math.MaxInt
	for k := 1; k < len(ws); k++ {
		l1, l2 := wordsLen(ws[:k]), wordsLen(ws[k:])
		if l1 > maxChars || l2 > maxChars {
			continue
		}
		score := max(l1, l2) * 4
		if ws[k-1].soft || ws[k-1].stop {
			score -= maxChars
		}
		if score < bestScore {
			best, bestScore = k, score
		}
	}
	if best < 0 {
		return nil, false
	}
	return [][]captionWord{ws[:best], ws[best:]}, true
}

// captionCut è quante parole di cur vanno nel sottotitolo quando la parola
// seguente non ci sta: fino all'ultima pausa (, ; :) che lascia almeno un
// terzo della riga, altrimenti tutte.
func captionCut(cur []captionWord, maxChars int) int {
	for j := len(cur) - 1; j >= 1; j-- {
		if cur[j-1].soft && wordsLen(cur[:j])*3 >= maxChars {
			return j
		}
	}
	return len(cur)
}

// captionSplit divide le parole in sottotitoli: ognuno comincia una frase o
// prosegue quella di prima e sta in due righe.
func captionSplit(ws []captionWord, maxChars int) [][]captionWord {
	fits := func(c []captionWord) bool {
		_, ok := captionWrap(c, maxChars)
		return ok
	}
	var cues [][]captionWord
	var cur []captionWord
	for _, w := range ws {
		for len(cur) > 0 && !fits(append(cur[:len(cur):len(cur)], w)) {
			j := captionCut(cur, maxChars)
			cues = append(cues, cur[:j])
			cur = append([]captionWord(nil), cur[j:]...)
		}
		cur = append(cur, w)
		if w.stop {
			cues = append(cues, cur)
			cur = nil
		}
	}
	if len(cur) > 0 {
		cues = append(cues, cur)
	}
	return cues
}

// captionText è il testo di un sottotitolo: le righe separate da "\n".
func captionText(cue []captionWord, maxChars int) string {
	lines, ok := captionWrap(cue, maxChars)
	if !ok {
		lines = [][]captionWord{cue}
	}
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		for j, w := range l {
			if j > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(w.text)
		}
	}
	return b.String()
}

// SplitCaptions divide text nei sottotitoli di righe da al più maxChars
// caratteri (da captionMinChars a captionMaxChars): ogni elemento sono le
// righe (una o due) di un sottotitolo. Le frasi (. ! ? … o a capo) cominciano
// un sottotitolo nuovo; una frase lunga si spezza alla punteggiatura (, ; :)
// se possibile.
func SplitCaptions(text string, maxChars int) [][]string {
	maxChars = max(captionMinChars, min(captionMaxChars, maxChars))
	var out [][]string
	for _, cue := range captionSplit(captionWords([]rune(text), maxChars), maxChars) {
		out = append(out, strings.Split(captionText(cue, maxChars), "\n"))
	}
	return out
}

// captionSpoken è il testo detto dalla voce (le pronunce del dizionario al
// posto delle parole scritte, a parola intera, senza distinguere le
// maiuscole, la più lunga per prima, come speech.Dictionary.Apply) e, per
// ogni posizione del testo scritto (fine compresa), quella nel testo detto.
func captionSpoken(d speech.Dictionary, text []rune) (spoken []rune, at []int) {
	at = make([]int, len(text)+1)
	type entry struct {
		key []rune
		val []rune
	}
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := make([]entry, 0, len(keys))
	for _, k := range keys {
		if k != "" {
			entries = append(entries, entry{[]rune(k), []rune(d[k])})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return len(entries[i].key) > len(entries[j].key) })
	word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
	for i := 0; i < len(text); {
		if i == 0 || !word(text[i-1]) {
			matched := false
			for _, e := range entries {
				n := len(e.key)
				if i+n <= len(text) && (i+n == len(text) || !word(text[i+n])) && strings.EqualFold(string(text[i:i+n]), string(e.key)) {
					sa := len(spoken)
					spoken = append(spoken, e.val...)
					for j := range n {
						at[i+j] = sa + j*len(e.val)/n
					}
					i += n
					matched = true
					break
				}
			}
			if matched {
				continue
			}
		}
		at[i] = len(spoken)
		spoken = append(spoken, text[i])
		i++
	}
	at[len(text)] = len(spoken)
	return spoken, at
}

// captionTime è il tempo di un carattere (non spazio) detto.
type captionTime struct{ start, end float64 }

// captionTimes sono i tempi dei caratteri non spazio di ts.
func captionTimes(ts []speech.CharTime) []captionTime {
	var out []captionTime
	for _, t := range ts {
		for _, r := range t.Char {
			if !unicode.IsSpace(r) && !lineBreak(r) {
				out = append(out, captionTime{t.Start, t.End})
			}
		}
	}
	return out
}

// voiceCaptions sono i sottotitoli di una voce; warn raccoglie gli avvisi.
func voiceCaptions(idx int, v CaptionVoice, maxChars int, warn func(string, ...any)) []Caption {
	text := []rune(v.Text)
	cues := captionSplit(captionWords(text, maxChars), maxChars)
	if len(cues) == 0 {
		return nil
	}
	n := idx + 1
	var ts []captionTime
	if v.Timings == nil {
		warn("voce %d: tempi assenti: sottotitoli distribuiti sulla durata della voce", n)
	} else if err := speech.CheckTimings(v.Timings, v.Duration); err != nil {
		warn("voce %d: tempi scartati (%v): sottotitoli distribuiti sulla durata della voce", n, err)
	} else {
		if ts = captionTimes(v.Timings); len(ts) == 0 {
			warn("voce %d: tempi senza caratteri: sottotitoli distribuiti sulla durata della voce", n)
		}
	}
	out := make([]Caption, len(cues))
	for i, c := range cues {
		out[i] = Caption{Text: captionText(c, maxChars), Voice: idx}
	}
	if len(ts) == 0 {
		// distribuiti sulla durata, in proporzione ai caratteri non spazio
		ns := make([]int, len(text)+1)
		for i, r := range text {
			ns[i+1] = ns[i]
			if !unicode.IsSpace(r) && !lineBreak(r) {
				ns[i+1]++
			}
		}
		total := float64(max(ns[len(text)], 1))
		for i, c := range cues {
			out[i].Start = v.Start + v.Duration*float64(ns[c[0].a])/total
			out[i].End = v.Start + v.Duration*float64(ns[c[len(c)-1].b])/total
		}
		return captionTidy(out, v.Start, v.Start+v.Duration, false)
	}
	spoken, at := captionSpoken(v.Dict, text)
	ns := make([]int, len(spoken)+1)
	for i, r := range spoken {
		ns[i+1] = ns[i]
		if !unicode.IsSpace(r) && !lineBreak(r) {
			ns[i+1]++
		}
	}
	nE := ns[len(spoken)]
	if nE != len(ts) {
		warn("voce %d: tempi non allineati al testo (%d caratteri detti, %d tempi): stimati in proporzione", n, nE, len(ts))
	}
	pick := func(k int) captionTime {
		k = max(0, min(k, nE-1))
		if nE != len(ts) && nE > 1 {
			k = int(math.Round(float64(k) * float64(len(ts)-1) / float64(nE-1)))
		}
		return ts[max(0, min(k, len(ts)-1))]
	}
	for i, c := range cues {
		first := ns[at[c[0].a]]
		last := max(first, ns[at[c[len(c)-1].b]]-1)
		out[i].Start = v.Start + pick(first).start
		out[i].End = v.Start + pick(last).end
	}
	hi := v.Start + v.Duration
	if v.Duration <= 0 {
		hi = v.Start + ts[len(ts)-1].end
	}
	return captionTidy(out, v.Start, hi, true)
}

// captionTidy mette i sottotitoli di una voce dentro [lo, hi], in ordine e
// senza sovrapposizioni; coi tempi della voce (timed) ognuno resta almeno
// captionMinS, se il seguente lo permette.
func captionTidy(cs []Caption, lo, hi float64, timed bool) []Caption {
	for i := range cs {
		c := &cs[i]
		c.Start = math.Max(lo, math.Min(c.Start, hi))
		c.End = math.Max(c.Start, math.Min(c.End, hi))
		if i > 0 && c.Start < cs[i-1].End {
			cs[i-1].End = math.Max(cs[i-1].Start, c.Start)
		}
	}
	for i := range cs {
		c := &cs[i]
		limit := hi
		if i+1 < len(cs) {
			limit = math.Min(limit, cs[i+1].Start)
		}
		if timed && c.End-c.Start < captionMinS {
			c.End = math.Max(c.End, math.Min(c.Start+captionMinS, limit))
		}
		if c.End-c.Start < captionTinyS {
			c.End = c.Start + captionTinyS
		}
		c.Start = math.Round(c.Start*1000) / 1000
		c.End = math.Round(c.End*1000) / 1000
	}
	return cs
}

// Captions sono i sottotitoli delle voci nel formato l, in ordine di inizio,
// con gli avvisi (tempi assenti, scartati o non allineati). Una voce senza
// testo non ne ha. Errori (ErrInvalid): misure del formato, tempi o durate
// non validi, testo oltre captionMaxRunes caratteri, voce senza tempi né
// durata.
func Captions(voices []CaptionVoice, l CaptionLayout) ([]Caption, []string, error) {
	if err := l.check(); err != nil {
		return nil, nil, err
	}
	if len(voices) > maxClips {
		return nil, nil, invalid("sottotitoli: al più %d voci", maxClips)
	}
	maxChars := CaptionChars(l)
	var out []Caption
	var warnings []string
	warn := func(format string, a ...any) { warnings = append(warnings, fmt.Sprintf(format, a...)) }
	for i, v := range voices {
		n := i + 1
		if !finite(v.Start) || v.Start < 0 || v.Start > 24*3600 {
			return nil, nil, invalid("sottotitoli: voce %d: inizio %v s non valido", n, v.Start)
		}
		if !finite(v.Duration) || v.Duration < 0 || v.Duration > 24*3600 {
			return nil, nil, invalid("sottotitoli: voce %d: durata %v s non valida", n, v.Duration)
		}
		if c := utf8.RuneCountInString(v.Text); c > captionMaxRunes {
			return nil, nil, invalid("sottotitoli: voce %d: testo di %d caratteri, al più %d", n, c, captionMaxRunes)
		}
		if strings.TrimSpace(v.Text) == "" {
			continue
		}
		if v.Duration == 0 && (v.Timings == nil || speech.CheckTimings(v.Timings, 0) != nil || len(captionTimes(v.Timings)) == 0) {
			return nil, nil, invalid("sottotitoli: voce %d: servono i tempi o la durata della voce", n)
		}
		out = append(out, voiceCaptions(i, v, maxChars, warn)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out, warnings, nil
}

// CaptionTexts sono i sottotitoli come testi del file .ass (ASS), in basso
// dentro i margini, con lo stile di l: da unire agli altri testi del video.
func CaptionTexts(cs []Caption, l CaptionLayout) []Text {
	st := l.style()
	ts := make([]Text, len(cs))
	for i, c := range cs {
		ts[i] = Text{Text: c.Text, Start: c.Start, End: c.End, Pos: Bottom, Style: st}
	}
	return ts
}

// CaptionsASS è il file .ass dei sottotitoli cs nel formato l (ASS: testi
// resi alla lettera); da scrivere con Work.WriteFile e incidere con ASSFilter.
func CaptionsASS(cs []Caption, l CaptionLayout) ([]byte, error) {
	if err := l.check(); err != nil {
		return nil, err
	}
	return ASS(l.Width, l.Height, l.Margins, CaptionTexts(cs, l))
}
