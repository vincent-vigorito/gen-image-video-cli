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
	"unicode/utf8"

	"gen-image-video-cli/internal/kit"
)

// Tipi di scena.
const (
	Video    = "video"    // generata da un modello video
	Clip     = "clip"     // file video già pronto (ripresa vera, screencast)
	Immagine = "immagine" // fermo immagine con durata (cartelli, chiusure)
	Avatar   = "avatar"   // personaggio che parla (giv avatar su fal): immagine più voce
)

// AvatarModelli sono i modelli di avatar.modello e del campo modello di una
// scena avatar (Kling AI Avatar v2 su fal); il primo è il default.
var AvatarModelli = []string{"standard", "pro"}

// maxSize è il tetto del file dello storyboard.
const maxSize = 4 << 20

// Storyboard è il file di giv story.
type Storyboard struct {
	Titolo        string              `json:"titolo,omitempty"`
	Formato       *Formato            `json:"formato,omitempty"`
	Default       Generazione         `json:"default"`
	Avatar        *AvatarDefault      `json:"avatar,omitempty"`          // scene avatar
	TettoCostoUSD *float64            `json:"tetto_costo_usd,omitempty"` // nil = nessun tetto
	Strict        bool                `json:"strict,omitempty"`          // controlli di rifinitura come errori
	Riferimenti   map[string][]string `json:"riferimenti,omitempty"`     // insiemi di immagini per nome
	Scene         []Scene             `json:"scene"`
	Montaggio     *Montaggio          `json:"montaggio,omitempty"`
	// FoglioContatto false: giv story run e cut non scrivono
	// foglio-contatto.jpg accanto allo storyboard.
	FoglioContatto *bool `json:"foglio_contatto,omitempty"`
	// Kit è il file del kit di marca e personaggi, relativo alla cartella
	// dello storyboard (può stare fuori); si usa solo se indicato, qui o con --kit.
	Kit string `json:"kit,omitempty"`
	// Parlato sono le impostazioni della voce delle scene con testo.
	Parlato *Parlato `json:"parlato,omitempty"`

	// Dir è la cartella dello storyboard: i percorsi sono relativi a questa.
	Dir string `json:"-"`

	// dal kit (LoadWithKit): il kit letto, i valori ereditati, e i file del
	// kit messi nello storyboard (percorso assoluto -> relativo al kit)
	KitFile     string   `json:"-"` // kit usato, come indicato
	KitData     *kit.Kit `json:"-"`
	Ereditati   []string `json:"-"` // campi che vengono dal kit
	kitPaths    map[string]string
	descrizioni map[string]string // personaggi del kit: descrizione
	tutele      map[string]string // personaggi del kit: tutela
}

// Parlato è il blocco della voce fuori campo generata dai testi delle scene
// (giv story run): i predefiniti vengono dal kit, lo storyboard li sostituisce
// campo per campo; le pronunce dello storyboard vincono su quelle del kit.
type Parlato struct {
	Provider  string            `json:"provider,omitempty"` // xai (default) o gemini
	Modello   string            `json:"modello,omitempty"`
	Voce      string            `json:"voce,omitempty"`
	Lingua    string            `json:"lingua,omitempty"`
	Velocita  *float64          `json:"velocita,omitempty"`
	Stile     string            `json:"stile,omitempty"`
	Pronuncia map[string]string `json:"pronuncia,omitempty"` // parola scritta -> come dirla
}

