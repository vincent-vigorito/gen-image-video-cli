package montage

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Testi incisi nel video (titoli, cartelli, etichette dei fogli di contatto):
// sempre in un file .ass generato da ASS e letto dal filtro ass (ASSFilter),
// mai nella stringa dei filtri. Nel file ogni testo è reso alla lettera
// (EscapeASS): niente blocchi {…} di override, niente \N o \h dell'utente,
// niente righe nuove nel file. Le direttive (posizione, dissolvenza) le scrive
// solo giv.

// DefaultFont è la famiglia dei testi senza un file di font: libera e di
// solito installata; se manca, libass ne sceglie un'altra.
const DefaultFont = "DejaVu Sans"

// maxFontBytes è il tetto di un file di font letto o copiato.
const maxFontBytes = 64 << 20

// fontExt sono le estensioni dei file di font ammessi.
var fontExt = map[string]bool{".ttf": true, ".otf": true, ".ttc": true}

// Color è un colore RGB.
type Color struct{ R, G, B uint8 }

// Colori di default dei testi.
var (
	White = Color{255, 255, 255}
	Black = Color{0, 0, 0}
)

// ParseColor legge un colore "#RRGGBB" (anche senza #).
func ParseColor(s string) (Color, error) {
	h := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(h) != 6 {
		return Color{}, fmt.Errorf("colore %q: serve la forma #RRGGBB", s)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return Color{}, fmt.Errorf("colore %q: serve la forma #RRGGBB", s)
	}
	return Color{uint8(v >> 16), uint8(v >> 8), uint8(v)}, nil
}

// Hex è il colore per le opzioni di ffmpeg (es. color=c=0xRRGGBB): solo cifre esadecimali.
func (c Color) Hex() string { return fmt.Sprintf("0x%02X%02X%02X", c.R, c.G, c.B) }

// ass è il colore nel formato .ass &HAABBGGRR (alpha 0 = opaco).
func (c Color) ass(alpha uint8) string {
	return fmt.Sprintf("&H%02X%02X%02X%02X", alpha, c.B, c.G, c.R)
}

// Position è una posizione nel quadro, dentro i margini sicuri.
type Position string

// Posizioni ammesse.
const (
	TopLeft      Position = "alto-sinistra"
	Top          Position = "alto"
	TopRight     Position = "alto-destra"
	Left         Position = "sinistra"
	Center       Position = "centro"
	Right        Position = "destra"
	BottomLeft   Position = "basso-sinistra"
	Bottom       Position = "basso"
	BottomRight  Position = "basso-destra"
	positionList          = "alto-sinistra, alto, alto-destra, sinistra, centro, destra, basso-sinistra, basso, basso-destra"
)

// align è l'allineamento .ass (tastierino numerico) di ogni posizione.
var align = map[Position]int{
	BottomLeft: 1, Bottom: 2, BottomRight: 3,
	Left: 4, Center: 5, Right: 6,
	TopLeft: 7, Top: 8, TopRight: 9,
}

