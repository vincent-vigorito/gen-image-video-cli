package montage

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Cambio formato (giv post reframe; da riusare in giv export e in giv story
// cut su più formati): il video si porta a una misura d'uscita W×H in tre
// modi: FitCrop riempie il quadro tagliando i bordi, nel punto indicato dal
// fuoco; FitPad lo tiene intero con bande nere; FitBlur lo tiene intero sopra
// lo stesso video ingrandito e sfocato. I filtri usano iw e ih: valgono per
// qualunque misura in ingresso (clip diverse dello stesso montaggio, video
// ruotati) senza misurarla prima. Nella stringa dei filtri vanno solo numeri
// convalidati e valori dell'elenco chiuso.

// FitBlur tiene il video intero sopra lo stesso video sfocato che riempie il
// quadro (FitPad e FitCrop sono in plan.go).
const FitBlur = "blur"

// Aspect è un formato d'uscita: il rapporto W:H (es. 9:16).
type Aspect struct{ W, H int }

// Formati ammessi.
var (
	Aspect9x16 = Aspect{9, 16}
	Aspect1x1  = Aspect{1, 1}
	Aspect4x5  = Aspect{4, 5}
	Aspect16x9 = Aspect{16, 9}
)

// Aspects sono i formati ammessi, nell'ordine dell'aiuto.
func Aspects() []Aspect { return []Aspect{Aspect9x16, Aspect1x1, Aspect4x5, Aspect16x9} }

// String è il formato come si scrive nei flag e nello storyboard ("9:16").
func (a Aspect) String() string { return fmt.Sprintf("%d:%d", a.W, a.H) }

// Tag è il formato nei nomi dei file ("9x16").
func (a Aspect) Tag() string { return fmt.Sprintf("%dx%d", a.W, a.H) }

// aspectNames è l'elenco dei formati ammessi per i messaggi.
func aspectNames() string {
	var s []string
	for _, a := range Aspects() {
		s = append(s, a.String())
	}
	return strings.Join(s, ", ")
}

// ParseAspect legge un formato fra quelli ammessi ("9:16", "1:1", "4:5",
// "16:9"); altrimenti ErrInvalid.
func ParseAspect(s string) (Aspect, error) {
	for _, a := range Aspects() {
		if strings.TrimSpace(s) == a.String() {
			return a, nil
		}
	}
	return Aspect{}, invalid("formato %q: valori ammessi %s", s, aspectNames())
}

// Size è la misura d'uscita del formato per un video srcW×srcH: il lato
// corto resta quello del video (pari), il lungo segue il rapporto, arrotondato
// al pari (da un 1920×1080: 1080×1920, 1080×1080, 1080×1350, 1920×1080). Se
// il lato lungo supera il massimo del montaggio la misura si riduce.
func (a Aspect) Size(srcW, srcH int) (w, h int, err error) {
	if a.W <= 0 || a.H <= 0 {
		return 0, 0, invalid("formato %s non valido", a)
	}
	if srcW <= 0 || srcH <= 0 {
		return 0, 0, invalid("misure del video non lette (%dx%d)", srcW, srcH)
	}
	short := float64(min(srcW, srcH))
	ratio := float64(max(a.W, a.H)) / float64(min(a.W, a.H))
	long := short * ratio
	if long > maxSide {
		short, long = maxSide/ratio, maxSide
	}
	s, l := even(short), even(long)
	if s < 16 {
		return 0, 0, invalid("video di %dx%d: troppo piccolo per il formato %s (lato corto da almeno 16 pixel)", srcW, srcH, a)
	}
	if a.W > a.H {
		return l, s, nil
	}
	return s, l, nil
}

// even arrotonda al numero pari più vicino, senza superare maxSide.
func even(f float64) int {
	n := 2 * int(math.Round(f/2))
	if n > maxSide {
		n = maxSide
	}
	return n
}

// Focus è il punto da tenere nell'inquadratura quando FitCrop taglia: X e Y
// da 0 a 1 (0 = bordo sinistro o alto, 1 = destro o basso).
type Focus struct{ X, Y float64 }

// FocusCenter è il fuoco di default: il centro.
var FocusCenter = Focus{0.5, 0.5}

// Check controlla che X e Y siano numeri finiti da 0 a 1.
func (f Focus) Check() error {
	for _, v := range []float64{f.X, f.Y} {
		if !finite(v) || v < 0 || v > 1 {
			return invalid("fuoco %s,%s: x e y da 0 a 1 (0.5 = centro, decimali col punto)", num(f.X), num(f.Y))
		}
	}
	return nil
}

// ParseFocus legge "<x>" o "<x>,<y>" (decimali col punto, da 0 a 1); senza
// y vale 0.5; vuoto: FocusCenter.
func ParseFocus(s string) (Focus, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return FocusCenter, nil
	}
	parts := strings.Split(s, ",")
	if len(parts) > 2 {
		return Focus{}, invalid("fuoco %q: si scrive <x> oppure <x>,<y>, da 0 a 1, decimali col punto", s)
	}
	f := FocusCenter
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return Focus{}, invalid("fuoco %q: si scrive <x> oppure <x>,<y>, da 0 a 1, decimali col punto", s)
		}
		if i == 0 {
			f.X = v
		} else {
			f.Y = v
		}
	}
	if err := f.Check(); err != nil {
		return Focus{}, err
	}
	return f, nil
}

