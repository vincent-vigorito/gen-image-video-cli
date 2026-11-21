// Package story legge e controlla lo storyboard di giv story: un file JSON con
// le scene di un video da montare. Qui solo forma, riferimenti fra scene e
// file locali; capacità, preset e stime per scena le controlla il CLI.
package story

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Tipi di scena.
const (
	Video    = "video"    // generata da un modello video
	Clip     = "clip"     // file video già pronto (ripresa vera, screencast)
	Immagine = "immagine" // fermo immagine con durata (cartelli, chiusure)
	Avatar   = "avatar"   // fuori da giv 0.9.0: rifiutato
)

// maxSize è il tetto del file dello storyboard.
const maxSize = 4 << 20

// Storyboard è il file di giv story.
type Storyboard struct {
	Titolo        string              `json:"titolo,omitempty"`
	Formato       *Formato            `json:"formato,omitempty"`
	Default       Generazione         `json:"default"`
	TettoCostoUSD *float64            `json:"tetto_costo_usd,omitempty"` // nil = nessun tetto
	Strict        bool                `json:"strict,omitempty"`          // controlli di rifinitura come errori
	Riferimenti   map[string][]string `json:"riferimenti,omitempty"`     // insiemi di immagini per nome
	Scene         []Scene             `json:"scene"`
	Montaggio     *Montaggio          `json:"montaggio,omitempty"`

	// Dir è la cartella dello storyboard: i percorsi sono relativi a questa.
	Dir string `json:"-"`
}

// Formato è il formato comune del video: aspect e risoluzione valgono anche per
// le scene generate che non li indicano.
type Formato struct {
	Aspect      string `json:"aspect,omitempty"`
	Risoluzione string `json:"risoluzione,omitempty"`
	FPS         int    `json:"fps,omitempty"`
}

// Generazione sono i parametri di generazione del blocco default, che ogni
// scena video può sovrascrivere. Vuoto = come giv video.
type Generazione struct {
	Provider    string                     `json:"provider,omitempty"`
	Modello     string                     `json:"modello,omitempty"`
	Risoluzione string                     `json:"risoluzione,omitempty"`
	Aspect      string                     `json:"aspect,omitempty"`
	Audio       string                     `json:"audio,omitempty"` // on, off
	Seed        int                        `json:"seed,omitempty"`
	Tutela      string                     `json:"tutela,omitempty"`
	Param       map[string]json.RawMessage `json:"param,omitempty"` // parametri passanti del modello
}

// Scene è una scena dello storyboard.
type Scene struct {
	ID     string `json:"id"`
	Tipo   string `json:"tipo"`
	Durata int    `json:"durata,omitempty"` // secondi; video: 0 = default del modello

	// scena video
	Prompt             string            `json:"prompt,omitempty"`
	Negative           string            `json:"negative,omitempty"`
	Preset             []string          `json:"preset,omitempty"`
	Immagine           string            `json:"immagine,omitempty"`          // primo fotogramma, file o URL
	UltimoFotogramma   string            `json:"ultimo_fotogramma,omitempty"` // file o URL
	Usa                []string          `json:"usa,omitempty"`               // insiemi di riferimenti
	FotogrammaChiave   *FotogrammaChiave `json:"fotogramma_chiave,omitempty"`
	ContinuaDa         string            `json:"continua_da,omitempty"` // id di una scena precedente
	CostoDichiaratoUSD *float64          `json:"costo_dichiarato_usd,omitempty"`
	Generazione

	// scene clip e immagine
	File string `json:"file,omitempty"`

	// tutte: voce fuori campo, messa al montaggio
	Voce string `json:"voce,omitempty"`
}

// FotogrammaChiave è l'immagine generata prima della scena e usata come primo
// fotogramma; gli insiemi di usa ne sono le immagini di ingresso.
type FotogrammaChiave struct {
	Prompt             string   `json:"prompt"`
	Provider           string   `json:"provider,omitempty"` // vuoto = quello della scena
	Modello            string   `json:"modello,omitempty"`  // vuoto = default di giv image
	CostoDichiaratoUSD *float64 `json:"costo_dichiarato_usd,omitempty"`
}

// Montaggio sono le scelte di giv story cut.
type Montaggio struct {
	Transizione string  `json:"transizione,omitempty"` // taglio (default) o dissolvenza
	Musica      *Musica `json:"musica,omitempty"`
	Sottotitoli string  `json:"sottotitoli,omitempty"` // file
	Uscita      string  `json:"uscita,omitempty"`      // file finale
}

// Musica è la traccia sotto tutto il video.
type Musica struct {
	File   string   `json:"file"`
	Volume *float64 `json:"volume,omitempty"` // 0-1
}

// Load legge e controlla lo storyboard in path: campi sconosciuti, scene,
// riferimenti e file. Tutti i problemi trovati sono nell'errore, uno per riga.
func Load(path string) (*Storyboard, error) {
	data, err := readLimited(path)
	if err != nil {
		return nil, err
	}
	s, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.Dir = filepath.Dir(path)
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("%s: oltre %d MiB, non è uno storyboard", path, maxSize>>20)
	}
	return data, nil
}

