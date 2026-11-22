package story

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"gen-image-video-cli/internal/kit"
)

// Kit di marca e personaggi nello storyboard (decisioni K1-K3): il kit si usa
// solo se indicato (campo kit o --kit); i suoi valori fanno da predefiniti,
// campo per campo: vince la scena, poi lo storyboard, poi il kit. Con un kit
// logo, cartello finale e musica compaiono da soli. I file del kit restano
// nella sua cartella (kit.CheckPath) e nello storyboard fuso compaiono come
// percorsi assoluti, riconosciuti solo nei campi che ha riempito applyKit
// (kitFields): un percorso assoluto scritto nello storyboard, anche uguale a
// quello di un file del kit, resta rifiutato come prima.

// kitField è il file del campo kit rispetto alla cartella corrente: relativo
// alla cartella dello storyboard (anche fuori, K1) o assoluto; mai un URL.
func (s *Storyboard) kitField() (string, error) {
	p := s.Kit
	switch {
	case strings.TrimSpace(p) == "":
		return "", errors.New("kit vuoto: togli il campo o indica il file del kit")
	case strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return "", fmt.Errorf("kit %q: percorso con caratteri di controllo", p)
	case !utf8.ValidString(p):
		return "", errors.New("kit: percorso non UTF-8")
	case hasScheme(p):
		return "", errors.New("kit: serve il file del kit, non un URL")
	case filepath.IsAbs(p):
		return p, nil
	}
	return filepath.Join(s.Dir, filepath.FromSlash(p)), nil
}

