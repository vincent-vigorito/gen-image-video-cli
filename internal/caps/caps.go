// Package caps descrive capacità e prezzi dei modelli video e controlla una
// richiesta prima di spendere: un controllo strutturale non supportato è un
// rifiuto, uno di rifinitura un avviso (con strict anche questo è un rifiuto).
package caps

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gen-image-video-cli/internal/provider"
)

// Model sono le capacità di un modello video; i campi vuoti non sono dichiarati.
type Model struct {
	ID              string             `json:"id"`
	Durations       []int              `json:"durations,omitempty"`
	DefaultDuration int                `json:"default_duration,omitempty"`
	Resolutions     []string           `json:"resolutions,omitempty"`
	Aspects         []string           `json:"aspect_ratios,omitempty"`
	Frames          []string           `json:"frame_images,omitempty"` // first_frame, last_frame
	Audio           *bool              `json:"audio,omitempty"`        // audio nativo
	Seed            *bool              `json:"seed,omitempty"`
	MaxRefs         *int               `json:"max_refs,omitempty"`        // 0 = riferimenti non supportati
	RefResolutions  []string           `json:"ref_resolutions,omitempty"` // risoluzioni ammesse con --ref
	RefMaxDuration  int                `json:"ref_max_duration,omitempty"`
	ImageWithRefs   *bool              `json:"image_with_refs,omitempty"` // false = --image e --ref non insieme
	ImageWithRefsOn string             `json:"-"`                         // provider che li accetta insieme
	Passthrough     []string           `json:"passthrough,omitempty"`
	Pricing         map[string]float64 `json:"pricing,omitempty"` // sku -> valore, unità nel nome
	Source          string             `json:"source"`
}

// Find cerca il modello per id; nil se non c'è.
func Find(catalog []Model, id string) *Model {
	for i := range catalog {
		if catalog[i].ID == id {
			return &catalog[i]
		}
	}
	return nil
}