// AvatarDefault è il blocco avatar: il modello delle scene avatar che non lo indicano.
type AvatarDefault struct {
	Modello string `json:"modello,omitempty"` // standard (default) o pro
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
	Prompt           string            `json:"prompt,omitempty"`
	Negative         string            `json:"negative,omitempty"`
	Preset           []string          `json:"preset,omitempty"`
	Immagine         string            `json:"immagine,omitempty"`          // primo fotogramma, file o URL
	UltimoFotogramma string            `json:"ultimo_fotogramma,omitempty"` // file o URL
	Usa              []string          `json:"usa,omitempty"`               // insiemi di riferimenti
	FotogrammaChiave *FotogrammaChiave `json:"fotogramma_chiave,omitempty"`
	ContinuaDa       string            `json:"continua_da,omitempty"` // id di una scena precedente
	// fotogrammi intermedi in ordine (file o URL) e inquadrature: valgono
	// solo coi modelli che li prevedono (story check)
	FotogrammiIntermedi []string       `json:"fotogrammi_intermedi,omitempty"`
	Inquadrature        []Inquadratura `json:"inquadrature,omitempty"`
	CostoDichiaratoUSD  *float64       `json:"costo_dichiarato_usd,omitempty"`
	Generazione

	// scene clip e immagine
	File string `json:"file,omitempty"`

	// tutte: voce fuori campo, messa al montaggio: un file (voce) o un testo
	// (testo) che giv story run fa dire (voce/<id>-v<N>.wav); con tutti e due
	// vale il file e il testo resta per sottotitoli e controllo
	Voce        string   `json:"voce,omitempty"`
	Testo       string   `json:"testo,omitempty"`
	RitardoVoce *float64 `json:"ritardo_voce,omitempty"` // secondi dall'inizio della scena; nil = 0

	// montaggio della scena (giv story cut), facoltativi
	Fuoco    *Fuoco   `json:"fuoco,omitempty"`    // punto da tenere col ritaglio
	Taglio   *Taglio  `json:"taglio,omitempty"`   // pezzo della clip da usare (video, clip, avatar)
	Velocita *float64 `json:"velocita,omitempty"` // fattore di velocità della clip, da 0.25 a 4
}

// Fuoco è il punto dell'inquadratura da tenere quando il montaggio ritaglia
// la clip per un formato diverso: x da 0 (sinistra) a 1 (destra), y da 0
// (alto) a 1 (basso); assente vale 0.5. Nello storyboard un numero (solo x) o
// {"x": …, "y": …}.
type Fuoco struct {
	X *float64 `json:"x,omitempty"`
	Y *float64 `json:"y,omitempty"`
}

// UnmarshalJSON accetta un numero (x) o un oggetto {x, y} senza altri campi.
func (f *Fuoco) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] != '{' {
		var x float64
		if err := json.Unmarshal(b, &x); err != nil {
			return errors.New("fuoco: serve un numero da 0 a 1 o un oggetto {\"x\": …, \"y\": …}")
		}
		*f = Fuoco{X: &x}
		return nil
	}
	type plain Fuoco
	var p plain
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("fuoco: %s (campi ammessi x e y, numeri da 0 a 1)", strings.TrimPrefix(err.Error(), "json: "))
	}
	*f = Fuoco(p)
	return nil
}

// XY sono x e y del fuoco, 0.5 dove mancano.
func (f *Fuoco) XY() (x, y float64) {
	x, y = 0.5, 0.5
	if f.X != nil {
		x = *f.X
	}
	if f.Y != nil {
		y = *f.Y
	}
	return x, y
}

// Taglio è il pezzo della clip usato nel montaggio: da Da ad A secondi
// dall'inizio del file (A assente: fino alla fine).
type Taglio struct {
	Da float64  `json:"da"`
	A  *float64 `json:"a,omitempty"`
}

// Limiti della velocità di una scena (come giv post speed).
const (
	MinVelocita = 0.25
	MaxVelocita = 4
)

// Inquadratura è una delle inquadrature di una scena video: testo e secondi;
// la scena dura la loro somma e non ha un prompt suo.
type Inquadratura struct {
	Prompt string `json:"prompt"`
	Durata int    `json:"durata"`
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

	// Formati sono i formati da montare, un file per formato
	// (<uscita>-9x16.mp4…): "16:9", "9:16", "1:1", "4:5"; vuoto: un solo
	// file nel formato dello storyboard, come prima.
	Formati []string `json:"formati,omitempty"`
	// Adatta è come una clip entra in un formato diverso dal suo: bande
	// (default), ritaglio (col fuoco della scena) o sfocato.
	Adatta         string      `json:"adatta,omitempty"`
	Logo           *Logo       `json:"logo,omitempty"`
	Titoli         []Titolo    `json:"titoli,omitempty"`
	CartelloFinale *Cartello   `json:"cartello_finale,omitempty"`
	Volume         *float64    `json:"volume,omitempty"` // volume finale in LUFS; assente: come prima
	StileTesti     *StileTesti `json:"stile_testi,omitempty"`
}

// Musica è la traccia sotto tutto il video.
type Musica struct {
	File      string   `json:"file"`
	Volume    *float64 `json:"volume,omitempty"`     // 0-1
	SottoVoce bool     `json:"sotto_voce,omitempty"` // si abbassa mentre parla la voce
	SfumaFine *float64 `json:"sfuma_fine,omitempty"` // secondi di dissolvenza in chiusura
}

