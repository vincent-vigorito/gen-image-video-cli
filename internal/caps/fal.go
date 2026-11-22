package caps

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"gen-image-video-cli/internal/provider"
)

// falSource è la fonte delle schede fal: pagine dei modelli lette il
// 22/11/2026 (fal.ai/models/<endpoint>/llms.txt e lo schema OpenAPI della
// coda); vanno aggiornate a mano.
func falSource(endpoint string) string {
	return "scheda fal 22/11/2026 (fal.ai/models/" + endpoint + ")"
}

// Fal sono le schede dei modelli video di fal: capacità, regole e prezzi
// fissati in giv, non un catalogo letto in rete. Gli id sono quelli di
// --model; l'endpoint di ogni scheda è nel provider.
func Fal() []Model {
	seconds := func(lo, hi int) []int {
		var d []int
		for i := lo; i <= hi; i++ {
			d = append(d, i)
		}
		return d
	}
	return []Model{
		{
			ID: "kling-v3-pro", Durations: seconds(3, 15), DefaultDuration: 5,
			Frames: []string{"first_frame", "last_frame"}, Audio: ptr(true), Seed: ptr(false),
			MaxRefs: ptr(4), Passthrough: []string{"cfg_scale", "negative_prompt"},
			// la scheda dice 0,112 $/s senza audio, il listino generale 0,14:
			// fonti discordi, vale la più alta finché la prova non lo chiarisce
			Pricing: map[string]float64{"duration_seconds_with_audio": 0.168, "duration_seconds_without_audio": 0.14},
			Source:  falSource("fal-ai/kling-video/v3/pro/image-to-video"),
			check:   checkKlingV3,
		},
		{
			ID: "seedance-2.0-ref", Durations: seconds(4, 15),
			Resolutions: []string{"480p", "720p", "1080p"},
			Aspects:     []string{"auto", "21:9", "16:9", "4:3", "1:1", "3:4", "9:16"},
			Audio:       ptr(true), Seed: ptr(false), MaxRefs: ptr(9),
			Pricing:  map[string]float64{"usd_per_1000_tokens": seedanceRate},
			Source:   falSource("bytedance/seedance-2.0/reference-to-video"),
			check:    checkSeedance,
			estimate: seedanceEstimate,
		},
	}
}

func checkKlingV3(req *provider.VideoRequest) error {
	switch {
	case req.Image == "":
		return reject("kling-v3-pro vuole --image (primo fotogramma)")
	case len(req.Refs) == 1:
		return reject("--ref: kling-v3-pro vuole da 2 a 4 immagini (la prima frontale, le altre di riferimento), non 1")
	case req.Resolution != "":
		return reject("--resolution non supportata da kling-v3-pro: la risoluzione la decide il modello")
	case req.Aspect != "":
		return reject("--aspect non supportato da kling-v3-pro: il formato segue --image")
	case utf8.RuneCountInString(req.Prompt) > 2500:
		return reject("prompt di %d caratteri: kling-v3-pro ne accetta al massimo 2500", utf8.RuneCountInString(req.Prompt))
	}
	return nil
}

func checkSeedance(req *provider.VideoRequest) error {
	switch {
	case len(req.Refs) == 0:
		return reject("seedance-2.0-ref vuole da 1 a 9 --ref")
	case strings.TrimSpace(req.Prompt) == "":
		return reject("seedance-2.0-ref vuole un prompt")
	}
	return nil
}

// seedanceRate è il prezzo di Seedance 2.0 su fal, in dollari per 1000 token
// (da 480p a 1080p).
const seedanceRate = 0.014

