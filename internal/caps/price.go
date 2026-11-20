package caps

import (
	"fmt"
	"math"
	"strings"

	"gen-image-video-cli/internal/provider"
)

// Estimate è il costo stimato da listino di una generazione.
type Estimate struct {
	USD    float64 // arrotondato a 4 decimali, per il piano
	Exact  float64 // non arrotondato: è il valore confrontato col tetto
	Source string
}

// tariffa al secondo letta dal nome di una sku del listino
type rate struct {
	usd   float64 // $/s
	audio string  // "with", "without" o "" (qualsiasi)
	res   string  // risoluzione in minuscolo o "" (base)
	mode  string  // "t2v", "i2v" o "" (qualsiasi)
	refs  bool    // tariffa dei video con riferimenti (reference_)
}

// basi delle sku al secondo (dopo l'eventuale cents_per_, long_ e reference_):
// le altre (video_continuation, megapixel, token) restano fuori
var perSecondBases = []string{"duration_seconds", "video_output_second", "second_output"}

func parseRate(name string, v float64) (rate, bool) {
	n := name
	r := rate{usd: v}
	if rest, ok := strings.CutPrefix(n, "cents_per_"); ok {
		n, r.usd = rest, v/100
	}
	// long_: soglia di durata non nota, la tariffa può valere sempre
	n = strings.TrimPrefix(n, "long_")
	if rest, ok := strings.CutPrefix(n, "reference_"); ok {
		n, r.refs = rest, true
	}
	if rest, ok := strings.CutPrefix(n, "text_to_video_"); ok {
		n, r.mode = rest, "t2v"
	} else if rest, ok := strings.CutPrefix(n, "image_to_video_"); ok {
		n, r.mode = rest, "i2v"
	}
	var rest string
	found := false
	for _, b := range perSecondBases {
		if n == b || strings.HasPrefix(n, b+"_") {
			rest, found = strings.TrimPrefix(strings.TrimPrefix(n, b), "_"), true
			break
		}
	}
	if !found {
		return rate{}, false
	}
	if x, ok := strings.CutPrefix(rest, "with_audio"); ok {
		rest, r.audio = x, "with"
	} else if x, ok := strings.CutPrefix(rest, "without_audio"); ok {
		rest, r.audio = x, "without"
	}
	rest = strings.TrimPrefix(rest, "_")
	if rest != "" {
		if !isResolution(rest) {
			return rate{}, false
		}
		r.res = rest
	}
	return r, true
}

// isResolution riconosce 480p, 1080p, 2k, 4k...
func isResolution(s string) bool {
	if len(s) < 2 || (s[len(s)-1] != 'p' && s[len(s)-1] != 'k') {
		return false
	}
	for _, c := range s[:len(s)-1] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Estimate stima il costo di req dal listino del modello; se non si può,
// restituisce nil e il motivo.
func (m *Model) Estimate(req provider.VideoRequest) (*Estimate, string) {
	dur := req.Duration
	if dur <= 0 {
		dur = m.DefaultDuration
	}
	if dur <= 0 {
		return nil, "durata non indicata: serve --duration per stimare il costo di " + m.ID
	}
	// dimensioni richieste: "" = default del modello non noto, vale qualsiasi
	// tariffa compatibile e si prende la più alta
	wantAudio := ""
	switch {
	case req.Audio != nil && *req.Audio:
		wantAudio = "with"
	case req.Audio != nil, m.Audio != nil && !*m.Audio:
		wantAudio = "without"
	}
	wantRes := strings.ToLower(req.Resolution)
	wantMode := "t2v"
	if req.Image != "" {
		wantMode = "i2v"
	}

	var best rate
	bestSKU, bestScore := "", -1
	var perImage, perRef, minimum float64
	tokens := false
	for _, sku := range sortedKeys(m.Pricing) {
		v := m.Pricing[sku]
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return nil, "listino di " + m.ID + " con prezzi non validi: stima non disponibile"
		}
		switch sku {
		case "cents_per_image_input":
			perImage = v / 100
			continue
		case "reference_images", "reference_input_images":
			perRef = v
			continue
		case "minimum_cents_per_generation":
			minimum = v / 100
			continue
		}
		r, ok := parseRate(sku, v)
		if !ok {
			tokens = tokens || strings.Contains(sku, "tokens")
			continue
		}
		if r.refs && len(req.Refs) == 0 {
			continue
		}
		// le tariffe di un'altra modalità, risoluzione o audio non valgono
		// per questa richiesta: escluse, mai usate come ripiego
		audio, okA := match(r.audio, wantAudio)
		res, okR := match(r.res, wantRes)
		mode, okM := match(r.mode, wantMode)
		if !okA || !okR || !okM {
			continue
		}
		score := audio*9 + res*3 + mode
		// a parità di punteggio la tariffa più alta: meglio stimare per eccesso
		if score > bestScore || score == bestScore && r.usd > best.usd {
			best, bestSKU, bestScore = r, sku, score
		}
	}
	if bestScore < 0 {
		if tokens {
			return nil, "listino a token per " + m.ID + ": stima non disponibile"
		}
		return nil, "nessuna tariffa al secondo del listino di " + m.ID + " vale per questa richiesta"
	}
	images := len(req.Refs)
	if req.Image != "" {
		images++
	}
	if req.LastFrame != "" {
		images++
	}
	total := best.usd*float64(dur) + perImage*float64(images) + perRef*float64(len(req.Refs))
	src := fmt.Sprintf("%s: %s × %d s", m.Source, bestSKU, dur)
	if perImage > 0 && images > 0 {
		src += fmt.Sprintf(" + %d immagini in ingresso", images)
	}
	if perRef > 0 && len(req.Refs) > 0 {
		src += fmt.Sprintf(" + %d riferimenti", len(req.Refs))
	}
	if minimum > total {
		total = minimum
		src += " (minimo a generazione)"
	}
	if math.IsNaN(total) || math.IsInf(total, 0) {
		return nil, "stima non valida per " + m.ID
	}
	return &Estimate{USD: math.Round(total*1e4) / 1e4, Exact: total, Source: src}, ""
}

// match confronta una dimensione della tariffa con quella richiesta: 2 uguale,
// 1 tariffa generica, 0 richiesta senza valore noto (qualsiasi tariffa); ok
// false se la tariffa è di un altro valore.
func match(rate, want string) (score int, ok bool) {
	switch {
	case want == "":
		return 0, true
	case rate == want:
		return 2, true
	case rate == "":
		return 1, true
	}
	return 0, false
}

// PerSecond è l'intervallo delle tariffe al secondo del listino; ok false se
// il listino non ne ha.
func (m *Model) PerSecond() (lo, hi float64, ok bool) {
	for sku, v := range m.Pricing {
		r, isRate := parseRate(sku, v)
		if !isRate || math.IsNaN(r.usd) || math.IsInf(r.usd, 0) {
			continue
		}
		if !ok || r.usd < lo {
			lo = r.usd
		}
		if !ok || r.usd > hi {
			hi = r.usd
		}
		ok = true
	}
	return lo, hi, ok
}
