package speech

import (
	"fmt"
	"math"
	"time"
	"unicode/utf8"
)

// Price è una tariffa della voce, valida da From (zero = da sempre) fino alla
// tariffa successiva dello stesso provider e modello. A caratteri (xai) o a
// token (gemini).
type Price struct {
	Provider           string
	Model              string
	From               time.Time
	USDPerMChars       float64 // dollari per milione di caratteri del testo inviato
	USDPerMInputTokens float64 // dollari per milione di token di testo in ingresso
	USDPerMAudioTokens float64 // dollari per milione di token audio in uscita
}

// geminiDouble è il raddoppio annunciato dei prezzi della voce di gemini
// (01/01/2027, preso a mezzanotte UTC: prima dell'ora del Pacifico, quindi
// per eccesso).
var geminiDouble = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

// Prices è il listino della voce (pagine prezzi dei provider, 22/11/2026).
// xAI: 15 $ per milione di caratteri, contati sul testo inviato (le
// sostituzioni del dizionario non cambiano il testo fatturato). Gemini: testo
// in ingresso 0,50 $ e audio in uscita 9 $ (flash) o 6 $ (flash-lite) per
// milione di token, il doppio dal 01/01/2027.
var Prices = []Price{
	{Provider: XAI, USDPerMChars: 15},
	{Provider: Gemini, Model: "gemini-3.8-flash-tts", USDPerMInputTokens: 0.5, USDPerMAudioTokens: 9},
	{Provider: Gemini, Model: "gemini-3.8-flash-tts", From: geminiDouble, USDPerMInputTokens: 1, USDPerMAudioTokens: 18},
	{Provider: Gemini, Model: "gemini-3.8-flash-lite-tts", USDPerMInputTokens: 0.5, USDPerMAudioTokens: 6},
	{Provider: Gemini, Model: "gemini-3.8-flash-lite-tts", From: geminiDouble, USDPerMInputTokens: 1, USDPerMAudioTokens: 12},
}

// Estimate è la stima del costo di una richiesta.
type Estimate struct {
	USD      float64 // arrotondato a 6 decimali, per piano e manifest
	Exact    float64 // non arrotondato: è il valore confrontato col tetto
	Probable bool    // stima probabile, non un massimo garantito (gemini): con un tetto vale CapRefusal
	Chars    int     // caratteri contati
	Source   string
}

// EstimateCost stima il costo di req (già passata da Check) alla data at:
// esatta per xai (caratteri × prezzo); per gemini probabile (Probable), sul
// limite dei token d'uscita che va nella richiesta (MaxGeminiOutputTokens,
// ragionamento compreso, alla tariffa audio) più il massimo dei token in
// ingresso (MaxGeminiInputTokens). nil con il motivo se manca la tariffa:
// con un tetto di spesa la richiesta è non stimabile e non parte.
func EstimateCost(req Request, at time.Time) (*Estimate, string) {
	p, ok := priceAt(req.Provider, req.Model, at)
	if !ok {
		return nil, fmt.Sprintf("nessuna tariffa della voce per %s nel listino di giv", providerModel(req))
	}
	chars := utf8.RuneCountInString(req.Spoken())
	var exact float64
	var src string
	probable := CapRefusal(req.Provider) != ""
	if p.USDPerMChars > 0 {
		exact = float64(chars) * p.USDPerMChars / 1e6
		src = fmt.Sprintf("listino %s: %g $ per milione di caratteri × %d caratteri", req.Provider, p.USDPerMChars, chars)
	} else {
		exact = tokenCost(p, MaxGeminiInputTokens, MaxGeminiOutputTokens)
		src = fmt.Sprintf("stima probabile, non un massimo garantito: listino %s%s, %d token di testo × %g $/M + %d token d'uscita (max_output_tokens della richiesta) × %g $/M",
			providerModel(req), priceDate(p), MaxGeminiInputTokens, p.USDPerMInputTokens, MaxGeminiOutputTokens, p.USDPerMAudioTokens)
	}
	if math.IsNaN(exact) || math.IsInf(exact, 0) || exact < 0 {
		return nil, "tariffa della voce non valida"
	}
	return &Estimate{USD: math.Round(exact*1e6) / 1e6, Exact: exact, Probable: probable, Chars: chars, Source: src}, ""
}

