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
// chiave (anche nello stesso file), un cambio della ricetta, del materiale da
// vedere (default.bozza aggiunto o tolto) o della clip di partenza di
// continua_da la rendono vecchia; un cambio in un'altra scena indipendente no.
type Approval struct {
	Take           string          `json:"take"`                      // ripresa vista: bozza o fotogramma
	File           string          `json:"file"`                      // suo file, relativo alla cartella dello storyboard
	SHA256         string          `json:"sha256"`                    // contenuto del file visto
	Keyframe       string          `json:"keyframe,omitempty"`        // fotogramma chiave in uso quando si è approvato
	KeyframeSHA256 string          `json:"keyframe_sha256,omitempty"` // suo contenuto: va al definitivo
	Source         *ApprovalSource `json:"source,omitempty"`          // continua_da da una scena generata
	Recipe         string          `json:"recipe"`                    // RecipeKey del definitivo approvato
	At             time.Time       `json:"at"`
}

// ApprovalSource è la clip di partenza di continua_da (il definitivo della
// scena Scene) quando si è approvato: la clip in uso (File, SHA256), se
// c'era, e quante clip quella scena aveva generato (Takes). Dopo
// l'approvazione un'altra clip in uso, o una seconda clip generata se allora
// non ce n'era, rende vecchia l'approvazione; la prima clip di una scena non
// ancora generata no: è quella della sua ricetta, compresa in RecipeKey.
type ApprovalSource struct {
	Scene  string `json:"scene"`
	File   string `json:"file,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Takes  int    `json:"takes"`
}

// ApprovalTake è il materiale che si approva nello storyboard: la bozza
// (TakeDraft) con default.bozza, altrimenti il fotogramma chiave
// (TakeKeyframe). Un'approvazione di un altro materiale non vale.
func (s *Storyboard) ApprovalTake() string {
	if s.Default.Bozza != nil {
		return TakeDraft
	}
	return TakeKeyframe
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
		if !sha256Re.MatchString(a.KeyframeSHA256) {
			errs = append(errs, errors.New("keyframe_sha256 non valido"))
		}
	} else if a.KeyframeSHA256 != "" {
		errs = append(errs, errors.New("keyframe_sha256 senza keyframe"))
	}
	if src := a.Source; src != nil {
		if !idRe.MatchString(src.Scene) {
			errs = append(errs, fmt.Errorf("source: scene %q non valida", src.Scene))
		}
		if src.File != "" {
			if err := localPath(src.File); err != nil {
				errs = append(errs, fmt.Errorf("source: file %q non valido", src.File))
			}
			if !sha256Re.MatchString(src.SHA256) {
				errs = append(errs, errors.New("source: sha256 non valido"))
			}
		} else if src.SHA256 != "" {
			errs = append(errs, errors.New("source: sha256 senza file"))
		}
		if src.Takes < 0 {
			errs = append(errs, errors.New("source: takes negativo"))
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
// un'approvazione precedente, insieme al contenuto del fotogramma chiave in
// uso e alla clip di partenza di continua_da. Va salvata (Save), col blocco
// dello stato (OpenLockedState). ErrNothingToApprove se la scena non ha una
// ripresa di quel tipo; take deve essere s.ApprovalTake().
func (st *State) Approve(s *Storyboard, id, take, recipe string) (*Approval, error) {
	if take != TakeDraft && take != TakeKeyframe {
		return nil, fmt.Errorf("scena %s: si approva una bozza o un fotogramma, non %q", id, take)
	}
	if want := s.ApprovalTake(); take != want {
		return nil, fmt.Errorf("scena %s: lo storyboard chiede di approvare %s, non %s", id, want, take)
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
		if a.KeyframeSHA256, err = s.FileSHA256(a.Keyframe); err != nil {
			return nil, fmt.Errorf("scena %s: fotogramma chiave: %w", id, err)
		}
	}
	if a.Source, err = st.approvalSource(s, id); err != nil {
		return nil, err
	}
	sc.Approval = a
	return a, nil
}

// approvalSource è la clip di partenza di continua_da della scena id adesso,
// per Approve; nil se la scena non continua da una scena generata (il file
// di una scena montata è già nella ricetta).
func (st *State) approvalSource(s *Storyboard, id string) (*ApprovalSource, error) {
	scene := s.Scena(id)
	if scene == nil || scene.ContinuaDa == "" {
		return nil, nil
	}
	c := scene.ContinuaDa
	if prev := s.Scena(c); prev == nil || !prev.Generata() {
		return nil, nil
	}
	src := &ApprovalSource{Scene: c}
	if ps := st.Scenes[c]; ps != nil {
		src.Takes = ps.ClipTakes()
		if ps.Clip != nil {
			sum, err := s.FileSHA256(ps.Clip.File)
			if err != nil {
				return nil, fmt.Errorf("scena %s: continua_da %s: la clip di partenza non si legge: %w", id, c, err)
			}
			src.File, src.SHA256 = ps.Clip.File, sum
		}
	}
	return src, nil
}

// sourceStale è il motivo per cui la clip di partenza di continua_da della
// scena id non è più quella dell'approvazione a; vuoto se lo è.
func (st *State) sourceStale(s *Storyboard, id string, a *Approval) string {
	scene := s.Scena(id)
	if scene == nil || scene.ContinuaDa == "" {
		return ""
	}
	c := scene.ContinuaDa
	if prev := s.Scena(c); prev == nil || !prev.Generata() {
		return ""
	}
	src := a.Source
	if src == nil || src.Scene != c {
		return "continua_da " + c + ": la clip di partenza non è quella approvata"
	}
	ps := st.Scenes[c]
	if src.File == "" {
		if ps != nil && ps.ClipTakes() > src.Takes+1 {
			return "continua_da " + c + ": la scena di partenza è stata rigenerata dopo l'approvazione"
		}
		return ""
	}
	if ps == nil || ps.Clip == nil || ps.Clip.File != src.File {
		return "continua_da " + c + ": la clip di partenza è cambiata"
	}
	sum, err := s.FileSHA256(src.File)
	if err != nil {
		return "continua_da " + c + ": la clip di partenza non si legge: " + err.Error()
	}
	if sum != src.SHA256 {
		return "continua_da " + c + ": la clip di partenza è cambiata"
	}
	return ""
}

// CheckApproval dice se l'approvazione della scena id vale ancora per la
// ricetta del definitivo recipe (RecipeKey adesso): nil se sì; altrimenti un
// errore con ErrNotApproved (nessuna approvazione) o ErrStaleApproval (ricetta
// cambiata, materiale approvato diverso da s.ApprovalTake(), nuova ripresa o
// altro fotogramma chiave dopo l'approvazione, file visto o fotogramma chiave
// cambiato o non più leggibile, clip di partenza di continua_da cambiata o
// rigenerata), col motivo. È la verifica del gate di giv story run.
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
	if want := s.ApprovalTake(); a.Take != want {
		return stale("approvato il materiale " + a.Take + ", lo storyboard ora chiede " + want)
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
	if kf != "" {
		ksum, err := s.FileSHA256(kf)
		if err != nil {
			return stale("il fotogramma chiave non si legge: " + err.Error())
		}
		if ksum != a.KeyframeSHA256 {
			return stale("il fotogramma chiave è cambiato")
		}
	}
	if why := st.sourceStale(s, id, a); why != "" {
		return stale(why)
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
