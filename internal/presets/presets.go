// Package presets è la libreria dei preset di camera ed effetti di giv: frasi
// da aggiungere al prompt, con varianti per famiglia di modello. Nessuna API
// video ha un parametro di movimento di camera, quindi il preset è solo testo.
//
// La libreria incorporata (presets.json) si estende con i file dell'utente
// ./giv-presets.json e ~/.config/giv/presets.json, nello stesso ordine delle
// credenziali: vince il primo che definisce un preset (o una tutela) con quell'id,
// e il preset dell'utente sostituisce per intero quello incorporato.
package presets

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

//go:embed presets.json
var builtinJSON []byte

// Origine dei preset della libreria incorporata.
const Builtin = "incorporato"

// Tipi di preset: al massimo uno per tipo in una richiesta.
const (
	TypeCamera = "camera"
	TypeEffect = "effetto"
)

// Esiti della prova di un preset su una famiglia (li cambia solo il provino a pagamento).
const (
	StatusUntested = "non provato"
	StatusOK       = "provato ok"
	StatusPoor     = "provato scarso"
)

// Generic è il nome della variante quando si usa la frase generica.
const Generic = "generica"

const maxPhrase = 300 // lunghezza massima di frasi, negative e tutele (byte)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// family è una famiglia di modelli: si riconosce dal nome del modello (senza il
// prefisso del vendor) e dice se il modello ha il negative prompt.
type family struct {
	name     string
	match    func(base string) bool
	negative bool
}

func prefix(p string) func(string) bool {
	return func(base string) bool { return strings.HasPrefix(base, p) }
}

// families: l'ordine è quello dei testi di aiuto e delle verifiche.
// Negative: Kling e Wan (negative_prompt), Veo (negativePrompt).
var families = []family{
	{"kling", prefix("kling"), true},
	{"veo", prefix("veo"), true},
	{"seedance", prefix("seedance"), false},
	{"grok", prefix("grok"), false},
	{"omni", func(b string) bool { return strings.Contains(b, "omni") }, false},
	{"wan", prefix("wan"), true},
}

// Families restituisce i nomi delle famiglie di modelli note.
func Families() []string {
	out := make([]string, len(families))
	for i, f := range families {
		out[i] = f.name
	}
	return out
}

func findFamily(name string) (family, bool) {
	for _, f := range families {
		if f.name == name {
			return f, true
		}
	}
	return family{}, false
}

// Family ricava la famiglia dal nome del modello ("kwaivgi/kling-v3.0-pro" → "kling",
// "grok-imagine-video-1.5" → "grok"); "" se il modello è vuoto o non riconosciuto.
func Family(model string) string {
	base := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if base == "" {
		return ""
	}
	for _, f := range families {
		if f.match(base) {
			return f.name
		}
	}
	return ""
}

// Requires sono le condizioni in cui il preset rende meglio: se mancano giv avvisa.
type Requires struct {
	FirstFrame  bool `json:"primo_fotogramma,omitempty"`
	LastFrame   bool `json:"ultimo_fotogramma,omitempty"`
	MinDuration int  `json:"durata_min,omitempty"` // secondi
}

// Status è l'esito della prova di un preset su una famiglia.
type Status struct {
	Result string `json:"esito"`
	Date   string `json:"data,omitempty"`    // AAAA-MM-GG
	Model  string `json:"modello,omitempty"` // modello usato nella prova
}

// Preset è un movimento di camera o un effetto.
type Preset struct {
	ID           string            `json:"id"`
	Type         string            `json:"tipo"`
	Description  string            `json:"descrizione"`
	Phrase       string            `json:"frase"`
	Variants     map[string]string `json:"varianti,omitempty"`
	Negative     string            `json:"negative,omitempty"`
	Requires     *Requires         `json:"richiede,omitempty"`
	Incompatible []string          `json:"incompatibile_con,omitempty"`
	// Status ha una voce per ogni famiglia nota: quelle assenti nel file valgono "non provato".
	Status map[string]Status `json:"stato"`
	// Source è "incorporato" o il percorso del file dell'utente che lo definisce.
	Source string `json:"-"`
}

// Library è la libreria caricata: incorporata più i file dell'utente.
type Library struct {
	order   []string
	byID    map[string]*Preset
	protect map[string]string
}

type libraryFile struct {
	Protect map[string]string `json:"tutele,omitempty"`
	Presets []*Preset         `json:"presets"`
}

