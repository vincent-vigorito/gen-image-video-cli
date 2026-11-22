// Package kit legge e controlla il kit di marca e personaggi di giv: un file
// JSON (di solito kit.json) con logo, colori, font, cartello finale, musica,
// immagini e descrizione dei personaggi, voce e pronunce, scritto una volta e
// richiamato dagli storyboard.
//
// Il kit è in sola lettura e chiuso nella sua cartella (quella del file JSON):
// ogni file è un percorso relativo a quella cartella e non ne esce, nemmeno
// seguendo i link; solo file normali, niente URL. Il pacchetto legge e basta:
// non scrive niente, né nella cartella del kit né altrove.
package kit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"
)

// MaxSize è il tetto del file del kit.
const MaxSize = 1 << 20

// Kit è il file del kit di marca e personaggi.
type Kit struct {
	Marca      *Marca                 `json:"marca,omitempty"`
	Personaggi map[string]Personaggio `json:"personaggi,omitempty"`
	Voce       *Voce                  `json:"voce,omitempty"`
	Pronuncia  map[string]string      `json:"pronuncia,omitempty"` // parola scritta -> come si pronuncia

	// Dir è la cartella del kit (quella del file JSON): i file sono relativi a lei.
	Dir string `json:"-"`
}

// Marca sono i predefiniti del montaggio. Logo, StileTesti, CartelloFinale e
// Musica hanno gli stessi campi del blocco montaggio dello storyboard, con i
// file relativi alla cartella del kit.
type Marca struct {
	Colori         *Colori     `json:"colori,omitempty"`
	Logo           *Logo       `json:"logo,omitempty"`
	StileTesti     *StileTesti `json:"stile_testi,omitempty"`
	CartelloFinale *Cartello   `json:"cartello_finale,omitempty"`
	Musica         *Musica     `json:"musica,omitempty"`
	Volume         *float64    `json:"volume,omitempty"` // volume finale in LUFS
}

// Colori sono i colori della marca, predefiniti del montaggio: Primario è lo
// sfondo del cartello e il bordo dei testi, Testo il colore dei testi.
type Colori struct {
	Primario string `json:"primario,omitempty"` // #RRGGBB
	Testo    string `json:"testo,omitempty"`    // #RRGGBB
}

// Logo è il logo posato sul video (montaggio.logo dello storyboard).
type Logo struct {
	File      string   `json:"file"`                // immagine .png, .jpg o .webp
	Posizione string   `json:"posizione,omitempty"` // default alto-destra
	Larghezza *float64 `json:"larghezza,omitempty"` // frazione della larghezza del quadro, oltre 0 e al più 1
	Opacita   *float64 `json:"opacita,omitempty"`   // oltre 0 e al più 1; default 1
	Da        *float64 `json:"da,omitempty"`        // secondi dall'inizio del montato; default 0
	A         *float64 `json:"a,omitempty"`         // secondi; assente: fino alla fine delle scene
}

// Cartello è il cartello finale (montaggio.cartello_finale dello storyboard).
type Cartello struct {
	Durata     *float64 `json:"durata,omitempty"` // secondi; default 3
	Sfondo     string   `json:"sfondo,omitempty"` // colore #RRGGBB
	Logo       string   `json:"logo,omitempty"`   // immagine .png, .jpg o .webp
	Testo      string   `json:"testo,omitempty"`
	Sottotesto string   `json:"sottotesto,omitempty"`
}

// StileTesti è lo stile dei testi (montaggio.stile_testi dello storyboard).
type StileTesti struct {
	Font       string   `json:"font,omitempty"`       // file .ttf, .otf o .ttc nella cartella del kit
	Colore     string   `json:"colore,omitempty"`     // #RRGGBB
	Bordo      string   `json:"bordo,omitempty"`      // colore del contorno #RRGGBB, o "nessuno"
	Dimensione *float64 `json:"dimensione,omitempty"` // altezza dei caratteri in frazione dell'altezza del quadro
}