// seedanceEstimate stima Seedance 2.0 con la formula della scheda: token =
// altezza × larghezza × secondi × 24 / 1024. Senza --aspect (o con auto) vale
// il formato più largo, senza --resolution 720p (default del modello).
func seedanceEstimate(m *Model, req provider.VideoRequest) (*Estimate, string) {
	dur := req.Duration
	if dur <= 0 {
		return nil, "durata non indicata: serve --duration per stimare il costo di " + m.ID + " (default auto)"
	}
	res := strings.ToLower(req.Resolution)
	if res == "" {
		res = "720p"
	}
	short, err := strconv.Atoi(strings.TrimSuffix(res, "p"))
	if err != nil || short <= 0 {
		return nil, "risoluzione " + req.Resolution + " non stimabile per " + m.ID
	}
	aspects := []string{req.Aspect}
	if req.Aspect == "" || req.Aspect == "auto" {
		aspects = nil
		for _, a := range m.Aspects {
			if a != "auto" {
				aspects = append(aspects, a)
			}
		}
	}
	w, h := 0, 0
	for _, a := range aspects {
		x, y, ok := aspectSize(short, a)
		if !ok {
			return nil, "formato " + a + " non stimabile per " + m.ID
		}
		if x*y > w*h {
			w, h = x, y
		}
	}
	tokens := float64(w) * float64(h) * float64(dur) * 24 / 1024
	total := tokens / 1000 * m.Pricing["usd_per_1000_tokens"]
	if math.IsNaN(total) || math.IsInf(total, 0) || total < 0 {
		return nil, "stima non valida per " + m.ID
	}
	src := fmt.Sprintf("%s: %g $ per 1000 token × %.0f token (%dx%d, %d s)", m.Source, m.Pricing["usd_per_1000_tokens"], tokens, w, h, dur)
	return &Estimate{USD: math.Round(total*1e4) / 1e4, Exact: total, Source: src}, ""
}

// aspectSize sono larghezza e altezza di un formato a:b col lato corto short.
func aspectSize(short int, aspect string) (w, h int, ok bool) {
	a, b, found := strings.Cut(aspect, ":")
	x, err1 := strconv.Atoi(a)
	y, err2 := strconv.Atoi(b)
	if !found || err1 != nil || err2 != nil || x <= 0 || y <= 0 {
		return 0, 0, false
	}
	long := int(math.Round(float64(short) * float64(max(x, y)) / float64(min(x, y))))
	if x >= y {
		return long, short, true
	}
	return short, long, true
}

// AvatarAudioWarnS è la durata dell'audio oltre la quale giv avatar avvisa (e
// con --strict rifiuta): la durata massima dell'Avatar v2 non è nelle schede di
// fal, il valore è prudente e va confermato con la prova a pagamento.
const AvatarAudioWarnS = 60

// FalOps sono le operazioni sulle clip di fal (giv avatar): schede fissate in
// giv come quelle video. Il primo modello di un'operazione è il default.
func FalOps() []Op {
	avatar := func(model, endpoint string, rate float64) Op {
		return Op{
			Op: "avatar", Model: model,
			Flags: []string{"image", "audio", "prompt"}, Required: []string{"image", "audio"},
			Pricing: map[string]float64{"usd_per_second": rate},
			Price:   avatarPrice(rate),
			Source:  falSource(endpoint),
		}
	}
	return []Op{
		avatar("avatar-v2-standard", "fal-ai/kling-video/ai-avatar/v2/standard", 0.0562),
		avatar("avatar-v2-pro", "fal-ai/kling-video/ai-avatar/v2/pro", 0.115),
	}
}

// avatarPrice stima l'Avatar v2: la clip dura quanto l'audio, contato per
// secondi interi in eccesso (prudente: il conteggio di fal non è nella scheda).
func avatarPrice(rate float64) func(req provider.ClipRequest) (float64, string, string) {
	return func(req provider.ClipRequest) (float64, string, string) {
		d := req.Source.AudioDuration
		if math.IsNaN(d) || math.IsInf(d, 0) || d <= 0 {
			return 0, "", "durata dell'audio non nota (serve ffprobe)"
		}
		s := math.Ceil(d)
		return s * rate, fmt.Sprintf("%g $/s × %g s (audio di %.2f s, arrotondato al secondo)", rate, s, d), ""
	}
}