// ParseOpenRouter legge la risposta di GET /videos/models di OpenRouter.
func ParseOpenRouter(data []byte) ([]Model, error) {
	var resp struct {
		Data []struct {
			ID          string         `json:"id"`
			Durations   []int          `json:"supported_durations"`
			Resolutions []string       `json:"supported_resolutions"`
			Aspects     []string       `json:"supported_aspect_ratios"`
			Frames      []string       `json:"supported_frame_images"`
			Audio       *bool          `json:"generate_audio"`
			Seed        *bool          `json:"seed"`
			Passthrough []string       `json:"allowed_passthrough_parameters"`
			Pricing     map[string]any `json:"pricing_skus"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("catalogo video openrouter: %w", err)
	}
	out := make([]Model, 0, len(resp.Data))
	for _, d := range resp.Data {
		m := Model{
			ID: d.ID, Durations: d.Durations, Resolutions: d.Resolutions, Aspects: d.Aspects,
			Frames: d.Frames, Audio: d.Audio, Seed: d.Seed, Passthrough: d.Passthrough,
			Source: "openrouter /videos/models",
		}
		// via OpenRouter i modelli x-ai rispondono 400 a immagine iniziale e
		// riferimenti insieme (prove di CLI-869); xai diretto li accetta
		if strings.HasPrefix(d.ID, "x-ai/") {
			m.ImageWithRefs, m.ImageWithRefsOn = ptr(false), "xai"
		}
		for k, v := range d.Pricing {
			var f float64
			var err error
			switch x := v.(type) {
			case string:
				f, err = strconv.ParseFloat(x, 64)
			case float64:
				f = x
			default:
				continue
			}
			if err != nil {
				continue
			}
			if m.Pricing == nil {
				m.Pricing = map[string]float64{}
			}
			m.Pricing[k] = f
		}
		out = append(out, m)
	}
	return out, nil
}

func ptr[T any](v T) *T { return &v }

// xaiSource è la fonte della tabella xAI: va aggiornata a mano a ogni release.
const xaiSource = "tabella giv 20/11/2026: docs.x.ai/developers/model-capabilities/video, docs.x.ai/developers/pricing"

// XAI è la tabella fissa dei modelli video di xAI (diretto).
func XAI() []Model {
	durations := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	aspects := []string{"1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3", "21:9", "5:2"}
	return []Model{
		{
			ID: "grok-imagine-video-1.5", Durations: durations, DefaultDuration: 8,
			Resolutions: []string{"480p", "720p", "1080p"}, Aspects: aspects,
			Frames: []string{"first_frame", "last_frame"}, Audio: ptr(true), Seed: ptr(false),
			MaxRefs: ptr(14), RefResolutions: []string{"480p", "720p"}, RefMaxDuration: 15,
			Pricing: map[string]float64{"duration_seconds": 0.08}, Source: xaiSource,
		},
		{
			ID: "grok-imagine-video-1.5-lite", Durations: durations, DefaultDuration: 8,
			Resolutions: []string{"480p", "720p", "1080p"}, Aspects: aspects,
			Frames: []string{"first_frame", "last_frame"}, Audio: ptr(true), Seed: ptr(false),
			MaxRefs: ptr(14), RefResolutions: []string{"480p", "720p"}, RefMaxDuration: 15,
			Pricing: map[string]float64{"duration_seconds": 0.02}, Source: xaiSource,
		},
		{
			ID: "grok-imagine-video", Durations: durations, DefaultDuration: 8,
			Resolutions: []string{"480p", "720p"}, Aspects: aspects[:7],
			Frames: []string{"first_frame"}, Seed: ptr(false),
			MaxRefs: ptr(7), RefResolutions: []string{"480p", "720p"}, RefMaxDuration: 10,
			Pricing: map[string]float64{"duration_seconds": 0.05}, Source: xaiSource,
		},
	}
}

// Rejection è un controllo non superato: il CLI esce con codice 2.
type Rejection struct{ Msg string }

func (r *Rejection) Error() string { return r.Msg }

func reject(format string, a ...any) error { return &Rejection{Msg: fmt.Sprintf(format, a...)} }

// Check controlla req sul modello m del provider prov (m nil = modello non in
// tabella) e restituisce gli avvisi. Sistema req per l'invio: --seed e
// --negative non supportati vengono tolti, --negative va fra i parametri
// passanti dove il modello lo prevede.
func Check(prov string, m *Model, catalog []Model, req *provider.VideoRequest, strict bool) ([]string, error) {
	var warns []string
	refine := func(format string, a ...any) error {
		msg := fmt.Sprintf(format, a...)
		if strict {
			return &Rejection{Msg: strings.TrimSuffix(msg, ", ignorato") + " (--strict)"}
		}
		warns = append(warns, msg)
		return nil
	}
	if m == nil {
		if err := refine("modello %s non nella tabella delle capacità di %s: controlli e stima non fatti", req.Model, prov); err != nil {
			return nil, err
		}
		return warns, nil
	}
	supporting := func(ok func(*Model) bool) string {
		var ids []string
		for i := range catalog {
			if ok(&catalog[i]) {
				ids = append(ids, catalog[i].ID)
			}
		}
		if len(ids) == 0 {
			return "nessun modello di " + prov
		}
		return "modelli di " + prov + " che lo supportano: " + strings.Join(ids, ", ")
	}

	// strutturali: cambiano cosa viene generato
	if req.Image != "" && !slices.Contains(m.Frames, "first_frame") {
		return nil, reject("--image non supportato da %s; %s", m.ID,
			supporting(func(x *Model) bool { return slices.Contains(x.Frames, "first_frame") }))
	}
	if req.LastFrame != "" && !slices.Contains(m.Frames, "last_frame") {
		return nil, reject("--last-frame non supportato da %s; %s", m.ID,
			supporting(func(x *Model) bool { return slices.Contains(x.Frames, "last_frame") }))
	}
	if req.Image != "" && len(req.Refs) > 0 && m.ImageWithRefs != nil && !*m.ImageWithRefs {
		alt := ""
		if m.ImageWithRefsOn != "" {
			alt = " oppure --provider " + m.ImageWithRefsOn
		}
		return nil, reject("--image e --ref insieme non supportati da %s su %s: usa uno solo dei due%s", m.ID, prov, alt)
	}
	resolutions, maxDuration := m.Resolutions, 0
	if len(req.Refs) > 0 {
		switch {
		case m.MaxRefs == nil:
			if err := refine("--ref non verificabile su %s: riferimenti inviati senza garanzia che il modello li usi", m.ID); err != nil {
				return nil, err
			}
		case *m.MaxRefs == 0:
			return nil, reject("--ref non supportato da %s; %s", m.ID,
				supporting(func(x *Model) bool { return x.MaxRefs != nil && *x.MaxRefs > 0 }))
		case len(req.Refs) > *m.MaxRefs:
			return nil, reject("--ref: %s accetta al massimo %d immagini di riferimento, non %d", m.ID, *m.MaxRefs, len(req.Refs))
		}
		if len(m.RefResolutions) > 0 {
			resolutions = m.RefResolutions
		}
		maxDuration = m.RefMaxDuration
	}
	if req.Duration > 0 {
		if len(m.Durations) > 0 && !slices.Contains(m.Durations, req.Duration) {
			return nil, reject("--duration %d non ammessa da %s (ammesse: %s)", req.Duration, m.ID, joinInts(m.Durations))
		}
		if maxDuration > 0 && req.Duration > maxDuration {
			return nil, reject("--duration %d: con --ref %s arriva al massimo a %d s", req.Duration, m.ID, maxDuration)
		}
	}
	if req.Resolution != "" && len(resolutions) > 0 && !containsFold(resolutions, req.Resolution) {
		with := ""
		if len(req.Refs) > 0 && len(m.RefResolutions) > 0 {
			with = " con --ref"
		}
		return nil, reject("--resolution %s non ammessa da %s%s (ammesse: %s)", req.Resolution, m.ID, with, strings.Join(resolutions, ", "))
	}
	if req.Aspect != "" && len(m.Aspects) > 0 && !slices.Contains(m.Aspects, req.Aspect) {
		return nil, reject("--aspect %s non ammesso da %s (ammessi: %s)", req.Aspect, m.ID, strings.Join(m.Aspects, ", "))
	}
	if req.Audio != nil && *req.Audio {
		switch {
		case m.Audio == nil:
			if err := refine("--audio on non verificabile su %s: il catalogo non dichiara l'audio", m.ID); err != nil {
				return nil, err
			}
		case !*m.Audio:
			return nil, reject("--audio on: %s non genera audio; %s", m.ID,
				supporting(func(x *Model) bool { return x.Audio != nil && *x.Audio }))
		}
	}
	for _, k := range sortedKeys(req.Params) {
		if !slices.Contains(m.Passthrough, k) {
			allowed := "nessuno"
			if len(m.Passthrough) > 0 {
				allowed = strings.Join(m.Passthrough, ", ")
			}
			return nil, reject("--param %s non ammesso da %s (ammessi: %s)", k, m.ID, allowed)
		}
	}

	// rifinitura: avviso, la generazione parte
	if req.Seed != 0 && (m.Seed == nil || !*m.Seed) {
		if err := refine("--seed non supportato da %s, ignorato", seedOwner(prov, m)); err != nil {
			return nil, err
		}
		req.Seed = 0
	}
	if req.Negative != "" {
		key := ""
		for _, k := range []string{"negativePrompt", "negative_prompt"} {
			if slices.Contains(m.Passthrough, k) {
				key = k
				break
			}
		}
		switch {
		case key == "":
			if err := refine("--negative non supportato da %s, ignorato", seedOwner(prov, m)); err != nil {
				return nil, err
			}
		case hasKey(req.Params, key):
			warns = append(warns, fmt.Sprintf("--param %s prevale su --negative", key))
		default:
			p := make(map[string]any, len(req.Params)+1)
			for k, v := range req.Params {
				p[k] = v
			}
			p[key] = req.Negative
			req.Params = p
		}
		req.Negative = ""
	}
	if prov == "xai" && req.Image != "" && req.Aspect != "" && strict {
		// senza --strict l'avviso lo dà l'adapter, che invia comunque aspect_ratio
		return nil, reject("con --image %s ignora --aspect (--strict)", prov)
	}
	return warns, nil
}

// seedOwner è il nome negli avvisi: xai li dava già col nome del provider.
func seedOwner(prov string, m *Model) string {
	if prov == "xai" {
		return prov
	}
	return m.ID
}

func joinInts(v []int) string {
	s := make([]string, len(v))
	for i, n := range v {
		s[i] = strconv.Itoa(n)
	}
	return strings.Join(s, ", ")
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