// Musica è la musica di sottofondo (montaggio.musica dello storyboard).
type Musica struct {
	File      string   `json:"file"`
	Volume    *float64 `json:"volume,omitempty"`     // 0-1
	SottoVoce bool     `json:"sotto_voce,omitempty"` // si abbassa mentre parla la voce
	SfumaFine *float64 `json:"sfuma_fine,omitempty"` // secondi di dissolvenza in chiusura
}

// Personaggio è un insieme di immagini di riferimento con un nome, usabile in
// usa come gli insiemi di riferimenti dello storyboard.
type Personaggio struct {
	Riferimenti []string `json:"riferimenti"`           // immagini nella cartella del kit
	Descrizione string   `json:"descrizione,omitempty"` // va davanti al prompt delle scene che lo usano
	Tutela      string   `json:"tutela,omitempty"`      // per le scene che lo usano e non ne indicano una
}

// Voce sono i predefiniti della voce fuori campo (blocco parlato dello storyboard).
type Voce struct {
	Provider string   `json:"provider,omitempty"` // xai o gemini
	Modello  string   `json:"modello,omitempty"`
	Voce     string   `json:"voce,omitempty"`     // nome della voce del provider
	Lingua   string   `json:"lingua,omitempty"`   // es. it, it-IT
	Velocita *float64 `json:"velocita,omitempty"` // da MinVelocita a MaxVelocita
	Stile    string   `json:"stile,omitempty"`    // a parole
}

// Valori ammessi e limiti dei campi del kit: quelli del blocco montaggio dello
// storyboard, più personaggi, voce e pronunce.
var (
	Posizioni     = []string{"alto-sinistra", "alto", "alto-destra", "sinistra", "centro", "destra", "basso-sinistra", "basso", "basso-destra"}
	VoceProviders = []string{"xai", "gemini"}
)

const (
	maxTestoBytes    = 2000
	maxCartello      = 60 // secondi
	maxSfumaFine     = 60 // secondi
	minLUFS, maxLUFS = -70, -5
	minDimensione    = 0.01
	maxDimensione    = 0.25

	MinVelocita = 0.7 // velocità della voce
	MaxVelocita = 1.5

	MaxPersonaggi  = 50
	MaxRiferimenti = 50  // immagini per personaggio (quante ne accettano i modelli: giv kit check avvisa)
	MaxPronunce    = 200 // voci del dizionario (il tetto di xAI)
	MaxParolaBytes = 100 // parola e pronuncia di una voce del dizionario

	// DescrizioneLunga è la lunghezza, in caratteri, oltre la quale la
	// descrizione di un personaggio allunga troppo il prompt: si avvisa.
	DescrizioneLunga = 500
)

// Load legge e controlla il kit in path: campi sconosciuti, valori e file.
// Tutti i problemi trovati sono nell'errore, uno per riga, senza il percorso
// del kit (lo aggiunge chi lo mostra); un kit che non c'è è un errore con
// fs.ErrNotExist.
func Load(path string) (*Kit, error) {
	data, err := readLimited(path)
	if err != nil {
		return nil, err
	}
	k, err := Parse(data)
	if err != nil {
		return nil, err
	}
	k.Dir = filepath.Dir(path)
	if err := k.Validate(); err != nil {
		return nil, err
	}
	return k, nil
}

// readLimited legge il file del kit: un file normale (anche dietro un link),
// al più MaxSize byte. L'apertura è senza blocco (una FIFO o un device non
// fermano la CLI) e il tipo si controlla sul descrittore aperto.
func readLimited(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("kit non trovato: %w", fs.ErrNotExist)
		}
		return nil, errors.New("kit non leggibile")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("il kit deve essere un file normale")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, errors.New("kit non leggibile")
	}
	if len(data) > MaxSize {
		return nil, fmt.Errorf("oltre %d MiB, non è un kit", MaxSize>>20)
	}
	return data, nil
}