// CapRefusal è il motivo per cui una voce del provider non si può chiedere
// con un tetto di spesa attivo (--max-cost o tetto dello storyboard): "" per
// xai, la cui stima è esatta. Il costo di una voce gemini non ha oggi un
// massimo garantito (decisione 22): con un tetto è non stimabile, codice 2
// prima di qualsiasi richiesta. Provider "" = DefaultProvider; un provider
// sconosciuto non è mai stimabile.
func CapRefusal(provider string) string {
	if provider == "" {
		provider = DefaultProvider
	}
	switch provider {
	case XAI:
		return ""
	case Gemini:
		return "la voce gemini non ha un costo massimo garantito: con un tetto di spesa usa xai o togli il tetto"
	}
	return fmt.Sprintf("voce %s senza una stima garantita: con un tetto di spesa usa xai o togli il tetto", provider)
}

// UsageCost è il costo di una voce generata dai consumi, alla data at della
// richiesta, per i provider a token (gemini): dai token riportati
// (res.Usage; quelli di ragionamento alla tariffa audio, per eccesso),
// altrimenti dalla durata misurata sul WAV (GeminiTokensPerSecond) più un
// token per byte di testo e stile. ok false per i provider a caratteri, senza
// tariffa o senza consumi né durata.
func UsageCost(req Request, res *Result, at time.Time) (usd float64, source string, ok bool) {
	p, found := priceAt(req.Provider, req.Model, at)
	if !found || p.USDPerMChars > 0 || res == nil {
		return 0, "", false
	}
	if u := res.Usage; u != nil && u.Input >= 0 && u.Output > 0 && u.Thought >= 0 {
		usd = tokenCost(p, u.Input, u.Output+u.Thought)
		source = fmt.Sprintf("token riportati da %s: %d di testo × %g $/M + %d d'uscita × %g $/M%s",
			req.Provider, u.Input, p.USDPerMInputTokens, u.Output+u.Thought, p.USDPerMAudioTokens, priceDate(p))
	} else if res.DurationS > 0 && !math.IsInf(res.DurationS, 0) {
		audio := int64(math.Ceil(res.DurationS * GeminiTokensPerSecond))
		input := int64(len(req.Spoken()) + len(req.Style))
		usd = tokenCost(p, input, audio)
		source = fmt.Sprintf("dalla durata misurata (%s non ha riportato i token): %d token di testo × %g $/M + %d token audio (%.3f s × %d) × %g $/M%s",
			req.Provider, input, p.USDPerMInputTokens, audio, res.DurationS, GeminiTokensPerSecond, p.USDPerMAudioTokens, priceDate(p))
	} else {
		return 0, "", false
	}
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return 0, "", false
	}
	return usd, source, true
}

// CharsCost è il costo di una voce generata per i provider a caratteri (xai),
// calcolato da giv alla data at della richiesta: caratteri del testo inviato
// (req.Spoken) × prezzo del listino, come la stima (il provider non riporta
// il costo). ok false per i provider a token o senza tariffa.
func CharsCost(req Request, at time.Time) (usd float64, source string, ok bool) {
	p, found := priceAt(req.Provider, req.Model, at)
	if !found || p.USDPerMChars <= 0 {
		return 0, "", false
	}
	chars := utf8.RuneCountInString(req.Spoken())
	usd = float64(chars) * p.USDPerMChars / 1e6
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return 0, "", false
	}
	return usd, fmt.Sprintf("calcolato da giv: listino %s %g $ per milione di caratteri × %d caratteri del testo inviato%s",
		req.Provider, p.USDPerMChars, chars, priceDate(p)), true
}

func tokenCost(p Price, input, audio int64) float64 {
	return float64(input)*p.USDPerMInputTokens/1e6 + float64(audio)*p.USDPerMAudioTokens/1e6
}

func providerModel(req Request) string {
	if req.Model == "" {
		return req.Provider
	}
	return req.Provider + " " + req.Model
}

func priceDate(p Price) string {
	if p.From.IsZero() {
		return ""
	}
	return ", tariffa dal " + p.From.Format("02/01/2006")
}

// priceAt è la tariffa di provider e modello in vigore alla data at: fra
// quelle con From non successivo ad at, la più recente.
func priceAt(provider, model string, at time.Time) (Price, bool) {
	var best Price
	found := false
	for _, p := range Prices {
		if p.Provider != provider || p.Model != model || p.From.After(at) {
			continue
		}
		if !found || p.From.After(best.From) {
			best, found = p, true
		}
	}
	return best, found
}