// ParsePosition legge una posizione dall'elenco chiuso.
func ParsePosition(s string) (Position, error) {
	p := Position(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := align[p]; !ok {
		return "", fmt.Errorf("posizione %q sconosciuta (valide: %s)", s, positionList)
	}
	return p, nil
}

// Align è l'allineamento .ass della posizione (1-9, tastierino numerico); 0 se sconosciuta.
func (p Position) Align() int { return align[p] }

// Place è l'angolo in alto a sinistra (x, y) di un oggetto ow×oh messo in p
// in un quadro w×h, dentro i margini m (per il filtro overlay).
func (p Position) Place(w, h, ow, oh int, m Margins) (x, y int) {
	t, b, l, r := m.Pixels(w, h)
	a := p.Align()
	switch (a - 1) % 3 {
	case 0:
		x = l
	case 1:
		x = l + (w-l-r-ow)/2
	default:
		x = w - r - ow
	}
	switch (a - 1) / 3 {
	case 2:
		y = t
	case 1:
		y = t + (h-t-b-oh)/2
	default:
		y = h - b - oh
	}
	return x, y
}

// Margins sono i margini sicuri, in frazioni del lato (0,14 = 14%).
type Margins struct{ Top, Bottom, Left, Right float64 }

// Margini sicuri di default per formato. Ufficiale solo il 9:16 (guida
// annunci Meta per i Reels: 14% in alto, 35% in basso, 6% ai lati); per gli
// altri formati il 5% per lato, una convenzione.
var (
	SafeVertical = Margins{Top: 0.14, Bottom: 0.35, Left: 0.06, Right: 0.06}
	SafeDefault  = Margins{Top: 0.05, Bottom: 0.05, Left: 0.05, Right: 0.05}
)

// SafeMargins sono i margini sicuri di default per un quadro w×h: quelli del
// 9:16 per i verticali stretti (rapporto entro il 2% di 9:16), altrimenti il
// 5% per lato.
func SafeMargins(w, h int) Margins {
	if w > 0 && h > 0 && math.Abs(float64(w)/float64(h)-9.0/16.0) <= 0.02*9.0/16.0 {
		return SafeVertical
	}
	return SafeDefault
}

// Check controlla i margini: ognuno da 0 a 0,45, e la somma dei due
// opposti sotto 0,9 (resta almeno il 10% del quadro).
func (m Margins) Check() error {
	for _, v := range []float64{m.Top, m.Bottom, m.Left, m.Right} {
		if !finite(v) || v < 0 || v > 0.45 {
			return fmt.Errorf("margine %v: da 0 a 0.45 (frazione del lato)", v)
		}
	}
	if m.Top+m.Bottom >= 0.9 || m.Left+m.Right >= 0.9 {
		return errors.New("margini troppo grandi: resta meno del 10% del quadro")
	}
	return nil
}

// Pixels sono i margini in pixel per un quadro w×h (alto, basso, sinistra, destra).
func (m Margins) Pixels(w, h int) (top, bottom, left, right int) {
	px := func(f float64, side int) int { return int(math.Round(f * float64(side))) }
	return px(m.Top, h), px(m.Bottom, h), px(m.Left, w), px(m.Right, w)
}

// TextStyle è lo stile di un testo.
type TextStyle struct {
	Font    string  // famiglia (FontFamily del file del font); vuoto: DefaultFont
	Size    int     // altezza dei caratteri in pixel del quadro
	Color   Color   // colore del testo
	Outline Color   // colore del bordo
	Border  float64 // spessore del bordo in pixel; 0: nessun bordo
}

// DefaultTextStyle è lo stile di default per un quadro alto h pixel: testo
// bianco con bordo nero, alto 1/18 del quadro.
func DefaultTextStyle(h int) TextStyle {
	size := max(h/18, 12)
	return TextStyle{Size: size, Color: White, Outline: Black, Border: math.Max(1, math.Round(float64(size)/16))}
}

// Text è un testo a tempo.
type Text struct {
	Text  string    // reso alla lettera; un a capo va a riga nuova
	Start float64   // secondi dall'inizio del video
	End   float64   // secondi, oltre Start
	Pos   Position  // posizione nel quadro, dentro i margini
	Style TextStyle // stile
	Fade  float64   // dissolvenza in entrata e in uscita, secondi (0: nessuna)
	// MarginV è la distanza in pixel dal bordo alto (posizioni in alto) o
	// basso (in basso) al posto del margine sicuro; 0: quello dei margini.
	// Al centro non conta.
	MarginV int
}

// ASS genera il file .ass dei testi per un video w×h, coi margini sicuri m.
// Ogni testo è reso alla lettera (EscapeASS); font, posizioni e tempi sono
// controllati (ErrInvalid).
func ASS(w, h int, m Margins, texts []Text) ([]byte, error) {
	if w < 16 || w > maxSide || h < 16 || h > maxSide {
		return nil, invalid("testi: quadro %dx%d fuori dai limiti", w, h)
	}
	if err := m.Check(); err != nil {
		return nil, invalid("testi: %v", err)
	}
	if len(texts) == 0 {
		return nil, invalid("testi: nessun testo")
	}
	top, bottom, left, right := m.Pixels(w, h)
	type styleKey struct {
		s TextStyle
		p Position
	}
	names := map[styleKey]string{}
	var styles, events strings.Builder
	for i, t := range texts {
		n := i + 1
		if t.Pos.Align() == 0 {
			return nil, invalid("testo %d: posizione %q sconosciuta (valide: %s)", n, t.Pos, positionList)
		}
		if !finite(t.Start) || !finite(t.End) || t.Start < 0 || t.End <= t.Start || t.End > 24*3600 {
			return nil, invalid("testo %d: tempi da %v a %v non validi", n, t.Start, t.End)
		}
		if !finite(t.Fade) || t.Fade < 0 || 2*t.Fade > t.End-t.Start {
			return nil, invalid("testo %d: dissolvenza di %v s più lunga di metà del testo", n, t.Fade)
		}
		st := t.Style
		if st.Font == "" {
			st.Font = DefaultFont
		}
		if err := checkFontName(st.Font); err != nil {
			return nil, invalid("testo %d: %v", n, err)
		}
		if st.Size < 1 || st.Size > h {
			return nil, invalid("testo %d: dimensione %d: da 1 a %d pixel", n, st.Size, h)
		}
		if !finite(st.Border) || st.Border < 0 || st.Border > 100 {
			return nil, invalid("testo %d: bordo %v: da 0 a 100 pixel", n, st.Border)
		}
		if t.MarginV < 0 || t.MarginV > h {
			return nil, invalid("testo %d: margine verticale %d: da 0 a %d pixel", n, t.MarginV, h)
		}
		k := styleKey{st, t.Pos}
		name, ok := names[k]
		if !ok {
			name = "T" + strconv.Itoa(len(names)+1)
			names[k] = name
			mv := bottom
			switch (t.Pos.Align() - 1) / 3 {
			case 2:
				mv = top
			case 1:
				mv = 0 // al centro il margine verticale non conta
			}
			fmt.Fprintf(&styles, "Style: %s,%s,%d,%s,%s,%s,%s,0,0,0,0,100,100,0,0,1,%s,0,%d,%d,%d,%d,1\n",
				name, st.Font, st.Size, st.Color.ass(0), st.Color.ass(0), st.Outline.ass(0), Black.ass(0x80),
				num(st.Border), t.Pos.Align(), left, right, mv)
		}
		text := EscapeASS(t.Text)
		if t.Fade > 0 {
			ms := strconv.Itoa(int(math.Round(t.Fade * 1000)))
			text = `{\fad(` + ms + "," + ms + ")}" + text
		}
		fmt.Fprintf(&events, "Dialogue: 0,%s,%s,%s,,0,0,%d,,%s\n",
			formatASSTime(centis(t.Start)), formatASSTime(centis(t.End)), name, t.MarginV, text)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Script Info]\n; generato da giv\nScriptType: v4.00+\nPlayResX: %d\nPlayResY: %d\nWrapStyle: 0\nScaledBorderAndShadow: yes\nYCbCr Matrix: None\n\n", w, h)
	b.WriteString("[V4+ Styles]\nFormat: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	b.WriteString(styles.String())
	b.WriteString("\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	b.WriteString(events.String())
	return []byte(b.String()), nil
}

// wordJoiner (U+2060) è invisibile: messo fra \ e N, n o h impedisce che
// diventino un a capo o uno spazio.
const wordJoiner = "⁠"

// EscapeASS rende s alla lettera nel testo di una riga Dialogue. Prima si
// normalizza: ogni a capo (\n, \r\n, \r, U+0085, U+2028, U+2029) diventa
// un solo a capo, la tabulazione uno spazio, gli altri caratteri di
// controllo si tolgono. Poi, sul testo normalizzato: { e } diventano \{ e \}
// (graffe visibili, nessun blocco di override), una \ davanti a N, n o h è
// seguita da un carattere invisibile, l'a capo diventa \N.
func EscapeASS(s string) string {
	var rs []rune
	for _, r := range strings.ReplaceAll(s, "\r\n", "\n") {
		switch {
		case r == '\n' || r == '\r' || r == 0x85 || r == 0x2028 || r == 0x2029:
			rs = append(rs, '\n')
		case r == '\t':
			rs = append(rs, ' ')
		case r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0:
		default:
			rs = append(rs, r)
		}
	}
	var b strings.Builder
	for i, r := range rs {
		switch r {
		case '{':
			b.WriteString(`\{`)
		case '}':
			b.WriteString(`\}`)
		case '\\':
			b.WriteByte('\\')
			if i+1 < len(rs) && strings.ContainsRune("Nnh", rs[i+1]) {
				b.WriteString(wordJoiner)
			}
		case '\n':
			b.WriteString(`\N`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// checkFontName rifiuta una famiglia che non sta in una riga Style: virgole,
// caratteri di controllo, @ iniziale (testo verticale), oltre 200 byte.
func checkFontName(f string) error {
	if f == "" || len(f) > 200 || strings.HasPrefix(f, "@") || strings.TrimSpace(f) != f {
		return fmt.Errorf("font %q non valido", f)
	}
	for _, r := range f {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 || r == ',' || r == 0x2028 || r == 0x2029 {
			return fmt.Errorf("font %q: nome con virgole o caratteri di controllo", f)
		}
	}
	return nil
}

// ASSFilter è il filtro che incide il file .ass (scritto con Work.WriteFile);
// fontsDir, facoltativa, è la cartella dei font (Work.Fonts). I percorsi sono
// protetti con filterValue.
func ASSFilter(assPath, fontsDir string) (string, error) {
	if err := checkPath(assPath); err != nil {
		return "", invalid("testi: %v", err)
	}
	a, err := filepath.Abs(assPath)
	if err != nil {
		return "", err
	}
	f := "ass=filename=" + filterValue(a)
	if fontsDir != "" {
		if err := checkPath(fontsDir); err != nil {
			return "", invalid("font: %v", err)
		}
		d, err := filepath.Abs(fontsDir)
		if err != nil {
			return "", err
		}
		f += ":fontsdir=" + filterValue(d)
	}
	return f, nil
}

// Fonts copia i file di font (.ttf, .otf, .ttc, al più 64 MiB l'uno) nella
// cartella privata "font" e la restituisce, per ASSFilter: libass vede solo
// quei font, oltre a quelli del sistema.
func (w *Work) Fonts(files ...string) (string, error) {
	dir := filepath.Join(w.Dir, "font")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	for i, src := range files {
		ext := strings.ToLower(filepath.Ext(src))
		if !fontExt[ext] {
			return "", invalid("font %s: serve un file .ttf, .otf o .ttc", src)
		}
		data, err := readFont(src)
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "font-"+strconv.Itoa(i+1)+ext), data, 0o600); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// FontFamily legge dal file del font (.ttf, .otf, .ttc: il primo) la
// famiglia da usare in TextStyle.Font: il nome di famiglia (ID 1) della
// piattaforma Windows, quello che cerca libass, in inglese se c'è.
func FontFamily(path string) (string, error) {
	if !fontExt[strings.ToLower(filepath.Ext(path))] {
		return "", invalid("font %s: serve un file .ttf, .otf o .ttc", path)
	}
	data, err := readFont(path)
	if err != nil {
		return "", err
	}
	name, err := fontFamily(data)
	if err != nil {
		return "", invalid("font %s: %v", path, err)
	}
	if err := checkFontName(name); err != nil {
		return "", invalid("font %s: %v", path, err)
	}
	return name, nil
}

// readFont legge un file di font normale, al più maxFontBytes.
func readFont(path string) ([]byte, error) {
	if err := checkPath(path); err != nil {
		return nil, invalid("font: %v", err)
	}
	if err := regularFile(path); err != nil {
		return nil, invalid("font: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFontBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFontBytes {
		return nil, invalid("font %s: oltre %d MiB", path, maxFontBytes>>20)
	}
	return data, nil
}

var errFont = errors.New("file di font non leggibile")

// fontFamily legge la tabella name di un font sfnt (TrueType, OpenType, la
// prima faccia di una collezione), controllando ogni lettura sui limiti.
func fontFamily(data []byte) (string, error) {
	be16 := func(o uint64) (uint64, bool) {
		if o+2 > uint64(len(data)) {
			return 0, false
		}
		return uint64(binary.BigEndian.Uint16(data[o:])), true
	}
	be32 := func(o uint64) (uint64, bool) {
		if o+4 > uint64(len(data)) {
			return 0, false
		}
		return uint64(binary.BigEndian.Uint32(data[o:])), true
	}
	var off uint64
	if len(data) >= 4 && string(data[:4]) == "ttcf" {
		v, ok := be32(12)
		if !ok {
			return "", errFont
		}
		off = v
	}
	tag, ok := be32(off)
	if !ok || tag != 0x00010000 && tag != 0x4F54544F && tag != 0x74727565 { // 1.0, "OTTO", "true"
		return "", errFont
	}
	count, ok := be16(off + 4)
	if !ok {
		return "", errFont
	}
	for i := range count {
		rec := off + 12 + 16*i
		if rec+16 > uint64(len(data)) {
			return "", errFont
		}
		if string(data[rec:rec+4]) != "name" {
			continue
		}
		to, _ := be32(rec + 8)
		tl, _ := be32(rec + 12)
		if to+tl > uint64(len(data)) {
			return "", errFont
		}
		return nameFamily(data[to : to+tl])
	}
	return "", errors.New("tabella dei nomi assente")
}

// maxFamilyBytes è il tetto di un nome di famiglia letto (UTF-16): i record
// più lunghi si saltano.
const maxFamilyBytes = 512

// nameFamily cerca nella tabella name il nome di famiglia (ID 1) della
// piattaforma Windows (3), codifica Unicode: il primo valido in inglese
// (0x409), altrimenti il primo valido. Si decodificano solo i record che
// migliorerebbero la scelta, ognuno di al più maxFamilyBytes.
func nameFamily(t []byte) (string, error) {
	if len(t) < 6 {
		return "", errFont
	}
	count := int(binary.BigEndian.Uint16(t[2:]))
	strOff := int(binary.BigEndian.Uint16(t[4:]))
	best, bestRank := "", 0
	for i := range count {
		r := 6 + 12*i
		if r+12 > len(t) {
			break
		}
		plat := binary.BigEndian.Uint16(t[r:])
		enc := binary.BigEndian.Uint16(t[r+2:])
		lang := binary.BigEndian.Uint16(t[r+4:])
		id := binary.BigEndian.Uint16(t[r+6:])
		ln := int(binary.BigEndian.Uint16(t[r+8:]))
		o := int(binary.BigEndian.Uint16(t[r+10:]))
		if plat != 3 || id != 1 || enc != 0 && enc != 1 && enc != 10 || ln%2 != 0 || ln > maxFamilyBytes {
			continue
		}
		rank := 1
		if lang == 0x409 {
			rank = 2
		}
		if rank <= bestRank {
			continue
		}
		s := strOff + o
		if s+ln > len(t) {
			continue
		}
		u := make([]uint16, ln/2)
		for j := range u {
			u[j] = binary.BigEndian.Uint16(t[s+2*j:])
		}
		name := strings.TrimSpace(string(utf16.Decode(u)))
		if name == "" {
			continue
		}
		best, bestRank = name, rank
		if rank == 2 {
			break
		}
	}
	if best == "" {
		return "", errors.New("nessun nome di famiglia leggibile")
	}
	return best, nil
}