// Parse decodifica un kit: un solo oggetto JSON, nessun campo sconosciuto.
func Parse(data []byte) (*Kit, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var k Kit
	if err := dec.Decode(&k); err != nil {
		return nil, jsonError(data, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("JSON non valido: altro testo dopo il kit")
	}
	return &k, nil
}

// jsonError rende leggibile un errore di decodifica, con la riga dove si può.
func jsonError(data []byte, err error) error {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	switch {
	case errors.As(err, &se):
		return fmt.Errorf("JSON non valido alla riga %d: %s", line(data, se.Offset), se.Error())
	case errors.As(err, &te):
		return fmt.Errorf("JSON non valido alla riga %d: il campo %s vuole %s, non %s", line(data, te.Offset), te.Field, te.Type, te.Value)
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("JSON non valido: file vuoto o troncato")
	}
	msg := strings.TrimPrefix(err.Error(), "json: ")
	if f, ok := strings.CutPrefix(msg, "unknown field "); ok {
		return fmt.Errorf("campo sconosciuto %s", f)
	}
	return fmt.Errorf("JSON non valido: %s", msg)
}

func line(data []byte, off int64) int {
	off = min(max(off, 0), int64(len(data)))
	return 1 + bytes.Count(data[:off], []byte("\n"))
}

var (
	// nameRe sono i nomi dei personaggi: usati in usa e nei messaggi.
	nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	// tokenRe sono tutela, nome della voce e modello: parole semplici.
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)
	// linguaRe è un codice di lingua (it, it-IT).
	linguaRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)
	// colorRe è un colore #RRGGBB.
	colorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// Validate controlla il kit già letto; Dir deve essere impostata.
func (k *Kit) Validate() error {
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	file := func(where, p string) {
		if err := k.CheckPath(p); err != nil {
			fail("%s: %v", where, err)
		}
	}
	if k.Marca == nil && len(k.Personaggi) == 0 && k.Voce == nil && len(k.Pronuncia) == 0 {
		fail("kit vuoto: serve almeno uno fra marca, personaggi, voce e pronuncia")
	}
	if m := k.Marca; m != nil {
		validateMarca(m, fail, file)
	}
	if len(k.Personaggi) > MaxPersonaggi {
		fail("personaggi: al più %d", MaxPersonaggi)
	}
	for _, name := range sortedKeys(k.Personaggi) {
		p := k.Personaggi[name]
		where := "personaggi." + name
		if !nameRe.MatchString(name) {
			where = fmt.Sprintf("personaggi %q", name)
			fail("%s: nome non valido (lettere, cifre, _ e -, al massimo 64)", where)
		}
		switch n := len(p.Riferimenti); {
		case n == 0:
			fail("%s.riferimenti: serve almeno un'immagine", where)
		case n > MaxRiferimenti:
			fail("%s.riferimenti: al più %d immagini", where, MaxRiferimenti)
		}
		seen := map[string]bool{}
		for i, r := range p.Riferimenti {
			w := fmt.Sprintf("%s.riferimenti n. %d", where, i+1)
			if seen[r] {
				fail("%s: %s ripetuto", w, shown(r))
				continue
			}
			seen[r] = true
			imageFile(w, r, fail, file)
		}
		text(where+".descrizione", p.Descrizione, false, fail)
		if p.Tutela != "" && !tokenRe.MatchString(p.Tutela) {
			fail("%s.tutela %q non valida (prodotto, illustrazione, no o una clausola dei preset)", where, p.Tutela)
		}
	}
	if v := k.Voce; v != nil {
		validateVoce(v, fail)
	}
	if len(k.Pronuncia) > MaxPronunce {
		fail("pronuncia: al più %d parole", MaxPronunce)
	}
	for _, w := range sortedKeys(k.Pronuncia) {
		where := fmt.Sprintf("pronuncia %q", w)
		word(where+": parola", w, fail)
		word(where+": pronuncia", k.Pronuncia[w], fail)
	}
	return errors.Join(errs...)
}