// Logo è il logo sul video, in ogni formato.
type Logo struct {
	File      string   `json:"file"`                // immagine .png, .jpg o .webp
	Posizione string   `json:"posizione,omitempty"` // default alto-destra
	Larghezza *float64 `json:"larghezza,omitempty"` // frazione della larghezza del quadro, oltre 0 e al più 1
	Opacita   *float64 `json:"opacita,omitempty"`   // oltre 0 e al più 1; default 1
	Da        *float64 `json:"da,omitempty"`        // secondi dall'inizio del montato; default 0
	A         *float64 `json:"a,omitempty"`         // secondi; assente: fino alla fine delle scene
}

// Titolo è un testo sopra il video: tempi relativi alla scena indicata
// (assente: all'inizio del montato).
type Titolo struct {
	Testo     string      `json:"testo"`
	Scena     string      `json:"scena,omitempty"`
	Da        float64     `json:"da,omitempty"`        // secondi
	Durata    *float64    `json:"durata,omitempty"`    // secondi; default 3
	Posizione string      `json:"posizione,omitempty"` // default basso
	Stile     *StileTesti `json:"stile,omitempty"`     // al posto di stile_testi
}

// Cartello è il cartello finale, generato e accodato dopo l'ultima scena.
type Cartello struct {
	Durata     *float64 `json:"durata,omitempty"` // secondi; default 3
	Sfondo     string   `json:"sfondo,omitempty"` // colore #RRGGBB; default #000000
	Logo       string   `json:"logo,omitempty"`   // immagine .png, .jpg o .webp
	Testo      string   `json:"testo,omitempty"`
	Sottotesto string   `json:"sottotesto,omitempty"`
}

// StileTesti è lo stile dei testi (titoli, cartello).
type StileTesti struct {
	Font       string   `json:"font,omitempty"`       // file .ttf, .otf o .ttc nella cartella
	Colore     string   `json:"colore,omitempty"`     // #RRGGBB; default #FFFFFF
	Bordo      string   `json:"bordo,omitempty"`      // colore del contorno #RRGGBB, o "nessuno"; default #000000
	Dimensione *float64 `json:"dimensione,omitempty"` // altezza dei caratteri in frazione dell'altezza del quadro
}

// Valori ammessi e limiti dei campi di montaggio.
var (
	Formati   = []string{"16:9", "9:16", "1:1", "4:5"}
	Adatta    = []string{"bande", "ritaglio", "sfocato"}
	Posizioni = []string{"alto-sinistra", "alto", "alto-destra", "sinistra", "centro", "destra", "basso-sinistra", "basso", "basso-destra"}
)

const (
	maxTitoli        = 50
	maxTestoBytes    = 2000
	maxCartello      = 60 // secondi
	maxSfumaFine     = 60 // secondi
	minLUFS, maxLUFS = -70, -5
	minDimensione    = 0.01
	maxDimensione    = 0.25
)

// Load legge e controlla lo storyboard in path: campi sconosciuti, scene,
// riferimenti e file, col kit del campo kit se c'è. Tutti i problemi trovati
// sono nell'errore, uno per riga.
func Load(path string) (*Storyboard, error) {
	return LoadWithKit(path, "")
}

