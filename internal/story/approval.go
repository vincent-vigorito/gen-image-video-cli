package story

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"
)

// Approval è l'approvazione di una scena (approvazione nello storyboard, giv
// story approve): la ripresa vista (bozza o fotogramma chiave, col contenuto
// del file) e la ricetta del definitivo approvata (RecipeKey). Vale finché
// CheckApproval non dice il contrario: una nuova ripresa, un altro fotogramma
// chiave o un cambio della ricetta la rendono vecchia, un cambio in un'altra
// scena indipendente no.
type Approval struct {
	Take     string    `json:"take"`               // ripresa vista: bozza o fotogramma
	File     string    `json:"file"`               // suo file, relativo alla cartella dello storyboard
	SHA256   string    `json:"sha256"`             // contenuto del file visto
	Keyframe string    `json:"keyframe,omitempty"` // fotogramma chiave in uso quando si è approvato
	Recipe   string    `json:"recipe"`             // RecipeKey del definitivo approvato
	At       time.Time `json:"at"`
}

// Approvazioni.
var (
	ErrNotApproved      = errors.New("scena non approvata")
	ErrStaleApproval    = errors.New("approvazione non più valida")
	ErrNothingToApprove = errors.New("niente da approvare")
)

// sha256Re è un'impronta sha256 esadecimale.
var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// check controlla un'approvazione letta dallo stato.
func (a *Approval) check() error {
	var errs []error
	if a.Take != TakeDraft && a.Take != TakeKeyframe {
		errs = append(errs, fmt.Errorf("take %q non valido (bozza, fotogramma)", a.Take))
	}
	if err := localPath(a.File); a.File == "" || err != nil {
		errs = append(errs, fmt.Errorf("file %q non valido", a.File))
	}
	if a.Keyframe != "" {
		if err := localPath(a.Keyframe); err != nil {
			errs = append(errs, fmt.Errorf("keyframe %q non valido", a.Keyframe))
		}
	}
	if !sha256Re.MatchString(a.SHA256) {
		errs = append(errs, errors.New("sha256 non valido"))
	}
	if !sha256Re.MatchString(a.Recipe) {
		errs = append(errs, errors.New("recipe non valida"))
	}
	return errors.Join(errs...)
}

// Approve approva la ripresa in uso di tipo take (TakeDraft o TakeKeyframe)
// della scena id con la ricetta del definitivo recipe (RecipeKey adesso): ne
// legge il file per l'impronta e la registra nello stato, al posto di
// un'approvazione precedente. Va salvata (Save), col blocco dello stato
// (OpenLockedState). ErrNothingToApprove se la scena non ha una ripresa di
// quel tipo.
func (st *State) Approve(s *Storyboard, id, take, recipe string) (*Approval, error) {
	if take != TakeDraft && take != TakeKeyframe {
		return nil, fmt.Errorf("scena %s: si approva una bozza o un fotogramma, non %q", id, take)
	}
	if !sha256Re.MatchString(recipe) {
		return nil, fmt.Errorf("scena %s: ricetta non valida", id)
	}
	sc := st.Scenes[id]
	var t *Take
	if sc != nil {
		t = sc.Current(take)
	}
	if t == nil {
		return nil, fmt.Errorf("scena %s: %w: nessuna ripresa (%s) da vedere", id, ErrNothingToApprove, take)
	}
	sum, err := s.FileSHA256(t.File)
	if err != nil {
		return nil, fmt.Errorf("scena %s: %s: %w", id, take, err)
	}
	a := &Approval{Take: take, File: t.File, SHA256: sum, Recipe: recipe, At: time.Now()}
	if sc.Keyframe != nil {
		a.Keyframe = sc.Keyframe.File
	}
	sc.Approval = a
	return a, nil
}

