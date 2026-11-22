package speech

import (
	"fmt"
	"math"
	"time"
	"unicode/utf8"
)

// Price è una tariffa della voce, valida da From (zero = da sempre) fino alla
// tariffa successiva dello stesso provider e modello.
type Price struct {
	Provider     string
	Model        string
	From         time.Time
	USDPerMChars float64 // dollari per milione di caratteri del testo inviato
}

// Prices è il listino della voce (pagine prezzi dei provider, 22/11/2026).
// xAI: 15 $ per milione di caratteri, contati sul testo inviato (le
// sostituzioni del dizionario non cambiano il testo fatturato).
var Prices = []Price{
	{Provider: XAI, USDPerMChars: 15},
}

// Estimate è la stima del costo di una richiesta.
type Estimate struct {
	USD    float64 // arrotondato a 6 decimali, per piano e manifest
	Exact  float64 // non arrotondato: è il valore confrontato col tetto
	Chars  int     // caratteri contati
	Source string
}

// EstimateCost stima il costo di req (già passata da Check) alla data at.
// nil con il motivo se manca la tariffa: con un tetto di spesa la richiesta è
// non stimabile e non parte.
func EstimateCost(req Request, at time.Time) (*Estimate, string) {
	p, ok := priceAt(req.Provider, req.Model, at)
	if !ok {
		return nil, fmt.Sprintf("nessuna tariffa della voce per %s nel listino di giv", req.Provider)
	}
	chars := utf8.RuneCountInString(req.Text)
	exact := float64(chars) * p.USDPerMChars / 1e6
	if math.IsNaN(exact) || math.IsInf(exact, 0) || exact < 0 {
		return nil, "tariffa della voce non valida"
	}
	return &Estimate{
		USD: math.Round(exact*1e6) / 1e6, Exact: exact, Chars: chars,
		Source: fmt.Sprintf("listino %s: %g $ per milione di caratteri × %d caratteri", req.Provider, p.USDPerMChars, chars),
	}, ""
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
