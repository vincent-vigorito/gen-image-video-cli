package judge

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// Price è la tariffa di un modello visivo, valida da From (zero = da sempre)
// fino alla tariffa successiva dello stesso modello: token in ingresso
// (testo e immagini) e in uscita (ragionamento compreso).
type Price struct {
	Model               string
	From                time.Time
	USDPerMInputTokens  float64
	USDPerMOutputTokens float64
	Source              string
}

// Prices è il listino del giudizio. gemini-2.5-flash-lite: 0,10 $ per milione
// di token in ingresso (testo, immagini, video) e 0,40 $ in uscita (pagina
// prezzi di Gemini, verifiche di giv 0.11.0); da riverificare prima di ogni
// prova a pagamento. Un modello senza tariffa con un tetto di spesa non parte.
var Prices = []Price{
	{Model: DefaultModel, USDPerMInputTokens: 0.10, USDPerMOutputTokens: 0.40, Source: "pagina prezzi della Gemini API, verifiche di giv 0.11.0"},
}

// Token per riquadro d'immagine (Gemini: 258 per un'immagine entro 384×384,
// oltre a riquadri da 258 ciascuno).
const (
	tokensPerTile = 258
	smallSide     = 384
	// promptMargin sono i token di contorno della richiesta oltre al testo.
	promptMargin = 256
)

// ImageTokens è il massimo dei token di un'immagine w×h, per eccesso:
// 258 entro 384×384, altrimenti 258 per riquadro, coi riquadri larghi due
// terzi del lato corto (il taglio usato da Gemini per le immagini grandi).
func ImageTokens(w, h int) int64 {
	if w <= smallSide && h <= smallSide {
		return tokensPerTile
	}
	unit := max(min(w, h)*2/3, 1)
	tiles := int64((w+unit-1)/unit) * int64((h+unit-1)/unit)
	return tokensPerTile * tiles
}

// Estimate è la stima di un giudizio: per eccesso, sul massimo dei token in
// ingresso (testo contato un token per byte del corpo senza le immagini, più
// i riquadri delle immagini) e sul limite d'uscita della richiesta.
type Estimate struct {
	USD          float64 // arrotondato a 6 decimali, per piano e uscita
	Exact        float64 // non arrotondato: il valore confrontato col tetto
	InputTokens  int64
	OutputTokens int64
	Source       string
}

// EstimateCost stima il costo di r (già passata da Check) alla data at; nil
// col motivo se il modello non ha tariffa: con un tetto il giudizio è non
// stimabile e non parte.
func EstimateCost(r Request, at time.Time) (*Estimate, string) {
	p, ok := priceAt(r.Model, at)
	if !ok {
		return nil, fmt.Sprintf("nessuna tariffa del giudizio per gemini %s nel listino di giv", r.Model)
	}
	text, err := json.Marshal(Body(r, false))
	if err != nil {
		return nil, "richiesta del giudizio non valida"
	}
	in := int64(len(text)) + promptMargin
	var img int64
	for _, im := range r.images() {
		img += ImageTokens(im.Width, im.Height)
	}
	in += img
	exact := tokenCost(p, in, MaxOutputTokens)
	if math.IsNaN(exact) || math.IsInf(exact, 0) || exact < 0 {
		return nil, "tariffa del giudizio non valida"
	}
	src := fmt.Sprintf("stima per eccesso dal listino di giv (%s), gemini %s%s: al massimo %d token d'ingresso (%d di testo, %d di %d immagini) × %g $/M + al massimo %d token d'uscita (maxOutputTokens della richiesta) × %g $/M",
		p.Source, r.Model, priceDate(p), in, in-img, img, len(r.images()), p.USDPerMInputTokens, MaxOutputTokens, p.USDPerMOutputTokens)
	return &Estimate{USD: math.Round(exact*1e6) / 1e6, Exact: exact, InputTokens: in, OutputTokens: MaxOutputTokens, Source: src}, ""
}

// Usage sono i token riportati da Gemini (usageMetadata).
type Usage struct {
	Prompt     int64
	Candidates int64
	Thoughts   int64
}

// UsageCost è il costo di un giudizio fatto dai token riportati, alla data at
// della richiesta: ok false senza tariffa o senza consumi validi.
func UsageCost(model string, u *Usage, at time.Time) (usd float64, source string, ok bool) {
	p, found := priceAt(model, at)
	if !found || u == nil || u.Prompt <= 0 || u.Candidates < 0 || u.Thoughts < 0 {
		return 0, "", false
	}
	usd = tokenCost(p, u.Prompt, u.Candidates+u.Thoughts)
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return 0, "", false
	}
	return usd, fmt.Sprintf("token riportati da gemini: %d d'ingresso × %g $/M + %d d'uscita e ragionamento × %g $/M%s",
		u.Prompt, p.USDPerMInputTokens, u.Candidates+u.Thoughts, p.USDPerMOutputTokens, priceDate(p)), true
}

func tokenCost(p Price, in, out int64) float64 {
	return float64(in)*p.USDPerMInputTokens/1e6 + float64(out)*p.USDPerMOutputTokens/1e6
}

func priceDate(p Price) string {
	if p.From.IsZero() {
		return ""
	}
	return ", tariffa dal " + p.From.Format("02/01/2006")
}

// priceAt è la tariffa del modello in vigore alla data at.
func priceAt(model string, at time.Time) (Price, bool) {
	var best Price
	found := false
	for _, p := range Prices {
		if p.Model != model || p.From.After(at) {
			continue
		}
		if !found || p.From.After(best.From) {
			best, found = p, true
		}
	}
	return best, found
}

// Misure delle immagini nella stima preventiva, prima che il video esista:
// lato lungo JudgeMaxSide e proporzioni 21:9, le più strette fra quelle dei
// video (più riquadri); la stima alla richiesta usa le misure vere.
const (
	planLong  = 768
	planShort = 328
)

// PlanEstimate è la stima preventiva di un giudizio con frames fotogrammi,
// la richiesta prompt e un riferimento se ref: per il piano di giv story
// prima che la ripresa esista. nil col motivo se il modello non ha tariffa.
func PlanEstimate(model, prompt string, frames int, ref bool, at time.Time) (*Estimate, string) {
	im := Image{Width: planLong, Height: planShort, Data: []byte{0}}
	r := Request{Model: model, Prompt: prompt}
	for i := 0; i < frames; i++ {
		f := im
		f.Role, f.TimeS = RoleFrame, float64(i)
		r.Frames = append(r.Frames, f)
	}
	if ref {
		rf := im
		rf.Role = RoleRef
		r.Ref = &rf
	}
	return EstimateCost(r, at)
}
