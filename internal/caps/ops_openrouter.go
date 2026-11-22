package caps

import (
	"fmt"

	"gen-image-video-cli/internal/provider"
)

// openRouterOpsSource è la fonte della tabella delle operazioni di OpenRouter:
// va aggiornata a mano (il catalogo /videos/models non dice quali modelli
// lavorano su una clip esistente).
const openRouterOpsSource = "tabella giv 22/11/2026: openrouter.ai/black-forest-labs/flux-video-upscale, openrouter.ai/api/v1/videos/models"

// OpenRouterUpscaleModel è l'unico modello di ingrandimento nel catalogo di OpenRouter al 22/11/2026.
const OpenRouterUpscaleModel = "black-forest-labs/flux-video-upscale"

// OpenRouterOps è la tabella fissa delle operazioni sulle clip di OpenRouter.
// Limiti di durata, peso e risoluzione dell'upscale non sono documentati:
// restano quelli comuni dei file in ingresso.
func OpenRouterOps() []Op {
	ops := []Op{{
		Op: "upscale", Model: OpenRouterUpscaleModel,
		Flags: []string{"video", "factor", "mode", "prompt"}, Required: []string{"video"},
		FactorMin: 1.5, FactorMax: 3, DefaultFactor: 2, Modes: []string{"precise", "creative"},
		Pricing: map[string]float64{"megapixel_seconds_precise": 0.035, "megapixel_seconds_creative": 0.05},
		Source:  openRouterOpsSource,
	}}
	up := &ops[0]
	up.Price = func(r provider.ClipRequest) (float64, string, string) { return megapixelSecondsPrice(up, r) }
	return ops
}

// megapixelSecondsPrice stima un ingrandimento col listino a megapixel-secondo,
// in modo prudente sui megapixel d'uscita (lati del sorgente × fattore): la
// formula del provider non è documentata. Servono le misure lette con ffprobe.
func megapixelSecondsPrice(o *Op, r provider.ClipRequest) (float64, string, string) {
	s := r.Source
	if s.Duration <= 0 || s.Width <= 0 || s.Height <= 0 {
		return 0, "", "misure del sorgente non note (URL o ffprobe assente) e listino a megapixel-secondo"
	}
	mode, f := o.Mode(r), o.Factor(r)
	rate, ok := o.Pricing["megapixel_seconds_"+mode]
	if !ok || f <= 0 {
		return 0, "", fmt.Sprintf("nessuna tariffa di %s per --mode %s", o.Model, mode)
	}
	mp := float64(s.Width) * float64(s.Height) * f * f / 1e6
	return mp * s.Duration * rate,
		fmt.Sprintf("%dx%d ×%g = %.2f MP d'uscita × %.2f s × %g $/MP·s (%s; prudente: formula del provider non documentata)",
			s.Width, s.Height, f, mp, s.Duration, rate, mode), ""
}