// Parse decodifica uno storyboard: un solo oggetto JSON, nessun campo sconosciuto.
func Parse(data []byte) (*Storyboard, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Storyboard
	if err := dec.Decode(&s); err != nil {
		return nil, jsonError(data, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("JSON non valido: altro testo dopo lo storyboard")
	}
	return &s, nil
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

// idRe sono gli id ammessi: diventano nomi di file.
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Validate controlla lo storyboard già letto; Dir deve essere impostata.
func (s *Storyboard) Validate() error {
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	file := func(where, p string, url bool) {
		if err := s.checkPath(p, url); err != nil {
			fail("%s: %v", where, err)
		}
	}

	if s.TettoCostoUSD != nil && !nonNegative(*s.TettoCostoUSD) {
		fail("tetto_costo_usd deve essere un importo finito e non negativo in dollari")
	}
	if s.Formato != nil && s.Formato.FPS < 0 {
		fail("formato.fps deve essere positivo")
	}
	for _, name := range sortedKeys(s.Riferimenti) {
		if len(s.Riferimenti[name]) == 0 {
			fail("riferimenti %s: insieme vuoto", name)
		}
		for i, p := range s.Riferimenti[name] {
			file(fmt.Sprintf("riferimenti %s n. %d", name, i+1), p, true)
		}
	}
	if len(s.Scene) == 0 {
		fail("nessuna scena")
	}
	seen := map[string]int{} // id -> posizione
	for i := range s.Scene {
		sc := &s.Scene[i]
		where := fmt.Sprintf("scena n. %d", i+1)
		switch {
		case sc.ID == "":
			fail("%s: id mancante", where)
		case !idRe.MatchString(sc.ID):
			fail("%s: id %q non valido (lettere, cifre, _ e -, al massimo 64)", where, sc.ID)
		default:
			where = "scena " + sc.ID
			if _, dup := seen[sc.ID]; dup {
				fail("%s: id ripetuto", where)
			} else {
				seen[sc.ID] = i
			}
		}
		if sc.Durata < 0 {
			fail("%s: durata negativa", where)
		}
		if sc.Voce != "" {
			file(where+": voce", sc.Voce, false)
		}
		switch sc.Tipo {
		case Video:
			s.validateVideo(sc, where, seen, fail, file)
		case Clip, Immagine:
			if extra := sc.videoFields(); len(extra) > 0 {
				verb := "non valgono"
				if len(extra) == 1 {
					verb = "non vale"
				}
				fail("%s: %s %s per una scena %s", where, strings.Join(extra, ", "), verb, sc.Tipo)
			}
			if sc.File == "" {
				fail("%s: file mancante (scena %s)", where, sc.Tipo)
			} else {
				file(where+": file", sc.File, false)
			}
			if sc.Tipo == Immagine && sc.Durata == 0 {
				fail("%s: una scena immagine vuole la durata in secondi", where)
			}
		case Avatar:
			fail("%s: il tipo avatar non c'è in giv 0.9.0 (arriva con la fase 3, giv avatar); la voce fuori campo va nel campo voce di una scena", where)
		case "":
			fail("%s: tipo mancante (video, clip o immagine)", where)
		default:
			fail("%s: tipo %q sconosciuto (video, clip o immagine)", where, sc.Tipo)
		}
	}
	if m := s.Montaggio; m != nil {
		switch m.Transizione {
		case "", "taglio", "dissolvenza":
		default:
			fail("montaggio.transizione %q sconosciuta (taglio o dissolvenza)", m.Transizione)
		}
		if m.Musica != nil {
			if m.Musica.File == "" {
				fail("montaggio.musica: file mancante")
			} else {
				file("montaggio.musica", m.Musica.File, false)
			}
			if v := m.Musica.Volume; v != nil && (!nonNegative(*v) || *v > 1) {
				fail("montaggio.musica.volume deve essere fra 0 e 1")
			}
		}
		if m.Sottotitoli != "" {
			file("montaggio.sottotitoli", m.Sottotitoli, false)
		}
		if m.Uscita != "" {
			if err := localPath(m.Uscita); err != nil {
				fail("montaggio.uscita: %v", err)
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Storyboard) validateVideo(sc *Scene, where string, seen map[string]int,
	fail func(string, ...any), file func(string, string, bool)) {
	if strings.TrimSpace(sc.Prompt) == "" {
		fail("%s: prompt mancante", where)
	}
	if sc.File != "" {
		fail("%s: file non vale per una scena video (il primo fotogramma è immagine)", where)
	}
	if sc.CostoDichiaratoUSD != nil && !nonNegative(*sc.CostoDichiaratoUSD) {
		fail("%s: costo_dichiarato_usd deve essere un importo finito e non negativo", where)
	}
	first := 0
	for _, set := range []bool{sc.Immagine != "", sc.FotogrammaChiave != nil, sc.ContinuaDa != ""} {
		if set {
			first++
		}
	}
	if first > 1 {
		fail("%s: un solo primo fotogramma fra immagine, fotogramma_chiave e continua_da", where)
	}
	if sc.Immagine != "" {
		file(where+": immagine", sc.Immagine, true)
	}
	if sc.UltimoFotogramma != "" {
		file(where+": ultimo_fotogramma", sc.UltimoFotogramma, true)
	}
	for _, name := range sc.Usa {
		if _, ok := s.Riferimenti[name]; !ok {
			fail("%s: usa %q non è fra i riferimenti", where, name)
		}
	}
	if fk := sc.FotogrammaChiave; fk != nil {
		if strings.TrimSpace(fk.Prompt) == "" {
			fail("%s: fotogramma_chiave senza prompt", where)
		}
		if fk.CostoDichiaratoUSD != nil && !nonNegative(*fk.CostoDichiaratoUSD) {
			fail("%s: fotogramma_chiave.costo_dichiarato_usd deve essere un importo finito e non negativo", where)
		}
	}
	if sc.ContinuaDa != "" {
		j, ok := seen[sc.ContinuaDa]
		switch {
		case sc.ContinuaDa == sc.ID:
			fail("%s: continua_da verso se stessa", where)
		case !ok:
			// le scene successive non sono ancora in seen
			if s.index(sc.ContinuaDa) >= 0 {
				fail("%s: continua_da %q è una scena successiva: serve una scena precedente", where, sc.ContinuaDa)
			} else {
				fail("%s: continua_da %q: scena inesistente", where, sc.ContinuaDa)
			}
		case s.Scene[j].Tipo != Video && s.Scene[j].Tipo != Clip:
			fail("%s: continua_da %q: serve una scena video o clip", where, sc.ContinuaDa)
		}
	}
}

// videoFields sono i campi di una scena video impostati su una scena di altro tipo.
func (sc *Scene) videoFields() []string {
	var out []string
	add := func(set bool, name string) {
		if set {
			out = append(out, name)
		}
	}
	add(sc.Prompt != "", "prompt")
	add(sc.Negative != "", "negative")
	add(len(sc.Preset) > 0, "preset")
	add(sc.Immagine != "", "immagine")
	add(sc.UltimoFotogramma != "", "ultimo_fotogramma")
	add(len(sc.Usa) > 0, "usa")
	add(sc.FotogrammaChiave != nil, "fotogramma_chiave")
	add(sc.ContinuaDa != "", "continua_da")
	add(sc.CostoDichiaratoUSD != nil, "costo_dichiarato_usd")
	g := sc.Generazione
	add(g.Provider != "", "provider")
	add(g.Modello != "", "modello")
	add(g.Risoluzione != "", "risoluzione")
	add(g.Aspect != "", "aspect")
	add(g.Audio != "", "audio")
	add(g.Seed != 0, "seed")
	add(g.Tutela != "", "tutela")
	add(len(g.Param) > 0, "param")
	return out
}

func (s *Storyboard) index(id string) int {
	for i := range s.Scene {
		if s.Scene[i].ID == id {
			return i
		}
	}
	return -1
}

// Scena restituisce la scena con l'id indicato; nil se non c'è.
func (s *Storyboard) Scena(id string) *Scene {
	if i := s.index(id); i >= 0 {
		return &s.Scene[i]
	}
	return nil
}

// IsURL dice se p è un indirizzo http(s) invece di un file.
func IsURL(p string) bool {
	return strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://")
}

// Path è il percorso di p rispetto alla cartella corrente: i file sono relativi
// alla cartella dello storyboard, gli URL restano come sono.
func (s *Storyboard) Path(p string) string {
	if p == "" || IsURL(p) {
		return p
	}
	return filepath.Join(s.Dir, filepath.FromSlash(p))
}

// localPath controlla che p sia relativo e resti nella cartella dello storyboard.
func localPath(p string) error {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return fmt.Errorf("%s: percorso assoluto, serve un percorso relativo alla cartella dello storyboard", p)
	}
	if !filepath.IsLocal(filepath.FromSlash(p)) {
		return fmt.Errorf("%s: il percorso deve restare nella cartella dello storyboard", p)
	}
	return nil
}

// checkPath controlla un file in ingresso (o un URL dove ammesso): relativo,
// dentro la cartella dello storyboard anche seguendo i link, esistente e regolare.
func (s *Storyboard) checkPath(p string, url bool) error {
	if IsURL(p) {
		if url {
			return nil
		}
		return fmt.Errorf("%s: qui serve un file, non un URL", redact(p))
	}
	if p == "" {
		return errors.New("percorso vuoto")
	}
	if err := localPath(p); err != nil {
		return err
	}
	full := s.Path(p)
	st, err := os.Stat(full)
	if err != nil {
		return fmt.Errorf("%s: file mancante", p)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s: non è un file", p)
	}
	root, err1 := filepath.EvalSymlinks(s.Dir)
	real, err2 := filepath.EvalSymlinks(full)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("%s: percorso non leggibile", p)
	}
	if rel, err := filepath.Rel(root, real); err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("%s: il link porta fuori dalla cartella dello storyboard", p)
	}
	return nil
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

// nonNegative: importo finito e non negativo.
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