// LoadWithKit è Load con il kit kitFile (--kit, relativo alla cartella
// corrente) al posto di quello del campo kit; vuoto: quello del campo, se c'è.
// I valori del kit si fondono nello storyboard prima dei controlli (scena,
// poi storyboard, poi kit: applyKit).
func LoadWithKit(path, kitFile string) (*Storyboard, error) {
	data, err := readLimited(path)
	if err != nil {
		return nil, err
	}
	s, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.Dir = filepath.Dir(path)
	if kitFile == "" && s.Kit != "" {
		if kitFile, err = s.kitField(); err != nil {
			return nil, err
		}
	}
	if kitFile != "" {
		if err := s.applyKit(kitFile); err != nil {
			return nil, err
		}
	}
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
	if a := s.Avatar; a != nil && a.Modello != "" && !slices.Contains(AvatarModelli, a.Modello) {
		fail("avatar.modello %q sconosciuto (%s)", a.Modello, strings.Join(AvatarModelli, " o "))
	}
	if s.Formato != nil && s.Formato.FPS < 0 {
		fail("formato.fps deve essere positivo")
	}
	if p := s.Parlato; p != nil {
		// i limiti della voce (provider, voce, lingua, velocità, pronunce) li
		// controlla giv story check sulla richiesta di ogni scena con testo
		if v := p.Velocita; v != nil && (!nonNegative(*v) || *v == 0) {
			fail("parlato.velocita deve essere un numero oltre 0 (da 0.7 a 1.5)")
		}
		text("parlato.stile", p.Stile, false, fail)
		for _, w := range sortedKeys(p.Pronuncia) {
			if strings.TrimSpace(w) == "" || strings.TrimSpace(p.Pronuncia[w]) == "" {
				fail("parlato.pronuncia %q: parola o pronuncia vuota", w)
			}
		}
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
		if r := sc.RitardoVoce; r != nil {
			if !nonNegative(*r) {
				fail("%s: ritardo_voce deve essere un numero di secondi finito e non negativo", where)
			}
			if sc.Voce == "" && sc.Testo == "" {
				fail("%s: ritardo_voce senza voce né testo", where)
			}
		}
		text(where+": testo", sc.Testo, false, fail)
		validateSceneCut(sc, where, fail)
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
			s.validateAvatar(sc, where, fail, file)
		case "":
			fail("%s: tipo mancante (video, clip, immagine o avatar)", where)
		default:
			fail("%s: tipo %q sconosciuto (video, clip, immagine o avatar)", where, sc.Tipo)
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
		s.validateMontaggio(m, seen, fail, file)
	}
	return errors.Join(errs...)
}

// validateSceneCut controlla fuoco, taglio e velocità di una scena.
func validateSceneCut(sc *Scene, where string, fail func(string, ...any)) {
	if f := sc.Fuoco; f != nil {
		if f.X == nil && f.Y == nil {
			fail("%s: fuoco vuoto: serve x (e facoltativo y), da 0 a 1", where)
		}
		for _, v := range []*float64{f.X, f.Y} {
			if v != nil && !unit(*v) {
				fail("%s: fuoco: x e y sono numeri da 0 a 1 (0.5 = centro)", where)
				break
			}
		}
	}
	if sc.Taglio == nil && sc.Velocita == nil {
		return
	}
	if sc.Tipo == Immagine {
		fail("%s: taglio e velocita valgono per le clip, non per una scena immagine", where)
		return
	}
	if t := sc.Taglio; t != nil {
		if !nonNegative(t.Da) {
			fail("%s: taglio.da deve essere un numero di secondi finito e non negativo", where)
		}
		if t.A != nil && (!nonNegative(*t.A) || *t.A <= t.Da) {
			fail("%s: taglio.a deve essere un numero di secondi dopo taglio.da", where)
		}
		if t.A == nil && t.Da == 0 {
			fail("%s: taglio senza effetto: indica da (oltre 0) o a", where)
		}
	}
	if v := sc.Velocita; v != nil && (!nonNegative(*v) || *v < MinVelocita || *v > MaxVelocita) {
		fail("%s: velocita deve essere un numero da %g a %g (2 = due volte più veloce)", where, MinVelocita, float64(MaxVelocita))
	}
}