// Reframe è un cambio formato: la misura d'uscita, il modo e il fuoco.
type Reframe struct {
	Width  int    // larghezza d'uscita, pari, da 16 a maxSide (Aspect.Size o un profilo)
	Height int    // altezza d'uscita, pari, da 16 a maxSide
	Fit    string // FitCrop, FitPad o FitBlur
	Focus  *Focus // solo FitCrop: il punto da tenere; nil = FocusCenter
}

// Check controlla misura, modo e fuoco.
func (r Reframe) Check() error {
	if r.Width < 16 || r.Width > maxSide || r.Width%2 != 0 || r.Height < 16 || r.Height > maxSide || r.Height%2 != 0 {
		return invalid("misura d'uscita %dx%d: larghezza e altezza pari, da 16 a %d", r.Width, r.Height, maxSide)
	}
	switch r.Fit {
	case FitCrop, FitPad, FitBlur:
	default:
		return invalid("adattamento %q: valori ammessi %s, %s, %s", r.Fit, FitCrop, FitPad, FitBlur)
	}
	if r.Focus != nil {
		return r.Focus.Check()
	}
	return nil
}

// focus è il fuoco effettivo.
func (r Reframe) focus() Focus {
	if r.Focus == nil {
		return FocusCenter
	}
	return *r.Focus
}

// scaleTo è lo scale verso W×H del montaggio: "increase" copre il quadro,
// "decrease" ci sta dentro.
func scaleTo(w, h int, mode string) string {
	return fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=%s:force_divisible_by=2:out_color_matrix=bt709:out_range=tv", w, h, mode)
}

// cropChain è scale e crop che coprono W×H tagliando i bordi nel punto f.
// Col fuoco al centro coincide con l'adattamento FitCrop del montaggio.
func cropChain(w, h int, f Focus) string {
	c := scaleTo(w, h, "increase") + fmt.Sprintf(",crop=%d:%d", w, h)
	if f != FocusCenter {
		c += fmt.Sprintf(":(iw-ow)*%s:(ih-oh)*%s", num(f.X), num(f.Y))
	}
	return c
}

// padChain è scale e pad: il video intero al centro, bande nere. Coincide con
// l'adattamento FitPad del montaggio.
func padChain(w, h int) string {
	return scaleTo(w, h, "decrease") + fmt.Sprintf(",pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black", w, h)
}

// blurRadius è il raggio dello sfondo sfocato: circa 1/24 del lato corto,
// entro il limite di boxblur sui piani del colore (un quarto del lato corto).
func blurRadius(w, h int) int {
	s := min(w, h)
	return max(1, min(s/24, s/4))
}

// Chain è il cambio formato come catena di filtri senza etichette (FitCrop e
// FitPad; FitBlur ha due rami e vuole Graph). Col fuoco al centro coincide con
// l'adattamento del montaggio (Plan.Fit) alla stessa misura; non mette setsar.
func (r Reframe) Chain() (string, error) {
	if err := r.Check(); err != nil {
		return "", err
	}
	switch r.Fit {
	case FitCrop:
		return cropChain(r.Width, r.Height, r.focus()), nil
	case FitPad:
		return padChain(r.Width, r.Height), nil
	}
	return "", invalid("adattamento %s: due rami di filtri, serve Graph", r.Fit)
}

// Graph è il cambio formato come pezzo del grafo dei filtri, dall'etichetta
// in (es. "0:v:0") all'etichetta out, con setsar=1 in fondo. FitBlur usa
// etichette interne <out>_a, <out>_b, <out>_bg, <out>_fg. Le etichette sono
// fatte solo di lettere, cifre, '_' e ':'.
func (r Reframe) Graph(in, out string) (string, error) {
	if !filterLabel(in) || !filterLabel(out) || strings.Contains(out, ":") {
		return "", fmt.Errorf("etichette del grafo non valide: %q, %q", in, out)
	}
	if err := r.Check(); err != nil {
		return "", err
	}
	w, h := r.Width, r.Height
	switch r.Fit {
	case FitCrop, FitPad:
		c, err := r.Chain()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("[%s]%s,setsar=1[%s]", in, c, out), nil
	}
	a, b, bg, fg := out+"_a", out+"_b", out+"_bg", out+"_fg"
	return fmt.Sprintf("[%s]split=2[%s][%s];"+
		"[%s]%s,crop=%d:%d,boxblur=luma_radius=%d:luma_power=2[%s];"+
		"[%s]%s[%s];"+
		"[%s][%s]overlay=(W-w)/2:(H-h)/2,setsar=1[%s]",
		in, a, b,
		a, scaleTo(w, h, "increase"), w, h, blurRadius(w, h), bg,
		b, scaleTo(w, h, "decrease"), fg,
		bg, fg, out), nil
}

// filterLabel accetta un'etichetta del grafo: lettere ASCII, cifre, '_' e ':'.
func filterLabel(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != ':' {
			return false
		}
	}
	return true
}

// ReframeArgs sono gli argomenti di ffmpeg (senza il programma) di giv post
// reframe: src (video) portato al formato r in dst (.mp4), audio della prima
// traccia ricodificato se c'è, ffmpeg che non scrive su un file esistente.
func ReframeArgs(src, dst string, r Reframe) ([]string, error) {
	g, err := r.Graph("0:v:0", "v")
	if err != nil {
		return nil, err
	}
	in, err := inputArgs(src, videoFormats)
	if err != nil {
		return nil, err
	}
	out, err := mp4Out(dst)
	if err != nil {
		return nil, err
	}
	args := append(ffArgs(), in...)
	args = append(args, "-filter_complex", g, "-map", "[v]", "-map", "0:a:0?")
	args = append(args, h264Args()...)
	args = append(args, aacArgs()...)
	return append(args, out...), nil
}