// validateMarca controlla i predefiniti del montaggio con le regole del blocco
// montaggio dello storyboard: valori da elenco chiuso, numeri negli
// intervalli, file nella cartella del kit.
func validateMarca(m *Marca, fail func(string, ...any), file func(string, string)) {
	if c := m.Colori; c != nil {
		color("marca.colori.primario", c.Primario, fail)
		color("marca.colori.testo", c.Testo, fail)
	}
	if l := m.Logo; l != nil {
		where := "marca.logo"
		imageFile(where, l.File, fail, file)
		position(where+".posizione", l.Posizione, fail)
		if v := l.Larghezza; v != nil && (!unit(*v) || *v == 0) {
			fail("%s.larghezza deve essere una frazione della larghezza del quadro, oltre 0 e al più 1", where)
		}
		if v := l.Opacita; v != nil && (!unit(*v) || *v == 0) {
			fail("%s.opacita deve essere un numero oltre 0 e al più 1", where)
		}
		if v := l.Da; v != nil && !nonNegative(*v) {
			fail("%s.da deve essere un numero di secondi finito e non negativo", where)
		}
		if v := l.A; v != nil && (!nonNegative(*v) || *v <= 0 || (l.Da != nil && *v <= *l.Da)) {
			fail("%s.a deve essere un numero di secondi dopo %s.da", where, where)
		}
	}
	if st := m.StileTesti; st != nil {
		where := "marca.stile_testi"
		if st.Font != "" {
			switch strings.ToLower(filepath.Ext(st.Font)) {
			case ".ttf", ".otf", ".ttc":
				file(where+".font", st.Font)
			default:
				if isURL(st.Font) {
					file(where+".font", st.Font) // rifiutato da CheckPath, con l'URL ridotto
					break
				}
				fail("%s.font %s: serve un file di font .ttf, .otf o .ttc", where, st.Font)
			}
		}
		color(where+".colore", st.Colore, fail)
		if st.Bordo != "nessuno" {
			color(where+".bordo", st.Bordo, fail)
		}
		if d := st.Dimensione; d != nil && (!nonNegative(*d) || *d < minDimensione || *d > maxDimensione) {
			fail("%s.dimensione deve essere una frazione dell'altezza del quadro da %g a %g (es. 0.055)", where, minDimensione, maxDimensione)
		}
	}
	if c := m.CartelloFinale; c != nil {
		where := "marca.cartello_finale"
		if d := c.Durata; d != nil && (!nonNegative(*d) || *d == 0 || *d > maxCartello) {
			fail("%s.durata deve essere un numero di secondi oltre 0 e al più %d", where, maxCartello)
		}
		color(where+".sfondo", c.Sfondo, fail)
		if c.Logo != "" {
			imageFile(where+".logo", c.Logo, fail, file)
		}
		text(where+".testo", c.Testo, false, fail)
		text(where+".sottotesto", c.Sottotesto, false, fail)
		if c.Logo == "" && c.Testo == "" && c.Sottotesto == "" {
			fail("%s: serve almeno uno fra logo, testo e sottotesto", where)
		}
	}
	if mu := m.Musica; mu != nil {
		where := "marca.musica"
		if mu.File == "" {
			fail("%s: file mancante", where)
		} else {
			file(where, mu.File)
		}
		if v := mu.Volume; v != nil && (!nonNegative(*v) || *v > 1) {
			fail("%s.volume deve essere fra 0 e 1", where)
		}
		if f := mu.SfumaFine; f != nil && (!nonNegative(*f) || *f > maxSfumaFine) {
			fail("%s.sfuma_fine deve essere un numero di secondi da 0 a %d", where, maxSfumaFine)
		}
	}
	if v := m.Volume; v != nil && (!nonNegative(-*v) || *v < minLUFS || *v > maxLUFS) {
		fail("marca.volume deve essere un livello in LUFS da %d a %d (es. -14)", minLUFS, maxLUFS)
	}
}

