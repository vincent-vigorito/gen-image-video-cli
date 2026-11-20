package caps

import (
	"fmt"
	"math"
	"strings"

	"gen-image-video-cli/internal/provider"
)

// Estimate è il costo stimato da listino di una generazione.
type Estimate struct {
	USD    float64
	Source string
}

// tariffa al secondo letta dal nome di una sku del listino
type rate struct {
	usd   float64 // $/s
	audio string  // "with", "without" o "" (qualsiasi)
	res   string  // risoluzione in minuscolo o "" (base)
	mode  string  // "t2v", "i2v" o "" (qualsiasi)
}

// basi delle sku al secondo (dopo l'eventuale cents_per_): le altre
// (long_, reference_duration_, video_continuation, megapixel, token) restano fuori
var perSecondBases = []string{"duration_seconds", "video_output_second", "second_output"}

func parseRate(name string, v float64) (rate, bool) {
	n := name
	r := rate{usd: v}
	if rest, ok := strings.CutPrefix(n, "cents_per_"); ok {
		n, r.usd = rest, v/100
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
	wantAudio := ""
	switch {
	case req.Audio != nil && *req.Audio, req.Audio == nil && m.Audio != nil && *m.Audio:
		wantAudio = "with"
	case req.Audio != nil, m.Audio != nil:
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
		score := 0
		switch r.audio {
		case wantAudio:
			score += 8
		case "":
			score += 4
		}
		switch r.res {
		case "":
			score += 2
		case wantRes:
			score += 4
		}
		switch r.mode {
		case "":
			score++
		case wantMode:
			score += 2
		}
		// a parità di punteggio la tariffa più alta: meglio stimare per eccesso
		if score > bestScore || score == bestScore && r.usd > best.usd {
			best, bestSKU, bestScore = r, sku, score
		}
	}
	if bestScore < 0 {
		if tokens {
			return nil, "listino a token per " + m.ID + ": stima non disponibile"
		}
		return nil, "nessuna tariffa al secondo nel listino di " + m.ID
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
	return &Estimate{USD: math.Round(total*1e4) / 1e4, Source: src}, ""
}

// PerSecond è l'intervallo delle tariffe al secondo del listino; ok false se
// il listino non ne ha.
func (m *Model) PerSecond() (lo, hi float64, ok bool) {
	for sku, v := range m.Pricing {
		r, isRate := parseRate(sku, v)
		if !isRate {
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