// CheckApproval dice se l'approvazione della scena id vale ancora per la
// ricetta del definitivo recipe (RecipeKey adesso): nil se sì; altrimenti un
// errore con ErrNotApproved (nessuna approvazione) o ErrStaleApproval (ricetta
// cambiata, nuova ripresa o altro fotogramma chiave dopo l'approvazione, file
// visto cambiato o non più leggibile), col motivo.
func (st *State) CheckApproval(s *Storyboard, id, recipe string) error {
	sc := st.Scenes[id]
	if sc == nil || sc.Approval == nil {
		return fmt.Errorf("scena %s: %w", id, ErrNotApproved)
	}
	a := sc.Approval
	stale := func(why string) error { return fmt.Errorf("scena %s: %w: %s", id, ErrStaleApproval, why) }
	if a.Recipe != recipe {
		return stale("la ricetta del definitivo è cambiata (richiesta, modelli, durata, riferimenti o kit)")
	}
	if t := sc.Current(a.Take); t == nil || t.File != a.File {
		return stale("c'è una nuova ripresa (" + a.Take + ") da vedere")
	}
	kf := ""
	if sc.Keyframe != nil {
		kf = sc.Keyframe.File
	}
	if kf != a.Keyframe {
		return stale("il fotogramma chiave è cambiato")
	}
	sum, err := s.FileSHA256(a.File)
	if err != nil {
		return stale("il file approvato non si legge: " + err.Error())
	}
	if sum != a.SHA256 {
		return stale("il file approvato è cambiato")
	}
	return nil
}

// FileSHA256 è l'impronta sha256 (esadecimale) del contenuto del file rel,
// relativo alla cartella dello storyboard e controllato come i suoi file
// (dentro anche seguendo i link, file regolare).
func (s *Storyboard) FileSHA256(rel string) (string, error) {
	p, err := s.CheckFile(rel)
	if err != nil {
		return "", err
	}
	return fileSHA256(p)
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return "", err
	} else if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: non è un file", p)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// recipe è la ricetta del definitivo di una scena generata, nella forma da
// cui si calcola RecipeKey: i percorsi sono quelli dello storyboard (relativi)
// o "kit:" più quello relativo al kit, così spostare la cartella del progetto
// non cambia la ricetta.
type recipe struct {
	Tipo             string                     `json:"tipo"`
	Provider         string                     `json:"provider,omitempty"`
	Modello          string                     `json:"modello,omitempty"`
	Risoluzione      string                     `json:"risoluzione,omitempty"`
	Aspect           string                     `json:"aspect,omitempty"`
	Audio            string                     `json:"audio,omitempty"`
	Tutela           string                     `json:"tutela,omitempty"`
	Seed             int                        `json:"seed,omitempty"`
	Param            map[string]json.RawMessage `json:"param,omitempty"`
	Durata           int                        `json:"durata,omitempty"`
	Descrizione      string                     `json:"descrizione,omitempty"`
	Prompt           string                     `json:"prompt,omitempty"`
	Negative         string                     `json:"negative,omitempty"`
	Preset           []string                   `json:"preset,omitempty"`
	Immagine         string                     `json:"immagine,omitempty"`
	UltimoFotogramma string                     `json:"ultimo_fotogramma,omitempty"`
	Riferimenti      []string                   `json:"riferimenti,omitempty"`
	Intermedi        []string                   `json:"intermedi,omitempty"`
	Inquadrature     []Inquadratura             `json:"inquadrature,omitempty"`
	Fotogramma       *FotogrammaChiave          `json:"fotogramma,omitempty"`
	ContinuaDa       string                     `json:"continua_da,omitempty"` // RecipeKey della scena, o il suo file
	Voce             string                     `json:"voce,omitempty"`        // avatar: la voce che guida il labiale
	AvatarModello    string                     `json:"avatar_modello,omitempty"`
	File             map[string]string          `json:"file,omitempty"` // percorso -> sha256 del contenuto
}

// recipeVersion cambia se cambia la forma della ricetta: le approvazioni di
// prima diventano vecchie.
const recipeVersion = "giv-ricetta-1\n"

// RecipeKey è l'impronta (sha256 esadecimale) della ricetta del definitivo
// della scena generata id (video o avatar): richiesta (prompt, negativo,
// preset, inquadrature, descrizione e tutela dei personaggi del kit), provider
// e modelli (anche del fotogramma chiave), durata, formato, audio, seed e
// parametri, immagini e riferimenti col contenuto dei file (anche quelli del
// kit), e la ricetta della scena di continua_da (o il contenuto della sua
// clip). Montaggio, voce fuori campo e costi dichiarati non ne fanno parte:
// cambiarli non rende vecchia un'approvazione. provider è il provider da
// usare quando né la scena né il blocco default lo indicano (GIV_PROVIDER);
// vuoto se nessuno.
func (s *Storyboard) RecipeKey(id, provider string) (string, error) {
	return s.recipeKey(id, provider, 0)
}

