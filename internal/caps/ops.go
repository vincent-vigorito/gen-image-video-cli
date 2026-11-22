package caps

import (
	"fmt"
	"math"
	"slices"
	"strings"

	"gen-image-video-cli/internal/provider"
)

// Op sono le capacità di un'operazione su una clip esistente (giv extend,
// edit, upscale, avatar) con un modello: flag ammessi, limiti e prezzo. Il
// primo Op di un'operazione nella tabella del provider è il modello di default.
// I limiti a zero non pongono vincoli.
type Op struct {
	Op    string `json:"op"` // extend, edit, upscale, avatar
	Model string `json:"model"`
	// flag del comando ammessi oltre a quelli comuni, senza --; "prompt" è il
	// prompt (argomento). Un flag indicato e non elencato è un rifiuto.
	Flags    []string `json:"flags"`
	Required []string `json:"required,omitempty"` // fra Flags, quelli obbligatori

	Durations       []int    `json:"durations,omitempty"` // --duration ammesse (extend: secondi aggiunti)
	DefaultDuration int      `json:"default_duration,omitempty"`
	SourceMin       float64  `json:"source_min_s,omitempty"` // durata del sorgente, secondi
	SourceMax       float64  `json:"source_max_s,omitempty"`
	MaxHeight       int      `json:"max_height,omitempty"` // altezza massima d'uscita: un sorgente più alto è un avviso
	FactorMin       float64  `json:"factor_min,omitempty"` // --factor
	FactorMax       float64  `json:"factor_max,omitempty"`
	DefaultFactor   float64  `json:"default_factor,omitempty"`
	Modes           []string `json:"modes,omitempty"` // --mode, il primo è il default

	Pricing map[string]float64 `json:"pricing,omitempty"` // listino mostrato; il calcolo è in Price
	// Price stima il costo di req (durate di default già applicate da chi la
	// scrive): dollari non arrotondati e dettaglio della formula, oppure il
	// motivo per cui non si può stimare (why non vuoto).
	Price  func(req provider.ClipRequest) (usd float64, detail, why string) `json:"-"`
	Source string                                                           `json:"source"`
}

// FindOp cerca l'operazione op col modello model; con model vuoto il primo
// modello dell'operazione (il default). nil se non c'è.
func FindOp(ops []Op, op, model string) *Op {
	for i := range ops {
		if ops[i].Op == op && (model == "" || ops[i].Model == model) {
			return &ops[i]
		}
	}
	return nil
}

// OpModels sono i modelli che fanno l'operazione op, nell'ordine della tabella.
func OpModels(ops []Op, op string) []string {
	var ids []string
	for _, o := range ops {
		if o.Op == op {
			ids = append(ids, o.Model)
		}
	}
	return ids
}

// Duration è la durata usata per la richiesta: quella indicata o il default.
func (o *Op) Duration(req provider.ClipRequest) int {
	if req.Duration > 0 {
		return req.Duration
	}
	return o.DefaultDuration
}