// hasScheme: p comincia con uno schema di URL (http:, https:, file:…), in
// maiuscolo o minuscolo.
func hasScheme(p string) bool {
	i := strings.Index(p, "://")
	if i <= 0 {
		return false
	}
	for _, r := range p[:i] {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '+' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

// applyKit legge il kit in file e ne fonde i valori nello storyboard ancora
// da controllare: montaggio (logo, stile dei testi coi colori, cartello
// finale, musica, volume), personaggi come insiemi di riferimenti (uno con lo
// stesso nome nello storyboard vince) e voce. I campi presi dal kit vanno in
// Ereditati. Logo, cartello e musica in montaggio.senza non si prendono.
func (s *Storyboard) applyKit(file string) error {
	k, err := kit.Load(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("kit %s: kit non trovato", file)
		}
		lines := strings.Split(err.Error(), "\n")
		for i := range lines {
			lines[i] = "kit " + file + ": " + lines[i]
		}
		return errors.New(strings.Join(lines, "\n"))
	}
	s.KitFile, s.KitData = file, k
	s.kitPaths, s.kitFields = map[string]string{}, map[*string]string{}
	s.descrizioni, s.tutele = map[string]string{}, map[string]string{}
	var ered []string
	inh := func(name string) { ered = append(ered, name) }
	var absErr error
	// abs mette nel campo dst il file p del kit, in forma assoluta, e registra
	// il campo: solo lì il percorso vale come file del kit (checkField)
	abs := func(dst *string, p string) {
		a, err := filepath.Abs(k.Path(p))
		if err != nil {
			absErr = fmt.Errorf("kit %s: %s: %v", file, p, err)
			*dst = p
			return
		}
		*dst = a
		s.kitPaths[a] = p
		s.kitFields[dst] = p
	}
	str := func(dst *string, v, name string) {
		if *dst == "" && v != "" {
			*dst = v
			inh(name)
		}
	}
	num := func(dst **float64, v *float64, name string) {
		if *dst == nil && v != nil {
			x := *v
			*dst = &x
			inh(name)
		}
	}
	if mk := k.Marca; mk != nil {
		m := s.Montaggio
		if m == nil {
			m = &Montaggio{}
		}
		n := len(ered)
		var primario, testo string
		if c := mk.Colori; c != nil {
			primario, testo = c.Primario, c.Testo
		}
		if kl := mk.Logo; kl != nil && !s.Without("logo") {
			l := m.Logo
			if l == nil {
				l = &Logo{}
			}
			if l.File == "" {
				abs(&l.File, kl.File)
				inh("montaggio.logo.file")
			}
			str(&l.Posizione, kl.Posizione, "montaggio.logo.posizione")
			num(&l.Larghezza, kl.Larghezza, "montaggio.logo.larghezza")
			num(&l.Opacita, kl.Opacita, "montaggio.logo.opacita")
			num(&l.Da, kl.Da, "montaggio.logo.da")
			num(&l.A, kl.A, "montaggio.logo.a")
			m.Logo = l
		}
		if kst := mk.StileTesti; kst != nil || primario != "" || testo != "" {
			if kst == nil {
				kst = &kit.StileTesti{}
			}
			st := m.StileTesti
			if st == nil {
				st = &StileTesti{}
			}
			k0 := len(ered)
			if st.Font == "" && kst.Font != "" {
				abs(&st.Font, kst.Font)
				inh("montaggio.stile_testi.font")
			}
			str(&st.Colore, kst.Colore, "montaggio.stile_testi.colore")
			str(&st.Colore, testo, "montaggio.stile_testi.colore (colori.testo)")
			str(&st.Bordo, kst.Bordo, "montaggio.stile_testi.bordo")
			str(&st.Bordo, primario, "montaggio.stile_testi.bordo (colori.primario)")
			num(&st.Dimensione, kst.Dimensione, "montaggio.stile_testi.dimensione")
			if len(ered) > k0 {
				m.StileTesti = st
			}
		}
		if kc := mk.CartelloFinale; (kc != nil || (m.CartelloFinale != nil && primario != "")) && !s.Without("cartello") {
			if kc == nil {
				kc = &kit.Cartello{}
			}
			c := m.CartelloFinale
			if c == nil {
				c = &Cartello{}
			}
			num(&c.Durata, kc.Durata, "montaggio.cartello_finale.durata")
			str(&c.Sfondo, kc.Sfondo, "montaggio.cartello_finale.sfondo")
			str(&c.Sfondo, primario, "montaggio.cartello_finale.sfondo (colori.primario)")
			if c.Logo == "" && kc.Logo != "" {
				abs(&c.Logo, kc.Logo)
				inh("montaggio.cartello_finale.logo")
			}
			str(&c.Testo, kc.Testo, "montaggio.cartello_finale.testo")
			str(&c.Sottotesto, kc.Sottotesto, "montaggio.cartello_finale.sottotesto")
			m.CartelloFinale = c
		}
		if km := mk.Musica; km != nil && !s.Without("musica") {
			mu := m.Musica
			if mu == nil {
				mu = &Musica{}
			}
			if mu.File == "" {
				abs(&mu.File, km.File)
				inh("montaggio.musica.file")
			}
			num(&mu.Volume, km.Volume, "montaggio.musica.volume")
			// abbassarsi sotto la voce vale solo se c'è una voce
			if !mu.SottoVoce && km.SottoVoce && s.hasVoice() {
				mu.SottoVoce = true
				inh("montaggio.musica.sotto_voce")
			}
			num(&mu.SfumaFine, km.SfumaFine, "montaggio.musica.sfuma_fine")
			m.Musica = mu
		}
		num(&m.Volume, mk.Volume, "montaggio.volume")
		if len(ered) > n {
			s.Montaggio = m
		}
	}
	for _, name := range sortedKeys(k.Personaggi) {
		if _, ok := s.Riferimenti[name]; ok {
			continue // un insieme con lo stesso nome nello storyboard vince
		}
		p := k.Personaggi[name]
		refs := make([]string, len(p.Riferimenti))
		for i, r := range p.Riferimenti {
			abs(&refs[i], r)
		}
		if s.Riferimenti == nil {
			s.Riferimenti = map[string][]string{}
		}
		s.Riferimenti[name] = refs
		if d := strings.TrimSpace(p.Descrizione); d != "" {
			s.descrizioni[name] = d
		}
		if p.Tutela != "" {
			s.tutele[name] = p.Tutela
		}
		inh("riferimenti." + name)
	}
	if v := k.Voce; v != nil || len(k.Pronuncia) > 0 {
		p := s.Parlato
		if p == nil {
			p = &Parlato{}
		}
		if v != nil {
			str(&p.Provider, v.Provider, "parlato.provider")
			str(&p.Modello, v.Modello, "parlato.modello")
			str(&p.Voce, v.Voce, "parlato.voce")
			str(&p.Lingua, v.Lingua, "parlato.lingua")
			num(&p.Velocita, v.Velocita, "parlato.velocita")
			str(&p.Stile, v.Stile, "parlato.stile")
		}
		added := 0
		for _, w := range sortedKeys(k.Pronuncia) {
			if slices.ContainsFunc(sortedKeys(p.Pronuncia), func(x string) bool { return strings.EqualFold(x, w) }) {
				continue // la pronuncia dello storyboard vince, anche a meno delle maiuscole
			}
			if p.Pronuncia == nil {
				p.Pronuncia = map[string]string{}
			}
			p.Pronuncia[w] = k.Pronuncia[w]
			added++
		}
		if added > 0 {
			inh(fmt.Sprintf("parlato.pronuncia (%d parole)", added))
		}
		s.Parlato = p
	}
	if absErr != nil {
		return absErr
	}
	s.Ereditati = ered
	return nil
}

// Descrizione è la descrizione dei personaggi del kit che la scena usa (usa),
// nell'ordine di usa: va davanti al prompt della scena e del suo fotogramma
// chiave. Vuota senza kit o senza personaggi descritti.
func (s *Storyboard) Descrizione(sc *Scene) string {
	var parts []string
	for _, name := range sc.Usa {
		if d := s.descrizioni[name]; d != "" && !slices.Contains(parts, d) {
			parts = append(parts, d)
		}
	}
	return strings.Join(parts, " ")
}

// ConDescrizione è prompt con davanti la descrizione dei personaggi della
// scena (Descrizione), chiusa da un punto se non ha già una punteggiatura;
// senza descrizione, prompt com'è.
func (s *Storyboard) ConDescrizione(sc *Scene, prompt string) string {
	d := s.Descrizione(sc)
	if d == "" {
		return prompt
	}
	if !strings.ContainsAny(d[len(d)-1:], ".!?:;") {
		d += "."
	}
	return d + " " + prompt
}

// TutelaPersonaggi è la tutela del primo personaggio del kit, in usa, che ne
// indica una: vale per la scena se la scena non ne indica una.
func (s *Storyboard) TutelaPersonaggi(sc *Scene) string {
	for _, name := range sc.Usa {
		if t := s.tutele[name]; t != "" {
			return t
		}
	}
	return ""
}

// KitRel dice se p (come restituito da Path) è un file del kit messo nello
// storyboard e, se sì, il suo percorso relativo alla cartella del kit.
func (s *Storyboard) KitRel(p string) (string, bool) {
	rel, ok := s.kitPaths[p]
	return rel, ok
}