// validateVoce controlla i predefiniti della voce: provider, nomi e numeri.
// Che voce e modello esistano lo dice il provider al momento della richiesta.
func validateVoce(v *Voce, fail func(string, ...any)) {
	if v.Provider != "" && !slices.Contains(VoceProviders, v.Provider) {
		fail("voce.provider %q sconosciuto (%s)", v.Provider, strings.Join(VoceProviders, " o "))
	}
	if v.Modello != "" && !tokenRe.MatchString(v.Modello) {
		fail("voce.modello %q non valido", v.Modello)
	}
	if v.Voce != "" && !tokenRe.MatchString(v.Voce) {
		fail("voce.voce %q non valida (il nome di una voce del provider)", v.Voce)
	}
	if v.Lingua != "" && !linguaRe.MatchString(v.Lingua) {
		fail("voce.lingua %q non valida (es. it o it-IT)", v.Lingua)
	}
	if s := v.Velocita; s != nil && (!nonNegative(*s) || *s < MinVelocita || *s > MaxVelocita) {
		fail("voce.velocita deve essere un numero da %g a %g (1 = normale)", MinVelocita, MaxVelocita)
	}
	text("voce.stile", v.Stile, false, fail)
}

// File è un file del kit, col campo che lo nomina.
type File struct {
	Campo string // es. marca.logo, personaggi.anja.riferimenti n. 1
	File  string // come scritto nel kit, relativo alla sua cartella
	Tipo  string // logo, font, musica o riferimento
}

// Tipi di File.
const (
	TipoLogo        = "logo"
	TipoFont        = "font"
	TipoMusica      = "musica"
	TipoRiferimento = "riferimento"
)

// Files elenca i file nominati dal kit, nell'ordine del kit (personaggi per
// nome); un file nominato da due campi compare due volte.
func (k *Kit) Files() []File {
	var out []File
	if m := k.Marca; m != nil {
		if m.Logo != nil && m.Logo.File != "" {
			out = append(out, File{"marca.logo", m.Logo.File, TipoLogo})
		}
		if st := m.StileTesti; st != nil && st.Font != "" {
			out = append(out, File{"marca.stile_testi.font", st.Font, TipoFont})
		}
		if c := m.CartelloFinale; c != nil && c.Logo != "" {
			out = append(out, File{"marca.cartello_finale.logo", c.Logo, TipoLogo})
		}
		if mu := m.Musica; mu != nil && mu.File != "" {
			out = append(out, File{"marca.musica", mu.File, TipoMusica})
		}
	}
	for _, name := range sortedKeys(k.Personaggi) {
		for i, r := range k.Personaggi[name].Riferimenti {
			out = append(out, File{fmt.Sprintf("personaggi.%s.riferimenti n. %d", name, i+1), r, TipoRiferimento})
		}
	}
	return out
}

// Riferimenti sono le immagini del personaggio name come percorsi rispetto
// alla cartella corrente (Path); nil se il kit non ha quel personaggio.
func (k *Kit) Riferimenti(name string) []string {
	p, ok := k.Personaggi[name]
	if !ok {
		return nil
	}
	out := make([]string, len(p.Riferimenti))
	for i, r := range p.Riferimenti {
		out[i] = k.Path(r)
	}
	return out
}

// Path è il percorso di p rispetto alla cartella corrente: i file del kit sono
// relativi alla sua cartella. Va usato solo su percorsi già controllati
// (CheckPath, Validate).
func (k *Kit) Path(p string) string {
	if p == "" {
		return p
	}
	return filepath.Join(k.Dir, filepath.FromSlash(p))
}