// CheckOpFlags controlla req sull'operazione o del provider prov prima di
// leggere i file (o nil = modello che non fa l'operazione): modello, flag
// ammessi e obbligatori, valori di --duration, --factor e --mode. set sono i
// flag dei comandi sulle clip indicati dall'utente, senza --, con "prompt" se
// c'è. Un rifiuto è un *Rejection (codice 2).
func CheckOpFlags(prov string, o *Op, ops []Op, req provider.ClipRequest, set []string) error {
	if o == nil {
		models := OpModels(ops, req.Op)
		if len(models) == 0 {
			return reject("giv %s non disponibile con %s", req.Op, prov)
		}
		return reject("--model %s: giv %s con %s ammette %s", req.Model, req.Op, prov, strings.Join(models, ", "))
	}
	who := o.who(prov)
	for _, f := range set {
		if slices.Contains(o.Flags, f) {
			continue
		}
		if f == "prompt" {
			return reject("%s non accetta un prompt", who)
		}
		return reject("--%s non ammesso da %s", f, who)
	}
	for _, f := range o.Required {
		if slices.Contains(set, f) {
			continue
		}
		if f == "prompt" {
			return reject("%s: manca il prompt", who)
		}
		return reject("%s: serve --%s", who, f)
	}
	if req.Duration != 0 && len(o.Durations) > 0 && !slices.Contains(o.Durations, req.Duration) {
		return reject("--duration %d non ammessa da %s (ammesse: %s)", req.Duration, who, intsText(o.Durations))
	}
	if req.Factor != 0 && (o.FactorMin > 0 && req.Factor < o.FactorMin || o.FactorMax > 0 && req.Factor > o.FactorMax) {
		return reject("--factor %g fuori dall'intervallo di %s (da %g a %g)", req.Factor, who, o.FactorMin, o.FactorMax)
	}
	if req.Mode != "" && len(o.Modes) > 0 && !slices.Contains(o.Modes, req.Mode) {
		return reject("--mode %s non ammesso da %s (ammessi: %s)", req.Mode, who, strings.Join(o.Modes, ", "))
	}
	return nil
}

// CheckOpSource controlla le misure del sorgente lette con ffprobe
// (req.Source) e restituisce gli avvisi: durata fuori dai limiti = rifiuto,
// durata non nota = avviso; un sorgente più alto dell'uscita è un avviso (con
// strict un rifiuto).
func CheckOpSource(prov string, o *Op, req provider.ClipRequest, strict bool) ([]string, error) {
	var warns []string
	src, who := req.Source, o.who(prov)
	if req.Video != "" && (o.SourceMin > 0 || o.SourceMax > 0) {
		switch {
		case src.Duration <= 0:
			warns = append(warns, fmt.Sprintf("durata del sorgente non nota: limiti di %s non controllati", who))
		case o.SourceMin > 0 && src.Duration < o.SourceMin, o.SourceMax > 0 && src.Duration > o.SourceMax:
			return nil, reject("sorgente di %.2f s: %s accetta %s", src.Duration, who, rangeText(o.SourceMin, o.SourceMax))
		}
	}
	if o.MaxHeight > 0 && src.Height > o.MaxHeight {
		msg := fmt.Sprintf("sorgente alto %d px: l'uscita di %s arriva al massimo a %dp", src.Height, who, o.MaxHeight)
		if strict {
			return nil, &Rejection{Msg: msg + " (--strict)"}
		}
		warns = append(warns, msg)
	}
	return warns, nil
}

func (o *Op) who(prov string) string { return fmt.Sprintf("giv %s con %s/%s", o.Op, prov, o.Model) }

// Estimate stima il costo di req col listino dell'operazione; se non si può,
// nil e il motivo.
func (o *Op) Estimate(req provider.ClipRequest) (*Estimate, string) {
	if o.Price == nil {
		return nil, fmt.Sprintf("listino di giv %s con %s non noto", o.Op, o.Model)
	}
	usd, detail, why := o.Price(req)
	if why != "" {
		return nil, why
	}
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return nil, "stima non valida per " + o.Model
	}
	src := o.Source
	if detail != "" {
		src += ": " + detail
	}
	return &Estimate{USD: math.Round(usd*1e4) / 1e4, Exact: usd, Source: src}, ""
}

// intsText scrive una lista di interi come intervallo (2-10) se consecutivi.
func intsText(v []int) string {
	for i := 1; i < len(v); i++ {
		if v[i] != v[i-1]+1 {
			return joinInts(v)
		}
	}
	if len(v) > 2 {
		return fmt.Sprintf("%d-%d", v[0], v[len(v)-1])
	}
	return joinInts(v)
}

func rangeText(lo, hi float64) string {
	switch {
	case lo > 0 && hi > 0:
		return fmt.Sprintf("da %g a %g s", lo, hi)
	case hi > 0:
		return fmt.Sprintf("al massimo %g s", hi)
	}
	return fmt.Sprintf("almeno %g s", lo)
}
