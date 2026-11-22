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

// Costo probabile di gemini, che non ha una velocità: un ritmo lento (il
// parlato normale è sui 15 caratteri al secondo) e mai meno di due secondi.
// È solo informativo: la durata non si può imporre (lo stile può
// rallentarla), quindi il tetto vale sul massimo fatturabile.
const (
	slowCharsPerSecond = 10
	minEstimateSeconds = 2
)

// Estimate è la stima del costo di una richiesta.
type Estimate struct {
	USD      float64 // arrotondato a 6 decimali, per piano e manifest
	Exact    float64 // non arrotondato: è il valore confrontato col tetto
	Probable float64 // costo probabile, arrotondato (gemini; xai: = USD)
	Chars    int     // caratteri contati
	Source   string
}

// EstimateCost stima il costo di req (già passata da Check) alla data at:
// esatta per xai (caratteri × prezzo); per gemini il massimo fatturabile di
// una richiesta, dai limiti del modello (MaxGeminiInputTokens in ingresso,
// MaxGeminiOutputTokens in uscita, ragionamento compreso, alla tariffa
// audio), con accanto il costo probabile (token audio di una durata a ritmo
// lento più un token per byte di testo e stile). nil con il motivo se manca
// la tariffa: con un tetto di spesa la richiesta è non stimabile e non parte.
func EstimateCost(req Request, at time.Time) (*Estimate, string) {
	p, ok := priceAt(req.Provider, req.Model, at)
	if !ok {
		return nil, fmt.Sprintf("nessuna tariffa della voce per %s nel listino di giv", providerModel(req))
	}
	spoken := req.Spoken()
	chars := utf8.RuneCountInString(spoken)
	var exact, probable float64
	var src string
	if p.USDPerMChars > 0 {
		exact = float64(chars) * p.USDPerMChars / 1e6
		probable = exact
		src = fmt.Sprintf("listino %s: %g $ per milione di caratteri × %d caratteri", req.Provider, p.USDPerMChars, chars)
	} else {
		exact = tokenCost(p, MaxGeminiInputTokens, MaxGeminiOutputTokens)
		secs := max(float64(chars)/slowCharsPerSecond, minEstimateSeconds)
		audio := min(int64(math.Ceil(secs*GeminiTokensPerSecond)), MaxGeminiOutputTokens)
		input := min(int64(len(spoken)+len(req.Style)), MaxGeminiInputTokens)
		probable = tokenCost(p, input, audio)
		src = fmt.Sprintf("massimo per richiesta, listino %s%s: %d token di testo × %g $/M + %d token d'uscita × %g $/M (limiti del modello); probabile %.6f $ (%d token audio: %.0f s a %d caratteri/s)",
			providerModel(req), priceDate(p), MaxGeminiInputTokens, p.USDPerMInputTokens, MaxGeminiOutputTokens, p.USDPerMAudioTokens,
			math.Round(probable*1e6)/1e6, audio, secs, slowCharsPerSecond)
	}
	if math.IsNaN(exact) || math.IsInf(exact, 0) || exact < 0 || math.IsNaN(probable) || probable < 0 {
		return nil, "tariffa della voce non valida"
	}
	return &Estimate{USD: math.Round(exact*1e6) / 1e6, Exact: exact, Probable: math.Round(probable*1e6) / 1e6, Chars: chars, Source: src}, ""
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