// CheckPath controlla un file del kit: niente URL, percorso relativo senza
// caratteri di controllo, dentro la cartella del kit anche seguendo i link,
// esistente e normale.
func (k *Kit) CheckPath(p string) error {
	if isURL(p) {
		return fmt.Errorf("%s: nel kit solo file della sua cartella, non URL", redact(p))
	}
	if p == "" {
		return errors.New("percorso vuoto")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%q: percorso con caratteri di controllo", p)
		}
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return fmt.Errorf("%s: percorso assoluto, serve un percorso relativo alla cartella del kit", p)
	}
	if !filepath.IsLocal(filepath.FromSlash(p)) {
		return fmt.Errorf("%s: il percorso deve restare nella cartella del kit", p)
	}
	full := k.Path(p)
	st, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("%s: file mancante", p)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s: non è un file", p)
	}
	root, err1 := filepath.EvalSymlinks(k.Dir)
	real, err2 := filepath.EvalSymlinks(full)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("%s: percorso non leggibile", p)
	}
	if rel, err := filepath.Rel(root, real); err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("%s: il link porta fuori dalla cartella del kit", p)
	}
	return nil
}

// imageFile controlla un'immagine (logo, riferimento) nella cartella del kit.
func imageFile(where, p string, fail func(string, ...any), file func(string, string)) {
	if p == "" {
		fail("%s: file mancante", where)
		return
	}
	if isURL(p) {
		file(where, p) // rifiutato da CheckPath, con l'URL ridotto
		return
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		file(where, p)
	default:
		fail("%s %s: serve un'immagine .png, .jpg o .webp", where, p)
	}
}

// position controlla una posizione nel quadro (vuota: quella di default).
func position(where, p string, fail func(string, ...any)) {
	if p != "" && !slices.Contains(Posizioni, p) {
		fail("%s %q sconosciuta (%s)", where, p, strings.Join(Posizioni, ", "))
	}
}

// color controlla un colore (vuoto: quello di default).
func color(where, c string, fail func(string, ...any)) {
	if c != "" && !colorRe.MatchString(c) {
		fail("%s %q: serve un colore #RRGGBB", where, c)
	}
}

// text controlla un testo: reso alla lettera, al più maxTestoBytes, UTF-8
// valido; required: non vuoto.
func text(where, t string, required bool, fail func(string, ...any)) {
	switch {
	case required && strings.TrimSpace(t) == "":
		fail("%s mancante", where)
	case len(t) > maxTestoBytes:
		fail("%s: al più %d byte", where, maxTestoBytes)
	case !utf8.ValidString(t):
		fail("%s: testo non UTF-8", where)
	}
}

// word controlla una parola del dizionario delle pronunce: non vuota, al più
// MaxParolaBytes, UTF-8 valido, senza a capo né caratteri di controllo.
func word(where, w string, fail func(string, ...any)) {
	switch {
	case strings.TrimSpace(w) == "":
		fail("%s vuota", where)
	case len(w) > MaxParolaBytes:
		fail("%s: al più %d byte", where, MaxParolaBytes)
	case !utf8.ValidString(w):
		fail("%s: testo non UTF-8", where)
	case strings.ContainsFunc(w, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		fail("%s: a capo e caratteri di controllo non ammessi", where)
	}
}

// isURL dice se p è un indirizzo http(s) invece di un file.
func isURL(p string) bool {
	return strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://")
}

// shown è p da mostrare in un messaggio: un URL ridotto a schema e host.
func shown(p string) string {
	if isURL(p) {
		return redact(p)
	}
	return p
}

// redact lascia di un URL solo schema e host: percorso e query possono contenere token.
func redact(p string) string {
	scheme, rest, _ := strings.Cut(p, "://")
	host, _, _ := strings.Cut(rest, "/")
	host, _, _ = strings.Cut(host, "?")
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	return scheme + "://" + host + "/…"
}

// unit: numero finito da 0 a 1.
func unit(v float64) bool { return nonNegative(v) && v <= 1 }

// nonNegative: numero finito e non negativo.
func nonNegative(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
