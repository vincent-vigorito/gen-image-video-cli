package montage

import (
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

// Sovrimpressioni (giv post overlay; i campi logo, titoli e cartello_finale
// dello storyboard): un logo posato sul video in una finestra di tempo, titoli
// a tempo, un cartello finale generato (colore pieno, immagine di sfondo, logo,
// testo e sottotesto) accodato dopo il video, tutto dentro i margini sicuri
// del quadro. I testi passano solo dal file .ass (ASS); nella stringa dei
// filtri vanno solo numeri calcolati da giv, colori esadecimali ed etichette
// fisse; logo e immagini sono ingressi di ffmpeg (inputArgs, solo formati di
// immagine).

// Valori di default delle sovrimpressioni.
const (
	DefaultLogoWidth     = 0.12 // larghezza del logo sul video, in frazione del quadro
	DefaultCardLogoWidth = 0.4  // larghezza massima del logo del cartello, in frazione del quadro
	DefaultCardSeconds   = 3.0  // durata del cartello finale
	DefaultTitleSeconds  = 3.0  // durata di un titolo
)

const (
	maxTextBytes   = 2000 // tetto di un testo (titolo, testo del cartello)
	maxCardSeconds = 60   // tetto della durata del cartello finale
	maxTextFade    = 0.3  // dissolvenza dei testi, secondi (TextFade)
	// timeSlack è quanto un tempo può passare la durata misurata del video
	// (un fotogramma a 20 fps): la durata letta da ffprobe è arrotondata.
	timeSlack = 0.05
)

// Rect è un riquadro in pixel nel quadro: angolo in alto a sinistra e misure.
type Rect struct{ X, Y, W, H int }

// Logo è un'immagine posata sul video.
type Logo struct {
	Path    string   // immagine png, jpg o webp: la trasparenza resta
	SrcW    int      // larghezza dell'immagine in pixel (misurata con Probe)
	SrcH    int      // altezza dell'immagine in pixel
	Pos     Position // posizione nel quadro, dentro i margini
	Width   float64  // larghezza in frazione di quella del quadro: da 0 (escluso) a 1
	Opacity float64  // opacità da 0 (escluso) a 1; 0 vale 1
	From    float64  // da quando si vede, secondi dall'inizio del video
	To      float64  // fino a quando, secondi; 0: fino alla fine del video
}

// EndCard è il cartello finale, accodato dopo il video: colore pieno, sopra
// l'immagine di sfondo (contenuta nel quadro), il logo, il testo al centro e
// il sottotesto in basso, dentro i margini. Il video sotto il cartello è
// muto.
type EndCard struct {
	Duration float64 // secondi: da 0 (escluso) a 60
	Color    Color   // colore dello sfondo, anche attorno all'immagine
	Image    string  // immagine di sfondo (png, jpg, webp); vuoto: solo il colore
	// Logo è il logo del cartello: contano Path, SrcW, SrcH, Width (0:
	// DefaultCardLogoWidth) e Opacity; posizione e tempi li sceglie il
	// cartello (al centro, o sopra il testo), rimpicciolito se non ci sta.
	Logo    *Logo
	Text    string    // testo al centro; vuoto: nessuno
	Subtext string    // testo in basso, più piccolo; vuoto: nessuno
	Style   TextStyle // stile del testo (il sottotesto è alto 2/3); Size 0: CardTextStyle
}

// Overlay è il lavoro di giv post overlay su un video: logo, titoli e
// cartello finale in una passata di ffmpeg (OverlayArgs, Build).
type Overlay struct {
	Video    string  // video in ingresso
	Width    int     // larghezza del video in pixel (Probe), pari
	Height   int     // altezza del video in pixel (Probe), pari
	FPS      float64 // fotogrammi al secondo (Probe): servono al cartello
	Duration float64 // durata del video in secondi (Probe)
	Audio    bool    // il video ha una traccia audio
	Margins  Margins // margini sicuri (SafeMargins per quelli di default)
	Logo     *Logo   // logo sul video; nil: nessuno
	Titles   []Text  // titoli, tempi dall'inizio del video, dentro la sua durata
	EndCard  *EndCard
	Fonts    []string // file di font dei testi (.ttf, .otf, .ttc), copiati con Work.Fonts
}

// TextFade è la dissolvenza di default di un testo lungo d secondi: 0,3 s, al
// più un quarto del testo.
func TextFade(d float64) float64 {
	if !finite(d) || d <= 0 {
		return 0
	}
	return math.Min(maxTextFade, d/4)
}

// CardTextStyle è lo stile di default del testo del cartello per un quadro
// alto h pixel: quello dei titoli (DefaultTextStyle), alto 1/12 del quadro.
func CardTextStyle(h int) TextStyle {
	st := DefaultTextStyle(h)
	st.Size = max(h/12, 12)
	st.Border = math.Max(1, math.Round(float64(st.Size)/16))
	return st
}

// CheckImage controlla un'immagine da posare (logo, sfondo del cartello) con
// le misure lette da Probe: estensione .png, .jpg, .jpeg o .webp, letta da
// ffprobe come immagine (non un video né un audio), lati da 1 a 7680 pixel.
func CheckImage(path string, info *Info) error {
	if err := checkPath(path); err != nil {
		return invalid("immagine: %v", err)
	}
	if !imageExt[strings.ToLower(filepath.Ext(path))] {
		return invalid("%s: serve un'immagine .png, .jpg o .webp", path)
	}
	if info == nil || info.Video == nil {
		return invalid("%s: nessuna immagine leggibile", path)
	}
	image := false
	for _, f := range strings.Split(info.Format, ",") {
		if strings.Contains(","+imageFormats+",", ","+f+",") {
			image = true
		}
	}
	if !image {
		return invalid("%s: non è un'immagine (formato %s)", path, info.Format)
	}
	if v := info.Video; v.Width < 1 || v.Height < 1 || v.Width > maxSide || v.Height > maxSide {
		return invalid("%s: misure %dx%d fuori dai limiti (da 1 a %d pixel)", path, v.Width, v.Height, maxSide)
	}
	return nil
}

// checkFrame controlla le misure del quadro: lati pari da 16 a 7680.
func checkFrame(w, h int) error {
	if w < 16 || w > maxSide || w%2 != 0 || h < 16 || h > maxSide || h%2 != 0 {
		return invalid("quadro %dx%d: larghezza e altezza pari, da 16 a %d", w, h, maxSide)
	}
	return nil
}

// checkLabels controlla le etichette dei filtri passate dal chiamante: da 1 a
// 32 fra lettere, cifre, '_' e ':' (es. "0:v", "vlogo"), nient'altro.
func checkLabels(labels ...string) error {
	for _, l := range labels {
		if l == "" || len(l) > 32 {
			return invalid("etichetta di filtro %q non valida", l)
		}
		for _, r := range l {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != ':' {
				return invalid("etichetta di filtro %q non valida", l)
			}
		}
	}
	return nil
}

// checkText controlla un testo: al più maxTextBytes.
func checkText(what, s string) error {
	if len(s) > maxTextBytes {
		return invalid("%s: al più %d byte", what, maxTextBytes)
	}
	return nil
}

// checkImage controlla i valori di un logo comuni al video e al cartello.
func (l Logo) checkImage(what string) error {
	if err := checkPath(l.Path); err != nil {
		return invalid("%s: %v", what, err)
	}
	if l.SrcW < 1 || l.SrcH < 1 || l.SrcW > maxSide || l.SrcH > maxSide {
		return invalid("%s %s: misure %dx%d non valide", what, l.Path, l.SrcW, l.SrcH)
	}
	if !finite(l.Width) || l.Width < 0 || l.Width > 1 {
		return invalid("%s: larghezza %v: frazione del quadro da 0 (escluso) a 1", what, l.Width)
	}
	if !finite(l.Opacity) || l.Opacity < 0 || l.Opacity > 1 {
		return invalid("%s: opacità %v: da 0 (escluso) a 1", what, l.Opacity)
	}
	return nil
}

// Box è il riquadro del logo in un quadro w×h coi margini m: largo Width
// del quadro, alto in proporzione all'immagine, nella posizione Pos dentro i
// margini. ErrInvalid se il logo non sta nei margini.
func (l Logo) Box(w, h int, m Margins) (Rect, error) {
	if err := checkFrame(w, h); err != nil {
		return Rect{}, err
	}
	if err := m.Check(); err != nil {
		return Rect{}, invalid("logo: %v", err)
	}
	if err := l.checkImage("logo"); err != nil {
		return Rect{}, err
	}
	if l.Width == 0 {
		return Rect{}, invalid("logo: larghezza 0: frazione del quadro da 0 (escluso) a 1")
	}
	if l.Pos.Align() == 0 {
		return Rect{}, invalid("logo: posizione %q sconosciuta (valide: %s)", l.Pos, positionList)
	}
	lw := max(1, int(math.Round(l.Width*float64(w))))
	lh := max(1, int(math.Round(float64(lw)*float64(l.SrcH)/float64(l.SrcW))))
	t, b, le, r := m.Pixels(w, h)
	if lw > w-le-r || lh > h-t-b {
		return Rect{}, invalid("logo %dx%d: non sta nei margini sicuri del quadro %dx%d (al più %dx%d): riduci la larghezza", lw, lh, w, h, w-le-r, h-t-b)
	}
	x, y := l.Pos.Place(w, h, lw, lh, m)
	return Rect{X: x, Y: y, W: lw, H: lh}, nil
}

// checkTimes controlla la finestra di tempo del logo su un video lungo d secondi.
func (l Logo) checkTimes(d float64) error {
	if !finite(l.From) || l.From < 0 || l.From >= d {
		return invalid("logo: inizio %v s fuori dal video (da 0 a %v s)", l.From, num(d))
	}
	if !finite(l.To) || l.To < 0 || l.To > 0 && l.To <= l.From {
		return invalid("logo: fine %v s non valida: dopo l'inizio (%v s), 0 per la fine del video", l.To, l.From)
	}
	if l.To > d+timeSlack {
		return invalid("logo: fine %v s dopo la fine del video (%v s)", l.To, num(d))
	}
	return nil
}

// imageChain scala un'immagine a w×h (alfa compreso) con l'opacità op (0 o 1:
// piena).
func imageChain(w, h int, op float64) string {
	s := fmt.Sprintf("scale=w=%d:h=%d:flags=lanczos,format=rgba", w, h)
	if op > 0 && op < 1 {
		s += ",colorchannelmixer=aa=" + num(op)
	}
	return s
}

// LogoFilter è la catena di filtri che posa il logo l (l'immagine
// all'etichetta logo) sul video all'etichetta in, un quadro w×h coi margini
// m, e chiama out il risultato. Il logo si vede da From a To (To 0: fino alla
// fine del video). I tempi rispetto alla durata li controlla il chiamante
// (Overlay.Check).
func LogoFilter(in, logo, out string, l Logo, w, h int, m Margins) (string, error) {
	if err := checkLabels(in, logo, out); err != nil {
		return "", err
	}
	box, err := l.Box(w, h, m)
	if err != nil {
		return "", err
	}
	if !finite(l.From) || l.From < 0 || !finite(l.To) || l.To < 0 || l.To > 0 && l.To <= l.From {
		return "", invalid("logo: finestra di tempo da %v a %v s non valida", l.From, l.To)
	}
	enable := ""
	switch {
	case l.To > 0:
		enable = ":enable='between(t," + num(l.From) + "," + num(l.To) + ")'"
	case l.From > 0:
		enable = ":enable='gte(t," + num(l.From) + ")'"
	}
	lg := out + "_logo"
	return fmt.Sprintf("[%s]%s[%s];[%s][%s]overlay=x=%d:y=%d%s[%s]",
		logo, imageChain(box.W, box.H, l.Opacity), lg, in, lg, box.X, box.Y, enable, out), nil
}

// style è lo stile del testo del cartello e quello del sottotesto (2/3).
func (c EndCard) style(h int) (text, sub TextStyle) {
	text = c.Style
	if text.Size == 0 {
		text = CardTextStyle(h)
	}
	sub = text
	sub.Size = max(text.Size*2/3, 1)
	return text, sub
}

// check controlla i valori del cartello.
func (c EndCard) check() error {
	if !finite(c.Duration) || c.Duration <= 0 || c.Duration > maxCardSeconds {
		return invalid("cartello finale: durata %v s: da 0 (escluso) a %d s", c.Duration, maxCardSeconds)
	}
	if c.Image != "" {
		if err := checkPath(c.Image); err != nil {
			return invalid("cartello finale: immagine: %v", err)
		}
	}
	if err := checkText("cartello finale: testo", c.Text); err != nil {
		return err
	}
	if err := checkText("cartello finale: sottotesto", c.Subtext); err != nil {
		return err
	}
	if c.Logo != nil {
		if err := c.Logo.checkImage("cartello finale: logo"); err != nil {
			return err
		}
	}
	return nil
}

// LogoBox è il riquadro del logo del cartello in un quadro w×h coi margini m:
// al centro della zona sicura senza testi; sopra il testo (che sta al centro
// del quadro) se c'è; sopra il sottotesto se c'è solo quello. Largo al più
// Logo.Width del quadro (0: DefaultCardLogoWidth), rimpicciolito in
// proporzione se non ci sta in altezza. ErrInvalid se non resta spazio.
func (c EndCard) LogoBox(w, h int, m Margins) (Rect, error) {
	if c.Logo == nil {
		return Rect{}, invalid("cartello finale: nessun logo")
	}
	if err := checkFrame(w, h); err != nil {
		return Rect{}, err
	}
	if err := m.Check(); err != nil {
		return Rect{}, invalid("cartello finale: %v", err)
	}
	l := *c.Logo
	if err := l.checkImage("cartello finale: logo"); err != nil {
		return Rect{}, err
	}
	frac := l.Width
	if frac == 0 {
		frac = DefaultCardLogoWidth
	}
	t, b, le, r := m.Pixels(w, h)
	text, sub := c.style(h)
	top, bottom := t, h-b // fascia verticale del logo
	switch {
	case strings.TrimSpace(c.Text) != "":
		bottom = h/2 - text.Size // il testo sta al centro, alto circa text.Size
		if line, _, ok := c.stacked(h, m); ok {
			bottom = line - text.Size*3/2 // il testo sale: finisce a line
		}
	case strings.TrimSpace(c.Subtext) != "":
		bottom = h - b - sub.Size*3/2
	}
	aw, ah := w-le-r, bottom-top
	if ah < 16 || aw < 16 {
		return Rect{}, invalid("cartello finale: nessuno spazio per il logo sopra il testo (quadro %dx%d)", w, h)
	}
	lw := min(max(1, int(math.Round(frac*float64(w)))), aw)
	lh := max(1, int(math.Round(float64(lw)*float64(l.SrcH)/float64(l.SrcW))))
	if lh > ah {
		lh = ah
		lw = max(1, int(math.Round(float64(lh)*float64(l.SrcW)/float64(l.SrcH))))
	}
	return Rect{X: le + (aw-lw)/2, Y: top + (ah-lh)/2, W: lw, H: lh}, nil
}

// EndCardFilter è la catena di filtri che genera il cartello: un quadro w×h a
// fps fotogrammi al secondo, lungo Duration, col colore pieno, l'immagine di
// sfondo (all'etichetta image, "" se non c'è) contenuta nel quadro e il logo
// (all'etichetta logo, "" se non c'è) in LogoBox; l'uscita out è yuv420p a
// pixel quadrati, pronta per concat. I testi del cartello non sono qui:
// vanno nel file .ass (EndCardTexts).
func EndCardFilter(c EndCard, image, logo, out string, w, h int, fps float64, m Margins) (string, error) {
	if err := checkLabels(out); err != nil {
		return "", err
	}
	if err := checkFrame(w, h); err != nil {
		return "", err
	}
	if err := c.check(); err != nil {
		return "", err
	}
	if !finite(fps) || fps <= 0 || fps > maxFPS {
		return "", invalid("cartello finale: %v fotogrammi al secondo: da 0 (escluso) a %d", fps, maxFPS)
	}
	if (image != "") != (c.Image != "") || (logo != "") != (c.Logo != nil) {
		return "", invalid("cartello finale: ingressi di immagine e logo non corrispondono al cartello")
	}
	g := []string{fmt.Sprintf("color=c=%s:s=%dx%d:r=%s:d=%s[%s_0]", c.Color.Hex(), w, h, num(fps), num(c.Duration), out)}
	cur := out + "_0"
	if image != "" {
		if err := checkLabels(image); err != nil {
			return "", err
		}
		g = append(g, fmt.Sprintf("[%s]scale=w=%d:h=%d:force_original_aspect_ratio=decrease,format=rgba[%s_img]", image, w, h, out),
			fmt.Sprintf("[%s][%s_img]overlay=x=(W-w)/2:y=(H-h)/2[%s_1]", cur, out, out))
		cur = out + "_1"
	}
	if logo != "" {
		if err := checkLabels(logo); err != nil {
			return "", err
		}
		box, err := c.LogoBox(w, h, m)
		if err != nil {
			return "", err
		}
		g = append(g, fmt.Sprintf("[%s]%s[%s_logo]", logo, imageChain(box.W, box.H, c.Logo.Opacity), out),
			fmt.Sprintf("[%s][%s_logo]overlay=x=%d:y=%d[%s_2]", cur, out, box.X, box.Y, out))
		cur = out + "_2"
	}
	g = append(g, fmt.Sprintf("[%s]format=yuv420p,setsar=1[%s]", cur, out))
	return strings.Join(g, ";"), nil
}

// stacked dice se nel quadro alto h coi margini m il sottotesto va subito
// sotto il testo invece che in basso: quando fra il testo al centro e il
// margine basso non restano due righe di sottotesto (i verticali, col margine
// basso del 35%), il sottotesto in basso finirebbe sul testo. Allora line è
// l'ordinata in pixel dove finisce il testo, alzato quanto basta a lasciare
// due righe di sottotesto dentro i margini (non oltre il margine alto), e gap
// lo stacco fra testo e sottotesto.
func (c EndCard) stacked(h int, m Margins) (line, gap int, ok bool) {
	if strings.TrimSpace(c.Text) == "" || strings.TrimSpace(c.Subtext) == "" {
		return 0, 0, false
	}
	text, sub := c.style(h)
	t, b, _, _ := m.Pixels(0, h) // solo alto e basso
	gap = max(1, sub.Size/4)
	line = h - b - 2*sub.Size - gap
	if line >= h/2+text.Size/2 {
		return 0, 0, false
	}
	return max(line, t+text.Size), gap, true
}

// EndCardTexts sono i testi del cartello che comincia a start secondi, per un
// quadro alto h pixel coi margini m: il testo al centro, il sottotesto in
// basso, con la dissolvenza di default (TextFade). Se in basso il sottotesto
// non ci sta (stacked) il testo sale un poco e il sottotesto va subito sotto.
// Vanno nel file .ass coi titoli.
func EndCardTexts(c EndCard, start float64, h int, m Margins) []Text {
	text, sub := c.style(h)
	end := start + c.Duration
	fade := TextFade(c.Duration)
	if line, gap, ok := c.stacked(h, m); ok {
		return []Text{
			{Text: c.Text, Start: start, End: end, Pos: Bottom, Style: text, Fade: fade, MarginV: max(1, h-line)},
			{Text: c.Subtext, Start: start, End: end, Pos: Top, Style: sub, Fade: fade, MarginV: max(1, line+gap)},
		}
	}
	var ts []Text
	if strings.TrimSpace(c.Text) != "" {
		ts = append(ts, Text{Text: c.Text, Start: start, End: end, Pos: Center, Style: text, Fade: fade})
	}
	if strings.TrimSpace(c.Subtext) != "" {
		ts = append(ts, Text{Text: c.Subtext, Start: start, End: end, Pos: Bottom, Style: sub, Fade: fade})
	}
	return ts
}

// Texts sono tutti i testi del lavoro, per ASS: i titoli e quelli del
// cartello, che comincia alla fine del video.
func (o Overlay) Texts() []Text {
	ts := append([]Text(nil), o.Titles...)
	if o.EndCard != nil {
		ts = append(ts, EndCardTexts(*o.EndCard, o.Duration, o.Height, o.Margins)...)
	}
	return ts
}

// TotalDuration è la durata dell'uscita: il video più il cartello.
func (o Overlay) TotalDuration() float64 {
	if o.EndCard != nil {
		return o.Duration + o.EndCard.Duration
	}
	return o.Duration
}

// Check controlla il lavoro (ErrInvalid): misure del video, margini, logo
// dentro i margini e dentro la durata, titoli dentro la durata, cartello,
// font, e il file .ass dei testi.
func (o Overlay) Check() error {
	if err := checkPath(o.Video); err != nil {
		return invalid("video: %v", err)
	}
	if err := checkFrame(o.Width, o.Height); err != nil {
		return invalid("video: %s", strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
	}
	if !finite(o.Duration) || o.Duration <= 0 || o.Duration > 24*3600 {
		return invalid("video: durata %v s non misurata o fuori dai limiti", o.Duration)
	}
	if err := o.Margins.Check(); err != nil {
		return invalid("%v", err)
	}
	if o.Logo == nil && len(o.Titles) == 0 && o.EndCard == nil {
		return invalid("niente da posare: serve un logo, un titolo o il cartello finale")
	}
	if o.Logo != nil {
		if _, err := o.Logo.Box(o.Width, o.Height, o.Margins); err != nil {
			return err
		}
		if err := o.Logo.checkTimes(o.Duration); err != nil {
			return err
		}
	}
	for i, t := range o.Titles {
		n := i + 1
		if strings.TrimSpace(t.Text) == "" {
			return invalid("titolo %d: testo vuoto", n)
		}
		if err := checkText(fmt.Sprintf("titolo %d", n), t.Text); err != nil {
			return err
		}
		if !finite(t.Start) || !finite(t.End) || t.Start >= o.Duration {
			return invalid("titolo %d: inizio %v s fuori dal video (da 0 a %v s)", n, t.Start, num(o.Duration))
		}
		if t.End > o.Duration+timeSlack {
			return invalid("titolo %d: finisce a %v s, dopo la fine del video (%v s)", n, num(t.End), num(o.Duration))
		}
	}
	if c := o.EndCard; c != nil {
		if err := c.check(); err != nil {
			return err
		}
		if !finite(o.FPS) || o.FPS <= 0 || o.FPS > maxFPS {
			return invalid("cartello finale: il video ha %v fotogrammi al secondo (da 0 escluso a %d)", o.FPS, maxFPS)
		}
		if c.Logo != nil {
			if _, err := c.LogoBox(o.Width, o.Height, o.Margins); err != nil {
				return err
			}
		}
	}
	for _, f := range o.Fonts {
		if err := checkPath(f); err != nil {
			return invalid("font: %v", err)
		}
		if !fontExt[strings.ToLower(filepath.Ext(f))] {
			return invalid("font %s: serve un file .ttf, .otf o .ttc", f)
		}
	}
	if ts := o.Texts(); len(ts) > 0 {
		if _, err := ASS(o.Width, o.Height, o.Margins, ts); err != nil {
			return err
		}
	}
	return nil
}

// OverlayArgs costruisce gli argomenti di ffmpeg (senza il nome del
// programma) del lavoro, senza eseguire niente: il video è l'ingresso 0, poi
// logo, immagine e logo del cartello; il logo si posa sul video, il cartello
// si accoda (con audio muto se il video ha l'audio), i testi (titoli e
// cartello) si incidono alla fine dal file .ass assPath (scritto da ASS con
// o.Texts(); "" se non ci sono testi), coi font della cartella fontsDir ("":
// solo quelli del sistema). L'uscita out è un .mp4 H.264/AAC.
func OverlayArgs(o Overlay, assPath, fontsDir, out string) ([]string, error) {
	if err := o.Check(); err != nil {
		return nil, err
	}
	texts := o.Texts()
	if len(texts) > 0 && assPath == "" {
		return nil, invalid("testi senza il file .ass")
	}
	args := ffArgs()
	in, err := inputArgs(o.Video, videoFormats)
	if err != nil {
		return nil, err
	}
	args = append(args, in...)
	next := 1
	addImage := func(path string) (string, error) {
		a, err := inputArgs(path, imageFormats)
		if err != nil {
			return "", err
		}
		args = append(args, a...)
		label := strconv.Itoa(next) + ":v"
		next++
		return label, nil
	}
	var g []string
	v := "0:v"
	if o.Logo != nil {
		lbl, err := addImage(o.Logo.Path)
		if err != nil {
			return nil, err
		}
		f, err := LogoFilter(v, lbl, "vlogo", *o.Logo, o.Width, o.Height, o.Margins)
		if err != nil {
			return nil, err
		}
		g = append(g, f)
		v = "vlogo"
	}
	audio := ""
	if c := o.EndCard; c != nil {
		var img, lg string
		if c.Image != "" {
			if img, err = addImage(c.Image); err != nil {
				return nil, err
			}
		}
		if c.Logo != nil {
			if lg, err = addImage(c.Logo.Path); err != nil {
				return nil, err
			}
		}
		f, err := EndCardFilter(*c, img, lg, "vcard", o.Width, o.Height, o.FPS, o.Margins)
		if err != nil {
			return nil, err
		}
		g = append(g, f, fmt.Sprintf("[%s]format=yuv420p,setsar=1[vmain]", v))
		if o.Audio {
			g = append(g,
				fmt.Sprintf("[0:a]%s,apad,atrim=duration=%s[amain]", audioFmt, num(o.Duration)),
				fmt.Sprintf("anullsrc=r=%d:cl=stereo,atrim=duration=%s[acard]", sampleRate, num(c.Duration)),
				"[vmain][amain][vcard][acard]concat=n=2:v=1:a=1[vcat][acat]")
			audio = "[acat]"
		} else {
			g = append(g, "[vmain][vcard]concat=n=2:v=1:a=0[vcat]")
		}
		v = "vcat"
	} else if o.Audio {
		audio = "0:a:0"
	}
	if len(texts) > 0 {
		f, err := ASSFilter(assPath, fontsDir)
		if err != nil {
			return nil, err
		}
		g = append(g, "["+v+"]"+f+"[vout]")
		v = "vout"
	}
	args = append(args, "-filter_complex", strings.Join(g, ";"), "-map", "["+v+"]")
	if audio != "" {
		args = append(args, "-map", audio)
	}
	args = append(args, h264Args()...)
	if audio != "" {
		args = append(args, aacArgs()...)
	}
	o2, err := mp4Out(out)
	if err != nil {
		return nil, err
	}
	return append(args, o2...), nil
}

// Build scrive il risultato in staged (per Op.Build): il file .ass dei testi
// e i font nella cartella privata, poi una passata di ffmpeg.
func (o Overlay) Build(w *Work, staged string) error {
	if err := o.Check(); err != nil {
		return err
	}
	var assPath, fontsDir string
	if ts := o.Texts(); len(ts) > 0 {
		data, err := ASS(o.Width, o.Height, o.Margins, ts)
		if err != nil {
			return err
		}
		if assPath, err = w.WriteFile("testi.ass", data); err != nil {
			return err
		}
		if len(o.Fonts) > 0 {
			if fontsDir, err = w.Fonts(o.Fonts...); err != nil {
				return err
			}
		}
	}
	args, err := OverlayArgs(o, assPath, fontsDir, staged)
	if err != nil {
		return err
	}
	return w.FFmpeg(args)
}