// UserFiles sono i file dell'utente, dal più forte: ./giv-presets.json, poi
// ~/.config/giv/presets.json.
func UserFiles() []string {
	fs := []string{"giv-presets.json"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		fs = append(fs, filepath.Join(home, ".config", "giv", "presets.json"))
	}
	return fs
}

// Load carica la libreria incorporata e i file dell'utente (UserFiles).
func Load() (*Library, error) {
	return load(builtinJSON, UserFiles())
}

// load applica i file dal più debole al più forte: incorporato, poi files in ordine
// inverso. I file che non esistono si saltano; tutto il resto che non va è un errore.
func load(builtin []byte, files []string) (*Library, error) {
	l := &Library{byID: map[string]*Preset{}, protect: map[string]string{}}
	if err := l.merge(builtin, Builtin); err != nil {
		return nil, err
	}
	for i := len(files) - 1; i >= 0; i-- {
		data, err := os.ReadFile(files[i])
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("file dei preset %s: %v", files[i], err)
		}
		if err := l.merge(data, files[i]); err != nil {
			return nil, err
		}
	}
	if err := l.validate(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *Library) merge(data []byte, source string) error {
	where := "file dei preset " + source
	if source == Builtin {
		where = "libreria incorporata"
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f libraryFile
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("%s: JSON non valido: %v", where, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("%s: JSON non valido: dati dopo l'oggetto principale", where)
	}
	for k, v := range f.Protect {
		if !idRe.MatchString(k) {
			return fmt.Errorf("%s: nome di tutela non valido %q (minuscole, cifre e trattini)", where, k)
		}
		if err := checkPhrase(v); err != nil {
			return fmt.Errorf("%s: tutela %s: %v", where, k, err)
		}
		l.protect[k] = strings.TrimSpace(v)
	}
	seen := map[string]bool{}
	for i, p := range f.Presets {
		if p == nil {
			return fmt.Errorf("%s: preset n. %d vuoto", where, i+1)
		}
		if !idRe.MatchString(p.ID) {
			return fmt.Errorf("%s: preset n. %d: id non valido %q (minuscole, cifre e trattini, max 40)", where, i+1, p.ID)
		}
		if seen[p.ID] {
			return fmt.Errorf("%s: preset %s ripetuto", where, p.ID)
		}
		seen[p.ID] = true
		p.Source = source
		if _, ok := l.byID[p.ID]; !ok {
			l.order = append(l.order, p.ID)
		}
		l.byID[p.ID] = p
	}
	return nil
}

func checkPhrase(s string) error {
	switch {
	case strings.TrimSpace(s) == "":
		return errors.New("testo vuoto")
	case len(s) > maxPhrase:
		return fmt.Errorf("testo troppo lungo (%d byte, max %d)", len(s), maxPhrase)
	}
	return nil
}

// validate controlla la libreria unita e completa lo stato delle famiglie mancanti.
func (l *Library) validate() error {
	known := strings.Join(Families(), ", ")
	for _, id := range l.order {
		p := l.byID[id]
		fail := func(format string, a ...any) error {
			where := "file dei preset " + p.Source
			if p.Source == Builtin {
				where = "libreria incorporata"
			}
			return fmt.Errorf("%s: preset %s: %s", where, p.ID, fmt.Sprintf(format, a...))
		}
		if p.Type != TypeCamera && p.Type != TypeEffect {
			return fail("tipo %q non valido (camera o effetto)", p.Type)
		}
		if strings.TrimSpace(p.Description) == "" {
			return fail("manca la descrizione")
		}
		if err := checkPhrase(p.Phrase); err != nil {
			return fail("frase: %v", err)
		}
		for fam, v := range p.Variants {
			if _, ok := findFamily(fam); !ok {
				return fail("variante per famiglia sconosciuta %q (note: %s)", fam, known)
			}
			if err := checkPhrase(v); err != nil {
				return fail("variante %s: %v", fam, err)
			}
		}
		if p.Negative != "" {
			if err := checkPhrase(p.Negative); err != nil {
				return fail("negative: %v", err)
			}
		}
		if p.Requires != nil && p.Requires.MinDuration < 0 {
			return fail("durata_min negativa")
		}
		for _, o := range p.Incompatible {
			if o == p.ID {
				return fail("incompatibile con se stesso")
			}
			if _, ok := l.byID[o]; !ok {
				return fail("incompatibile con un preset inesistente: %s", o)
			}
		}
		for fam, s := range p.Status {
			if _, ok := findFamily(fam); !ok {
				return fail("stato per famiglia sconosciuta %q (note: %s)", fam, known)
			}
			if s.Result != StatusUntested && s.Result != StatusOK && s.Result != StatusPoor {
				return fail("stato %s: esito %q non valido (%s, %s, %s)", fam, s.Result, StatusUntested, StatusOK, StatusPoor)
			}
			if s.Date != "" {
				if _, err := time.Parse("2006-01-02", s.Date); err != nil {
					return fail("stato %s: data %q non valida (AAAA-MM-GG)", fam, s.Date)
				}
			}
		}
		if p.Status == nil {
			p.Status = map[string]Status{}
		}
		for _, f := range families {
			if _, ok := p.Status[f.name]; !ok {
				p.Status[f.name] = Status{Result: StatusUntested}
			}
		}
	}
	return nil
}

// List restituisce i preset: prima quelli incorporati, poi quelli aggiunti dall'utente.
func (l *Library) List() []*Preset {
	out := make([]*Preset, 0, len(l.order))
	for _, id := range l.order {
		out = append(out, l.byID[id])
	}
	return out
}

// Get restituisce il preset con quell'id.
func (l *Library) Get(id string) (*Preset, bool) {
	p, ok := l.byID[id]
	return p, ok
}

// Protections restituisce i nomi delle clausole di tutela, in ordine alfabetico.
func (l *Library) Protections() []string {
	out := make([]string, 0, len(l.protect))
	for k := range l.protect {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Inputs descrive gli ingressi della generazione, per controllare i requisiti dei preset.
type Inputs struct {
	FirstFrame bool
	LastFrame  bool
	Duration   int // secondi; 0 = default del modello (durata minima non controllata)
}

// Request è una richiesta di espansione.
type Request struct {
	Prompt   string   // prompt dell'utente
	Negative string   // negative dell'utente
	Presets  []string // id dei preset: al massimo uno di camera e uno di effetto
	Model    string   // modello di destinazione ("" = frase generica, senza avvisi)
	Protect  string   // clausola di tutela ("" = nessuna; "prodotto", "illustrazione")
	Inputs   *Inputs  // nil = requisiti non controllati
	Strict   bool     // variante mancante o requisito non soddisfatto diventano errori
}

// Applied è un preset applicato, con la variante usata.
type Applied struct {
	ID       string    `json:"id"`
	Type     string    `json:"tipo"`
	Variant  string    `json:"variante"` // famiglia o "generica"
	Phrase   string    `json:"frase"`
	Requires *Requires `json:"richiede,omitempty"`
}

// Result è il risultato dell'espansione.
type Result struct {
	Prompt   string    // prompt finale
	Negative string    // negative finale (quello dell'utente più quelli dei preset dove il modello lo ha)
	Family   string    // famiglia del modello ("" se non riconosciuta)
	Presets  []Applied // in ordine: camera, effetto
	Warnings []string
}

// Expand compone il prompt finale: prompt dell'utente + frase del preset di camera +
// frase del preset di effetto + clausola di tutela. Senza preset né tutela prompt
// e negative restano identici. Gli errori sono tutti d'uso (codice 2 nel CLI).
func (l *Library) Expand(r Request) (Result, error) {
	res := Result{Family: Family(r.Model), Prompt: r.Prompt, Negative: r.Negative}
	if len(r.Presets) > 2 {
		return Result{}, fmt.Errorf("troppi preset (%d): al massimo uno di camera e uno di effetto", len(r.Presets))
	}
	var picked []*Preset
	for _, id := range r.Presets {
		id = strings.TrimSpace(id)
		p, ok := l.byID[id]
		if !ok {
			return Result{}, fmt.Errorf("preset sconosciuto: %q (elenco con giv presets)", id)
		}
		if len(picked) == 1 && picked[0].ID == id {
			return Result{}, fmt.Errorf("preset ripetuto: %s", id)
		}
		picked = append(picked, p)
	}
	var camera, effect *Preset
	if len(picked) == 2 {
		a, b := picked[0], picked[1]
		if slices.Contains(a.Incompatible, b.ID) || slices.Contains(b.Incompatible, a.ID) {
			return Result{}, fmt.Errorf("preset incompatibili: %s e %s", a.ID, b.ID)
		}
		if a.Type == b.Type {
			what := "camera"
			if a.Type == TypeEffect {
				what = "effetto"
			}
			return Result{}, fmt.Errorf("due preset di %s (%s, %s): al massimo uno di camera e uno di effetto", what, a.ID, b.ID)
		}
	}
	for _, p := range picked {
		if p.Type == TypeCamera {
			camera = p
		} else {
			effect = p
		}
	}
	clause := ""
	if r.Protect != "" {
		c, ok := l.protect[r.Protect]
		if !ok {
			return Result{}, fmt.Errorf("tutela sconosciuta: %q (valide: %s)", r.Protect, strings.Join(l.Protections(), ", "))
		}
		clause = c
	}
	if camera == nil && effect == nil && clause == "" {
		return res, nil
	}

	fam, hasFam := findFamily(res.Family)
	prompt := strings.TrimSpace(r.Prompt)
	var negatives []string
	for _, p := range []*Preset{camera, effect} {
		if p == nil {
			continue
		}
		phrase, variant := p.Phrase, Generic
		if v, ok := p.Variants[res.Family]; ok && hasFam {
			phrase, variant = v, res.Family
		} else if strings.TrimSpace(r.Model) != "" {
			msg := fmt.Sprintf("preset %s: nessuna variante per %s (modello %s)", p.ID, res.Family, r.Model)
			if !hasFam {
				msg = fmt.Sprintf("preset %s: famiglia del modello %s non riconosciuta (note: %s)",
					p.ID, r.Model, strings.Join(Families(), ", "))
			}
			if r.Strict {
				return Result{}, errors.New(msg + ": --strict non ammette la frase generica")
			}
			res.Warnings = append(res.Warnings, msg+": uso la frase generica")
		}
		if msgs := unmet(p, r.Inputs); len(msgs) > 0 {
			if r.Strict {
				return Result{}, errors.New(msgs[0])
			}
			res.Warnings = append(res.Warnings, msgs...)
		}
		prompt = appendSentence(prompt, phrase)
		if p.Negative != "" {
			negatives = append(negatives, strings.TrimSpace(p.Negative))
		}
		res.Presets = append(res.Presets, Applied{ID: p.ID, Type: p.Type, Variant: variant, Phrase: phrase, Requires: p.Requires})
	}
	if clause != "" {
		prompt = appendSentence(prompt, clause)
	}
	res.Prompt = prompt
	if hasFam && fam.negative && len(negatives) > 0 {
		parts := negatives
		if n := strings.TrimSpace(r.Negative); n != "" {
			parts = append([]string{n}, negatives...)
		}
		res.Negative = strings.Join(parts, ", ")
	}
	return res, nil
}

// unmet restituisce gli avvisi per i requisiti del preset non soddisfatti dagli ingressi.
func unmet(p *Preset, in *Inputs) []string {
	if p.Requires == nil || in == nil {
		return nil
	}
	var out []string
	if p.Requires.FirstFrame && !in.FirstFrame {
		out = append(out, fmt.Sprintf("preset %s: rende meglio con un primo fotogramma (--image)", p.ID))
	}
	if p.Requires.LastFrame && !in.LastFrame {
		out = append(out, fmt.Sprintf("preset %s: rende meglio con un ultimo fotogramma (--last-frame)", p.ID))
	}
	if p.Requires.MinDuration > 0 && in.Duration > 0 && in.Duration < p.Requires.MinDuration {
		out = append(out, fmt.Sprintf("preset %s: rende meglio con una durata di almeno %d s (chiesti %d s)",
			p.ID, p.Requires.MinDuration, in.Duration))
	}
	return out
}

// appendSentence aggiunge una frase al prompt: dopo la fine di una frase la
// nuova comincia con la maiuscola, altrimenti si separa con un punto.
func appendSentence(prompt, s string) string {
	s = strings.TrimSpace(s)
	if prompt == "" || s == "" {
		return prompt + s
	}
	last := prompt[len(prompt)-1:]
	if strings.ContainsAny(last, ";:,") {
		return prompt + " " + s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		s = string(s[0]-'a'+'A') + s[1:]
	}
	if strings.ContainsAny(last, ".!?") {
		return prompt + " " + s
	}
	return prompt + ". " + s
}