func (s *Storyboard) recipeKey(id, provider string, depth int) (string, error) {
	sc := s.Scena(id)
	switch {
	case sc == nil:
		return "", fmt.Errorf("scena %s: inesistente", id)
	case !sc.Generata():
		return "", fmt.Errorf("scena %s: non è una scena generata (video o avatar)", id)
	case depth > len(s.Scene):
		return "", fmt.Errorf("scena %s: continua_da in cerchio", id)
	}
	d, g := s.Default.Generazione, sc.Generazione
	var f Formato
	if s.Formato != nil {
		f = *s.Formato
	}
	r := recipe{
		Tipo:        sc.Tipo,
		Provider:    pick(g.Provider, d.Provider, provider),
		Modello:     pick(g.Modello, d.Modello),
		Risoluzione: pick(g.Risoluzione, d.Risoluzione, f.Risoluzione),
		Aspect:      pick(g.Aspect, d.Aspect, f.Aspect),
		Audio:       pick(g.Audio, d.Audio),
		Tutela:      pick(g.Tutela, s.TutelaPersonaggi(sc), d.Tutela),
		Seed:        g.Seed,
		Durata:      sc.Durata,
		Descrizione: s.Descrizione(sc),
		Prompt:      sc.Prompt, Negative: sc.Negative, Preset: sc.Preset,
		Inquadrature: sc.Inquadrature,
		File:         map[string]string{},
	}
	if r.Seed == 0 {
		r.Seed = d.Seed
	}
	for _, m := range []map[string]json.RawMessage{d.Param, g.Param} {
		for k, v := range m {
			var b bytes.Buffer
			if err := json.Compact(&b, v); err != nil {
				return "", fmt.Errorf("scena %s: param %s: %v", id, k, err)
			}
			if r.Param == nil {
				r.Param = map[string]json.RawMessage{}
			}
			r.Param[k] = b.Bytes()
		}
	}
	var ferr error
	file := func(p string) string {
		if p == "" || ferr != nil {
			return p
		}
		if IsURL(p) {
			return p // si confronta l'indirizzo: il contenuto non si scarica
		}
		key, full := p, s.Path(p)
		if rel, ok := s.KitRel(p); ok {
			key = "kit:" + rel
			if err := s.KitData.CheckPath(rel); err != nil {
				ferr = fmt.Errorf("scena %s: kit: %w", id, err)
				return key
			}
		} else if err := s.checkPath(p, false); err != nil {
			ferr = fmt.Errorf("scena %s: %w", id, err)
			return key
		}
		sum, err := fileSHA256(full)
		if err != nil {
			ferr = fmt.Errorf("scena %s: %s: %w", id, key, err)
			return key
		}
		r.File[key] = sum
		return key
	}
	r.Immagine = file(sc.Immagine)
	r.UltimoFotogramma = file(sc.UltimoFotogramma)
	for _, p := range sc.FotogrammiIntermedi {
		r.Intermedi = append(r.Intermedi, file(p))
	}
	for _, name := range sc.Usa {
		for _, p := range s.Riferimenti[name] {
			r.Riferimenti = append(r.Riferimenti, file(p))
		}
	}
	if fk := sc.FotogrammaChiave; fk != nil {
		r.Fotogramma = &FotogrammaChiave{Prompt: fk.Prompt, Provider: fk.Provider, Modello: fk.Modello}
	}
	if sc.Tipo == Avatar {
		r.Voce = file(sc.Voce)
		r.AvatarModello = s.AvatarModello(sc)
	}
	if ferr != nil {
		return "", ferr
	}
	if c := sc.ContinuaDa; c != "" {
		prev := s.Scena(c)
		switch {
		case prev == nil:
			return "", fmt.Errorf("scena %s: continua_da %q: scena inesistente", id, c)
		case prev.Generata():
			k, err := s.recipeKey(c, provider, depth+1)
			if err != nil {
				return "", err
			}
			r.ContinuaDa = k
		default:
			r.ContinuaDa = "file:" + file(prev.File)
			if ferr != nil {
				return "", ferr
			}
		}
	}
	data, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	io.WriteString(h, recipeVersion)
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// pick è il primo valore non vuoto.
func pick(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}