// validateMontaggio controlla i campi di montaggio della 0.11.0: valori da
// elenco chiuso, numeri negli intervalli, file nella cartella dello storyboard.
func (s *Storyboard) validateMontaggio(m *Montaggio, seen map[string]int, fail func(string, ...any), file func(string, string, bool)) {
	dup := map[string]bool{}
	for _, f := range m.Formati {
		switch {
		case !slices.Contains(Formati, f):
			fail("montaggio.formati: %q sconosciuto (%s)", f, strings.Join(Formati, ", "))
		case dup[f]:
			fail("montaggio.formati: %s ripetuto", f)
		}
		dup[f] = true
	}
	if m.Formati != nil && len(m.Formati) == 0 {
		fail("montaggio.formati vuoto: togli il campo per un solo formato, o indica i formati")
	}
	if m.Adatta != "" && !slices.Contains(Adatta, m.Adatta) {
		fail("montaggio.adatta %q sconosciuto (%s)", m.Adatta, strings.Join(Adatta, ", "))
	}
	if mu := m.Musica; mu != nil {
		if f := mu.SfumaFine; f != nil && (!nonNegative(*f) || *f > maxSfumaFine) {
			fail("montaggio.musica.sfuma_fine deve essere un numero di secondi da 0 a %d", maxSfumaFine)
		}
		if mu.SottoVoce && !s.hasVoice() {
			fail("montaggio.musica.sotto_voce: nessuna scena ha una voce sotto cui abbassare la musica")
		}
	}
	if v := m.Volume; v != nil && (!nonNegative(-*v) || *v < minLUFS || *v > maxLUFS) {
		fail("montaggio.volume deve essere un livello in LUFS da %d a %d (es. -14)", minLUFS, maxLUFS)
	}
	if st := m.StileTesti; st != nil {
		validateStile("montaggio.stile_testi", st, fail, file)
	}
	if l := m.Logo; l != nil {
		where := "montaggio.logo"
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
	if len(m.Titoli) > maxTitoli {
		fail("montaggio.titoli: al più %d titoli", maxTitoli)
	}
	for i := range m.Titoli {
		t := &m.Titoli[i]
		where := fmt.Sprintf("montaggio.titoli n. %d", i+1)
		text(where+".testo", t.Testo, true, fail)
		if t.Scena != "" {
			if _, ok := seen[t.Scena]; !ok {
				fail("%s.scena %q: nessuna scena con questo id", where, t.Scena)
			}
		}
		if !nonNegative(t.Da) {
			fail("%s.da deve essere un numero di secondi finito e non negativo", where)
		}
		if d := t.Durata; d != nil && (!nonNegative(*d) || *d == 0) {
			fail("%s.durata deve essere un numero di secondi oltre 0", where)
		}
		position(where+".posizione", t.Posizione, fail)
		if t.Stile != nil {
			validateStile(where+".stile", t.Stile, fail, file)
		}
	}
	if c := m.CartelloFinale; c != nil {
		where := "montaggio.cartello_finale"
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
}

// hasVoice dice se almeno una scena ha una voce fuori campo.
func (s *Storyboard) hasVoice() bool {
	for i := range s.Scene {
		if (s.Scene[i].Voce != "" || s.Scene[i].Testo != "") && s.Scene[i].Tipo != Avatar {
			return true
		}
	}
	return false
}

// validateStile controlla uno stile dei testi.
func validateStile(where string, st *StileTesti, fail func(string, ...any), file func(string, string, bool)) {
	if st.Font != "" {
		switch strings.ToLower(filepath.Ext(st.Font)) {
		case ".ttf", ".otf", ".ttc":
			file(where+".font", st.Font, false)
		default:
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

// imageFile controlla un'immagine (logo) nella cartella dello storyboard.
func imageFile(where, p string, fail func(string, ...any), file func(string, string, bool)) {
	if p == "" {
		fail("%s: file mancante", where)
		return
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".webp":
		file(where, p, false)
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

// colorRe è un colore #RRGGBB.
var colorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// color controlla un colore (vuoto: quello di default).
func color(where, c string, fail func(string, ...any)) {
	if c != "" && !colorRe.MatchString(c) {
		fail("%s %q: serve un colore #RRGGBB", where, c)
	}
}

// text controlla un testo da mostrare: reso alla lettera, al più
// maxTestoBytes, UTF-8 valido; required: non vuoto.
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

// unit: numero finito da 0 a 1.
func unit(v float64) bool { return nonNegative(v) && v <= 1 }

func (s *Storyboard) validateVideo(sc *Scene, where string, seen map[string]int,
	fail func(string, ...any), file func(string, string, bool)) {
	if len(sc.Inquadrature) > 0 {
		// il testo è nelle inquadrature, la durata è la loro somma
		if sc.Prompt != "" || len(sc.Preset) > 0 {
			fail("%s: con inquadrature il testo va nelle inquadrature: prompt e preset non valgono", where)
		}
		sum := 0
		for i, q := range sc.Inquadrature {
			if strings.TrimSpace(q.Prompt) == "" {
				fail("%s: inquadratura n. %d senza prompt", where, i+1)
			}
			if q.Durata < 1 {
				fail("%s: inquadratura n. %d: durata in secondi interi da 1 in su", where, i+1)
			}
			sum += max(q.Durata, 0)
		}
		if sc.Durata != 0 && sc.Durata != sum {
			fail("%s: durata %d diversa dalla somma delle inquadrature (%d s)", where, sc.Durata, sum)
		}
	} else if strings.TrimSpace(sc.Prompt) == "" {
		fail("%s: prompt mancante", where)
	}
	for i, p := range sc.FotogrammiIntermedi {
		if p == "" {
			fail("%s: fotogrammi_intermedi n. %d vuoto", where, i+1)
		} else {
			file(fmt.Sprintf("%s: fotogrammi_intermedi n. %d", where, i+1), p, true)
		}
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

// avatarFields sono i campi di una scena video che valgono anche per una scena avatar.
var avatarFields = []string{"prompt", "immagine", "costo_dichiarato_usd", "provider", "modello"}

// validateAvatar controlla una scena avatar: immagine e voce sono file della
// cartella dello storyboard; la scena dura quanto la voce, che guida il labiale
// dall'inizio e resta nella clip (al montaggio non si aggiunge una seconda volta).
func (s *Storyboard) validateAvatar(sc *Scene, where string, fail func(string, ...any), file func(string, string, bool)) {
	if sc.Immagine == "" {
		fail("%s: immagine mancante (scena avatar)", where)
	} else {
		file(where+": immagine", sc.Immagine, false)
	}
	if sc.Voce == "" {
		fail("%s: voce mancante (scena avatar: l'audio che guida il labiale)", where)
		if sc.Testo != "" {
			fail("%s: testo senza voce: in una scena avatar la voce è un file (fallo con giv voice), il testo vale solo per sottotitoli e controllo", where)
		}
	}
	if sc.Durata != 0 {
		fail("%s: durata non vale per una scena avatar (dura quanto la voce)", where)
	}
	if sc.RitardoVoce != nil {
		fail("%s: ritardo_voce non vale per una scena avatar (la voce guida il labiale dall'inizio)", where)
	}
	if sc.File != "" {
		fail("%s: file non vale per una scena avatar (l'immagine del personaggio è immagine)", where)
	}
	extra := slices.DeleteFunc(sc.videoFields(), func(f string) bool { return slices.Contains(avatarFields, f) })
	if len(extra) > 0 {
		verb := "non valgono"
		if len(extra) == 1 {
			verb = "non vale"
		}
		fail("%s: %s %s per una scena avatar", where, strings.Join(extra, ", "), verb)
	}
	if p := sc.Provider; p != "" && p != "fal" {
		fail("%s: provider %q: una scena avatar si genera solo con fal", where, p)
	}
	if m := sc.Modello; m != "" && !slices.Contains(AvatarModelli, m) {
		fail("%s: modello %q sconosciuto per una scena avatar (%s)", where, m, strings.Join(AvatarModelli, " o "))
	}
	if sc.CostoDichiaratoUSD != nil && !nonNegative(*sc.CostoDichiaratoUSD) {
		fail("%s: costo_dichiarato_usd deve essere un importo finito e non negativo", where)
	}
}

// AvatarModello è il modello della scena avatar: quello della scena, poi
// avatar.modello, poi il default.
func (s *Storyboard) AvatarModello(sc *Scene) string {
	switch {
	case sc.Modello != "":
		return sc.Modello
	case s.Avatar != nil && s.Avatar.Modello != "":
		return s.Avatar.Modello
	}
	return AvatarModelli[0]
}

// Generata dice se la scena si genera con giv story run (video o avatar) e ha
// quindi uno stato, una clip e un costo.
func (sc *Scene) Generata() bool { return sc.Tipo == Video || sc.Tipo == Avatar }

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
	add(len(sc.FotogrammiIntermedi) > 0, "fotogrammi_intermedi")
	add(len(sc.Inquadrature) > 0, "inquadrature")
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

// Ritardo è l'inizio della voce in secondi dall'inizio della scena (0 se
// ritardo_voce manca).
func (sc *Scene) Ritardo() float64 {
	if sc.RitardoVoce == nil {
		return 0
	}
	return *sc.RitardoVoce
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
	if _, ok := s.kitPaths[p]; ok {
		return p // file del kit, già nella forma assoluta
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
	if rel, ok := s.kitPaths[p]; ok {
		// file ereditato dal kit: chiuso nella cartella del kit (K1)
		if err := s.KitData.CheckPath(rel); err != nil {
			return fmt.Errorf("kit: %w", err)
		}
		return nil
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
