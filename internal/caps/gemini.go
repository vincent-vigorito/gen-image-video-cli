package caps

import (
	"fmt"
	"strings"

	"gen-image-video-cli/internal/provider"
)

// GeminiVideoModel è il modello video di Gemini diretto (API interactions).
const GeminiVideoModel = "gemini-omni-1.2-flash"

// geminiSource è la fonte della tabella Gemini: va aggiornata a mano a ogni release.
const geminiSource = "tabella giv 22/11/2026: aistudio.google.com/docs/omni.md, aistudio.google.com/docs/pricing.md"

// listino di gemini-omni-1.2-flash in dollari per milione di token
const (
	geminiVideoOutPerM = 12.0
	geminiTextOutPerM  = 9.0
	geminiInPerM       = 1.5
)

// geminiTokensPerSecond sono i token video per secondo d'uscita (listino).
var geminiTokensPerSecond = map[string]float64{"360p": 1931, "720p": 5792, "1080p": 8688, "4k": 17376}

// Valori che giv invia sempre a Gemini se l'utente non li indica: la stima
// vale per la richiesta inviata, non per un default del provider.
const (
	GeminiDefaultDuration   = 10 // giv video, secondi
	GeminiDefaultResolution = "720p"
	GeminiExtendDuration    = 10 // giv extend, secondi aggiunti
	geminiFramesMaxDuration = 10 // con primo e ultimo fotogramma
)

func geminiRate(res string) float64 { return geminiTokensPerSecond[res] * geminiVideoOutPerM / 1e6 }

// Gemini è la tabella fissa dei modelli video di Gemini diretto.
func Gemini() []Model {
	return []Model{{
		ID: GeminiVideoModel, Durations: intRange(3, 15), DefaultDuration: GeminiDefaultDuration,
		Resolutions: []string{"360p", "720p", "1080p", "4k"}, Aspects: []string{"16:9", "9:16"},
		Frames: []string{"first_frame", "last_frame"}, Audio: ptr(true), Seed: ptr(false),
		// primo fotogramma e riferimenti insieme: ruoli non distinguibili nella richiesta
		MaxRefs: ptr(6), ImageWithRefs: ptr(false),
		Pricing: geminiPricing(), Source: geminiSource, check: geminiCheck,
	}}
}

func geminiPricing() map[string]float64 {
	p := map[string]float64{}
	for res := range geminiTokensPerSecond {
		p["duration_seconds_"+res] = geminiRate(res)
	}
	return p
}

// geminiCheck: audio sempre presente, ultimo fotogramma solo dopo il primo e
// fino a 10 s; completa req con durata e risoluzione, che giv indica sempre
// nella richiesta.
func geminiCheck(req *provider.VideoRequest) error {
	m := GeminiVideoModel
	if req.Audio != nil && !*req.Audio {
		return reject("--audio off: %s genera sempre l'audio; per guidarlo scrivilo nel prompt (es. \"No dialogue\")", m)
	}
	if req.LastFrame != "" && req.Image == "" {
		return reject("--last-frame con %s vuole anche --image: primo e ultimo fotogramma vanno in quest'ordine", m)
	}
	if req.LastFrame != "" && len(req.Refs) > 0 {
		return reject("--last-frame e --ref insieme non supportati da %s: usa uno solo dei due", m)
	}
	if req.Duration < 0 {
		return reject("--duration deve essere un intero positivo di secondi")
	}
	if req.Duration == 0 {
		req.Duration = GeminiDefaultDuration
	}
	if req.LastFrame != "" && req.Duration > geminiFramesMaxDuration {
		return reject("--duration %d: con --last-frame %s arriva al massimo a %d s", req.Duration, m, geminiFramesMaxDuration)
	}
	req.Resolution = strings.ToLower(req.Resolution)
	if req.Resolution == "" {
		req.Resolution = GeminiDefaultResolution
	}
	return nil
}

// GeminiOps sono le operazioni di Gemini sui video che ha generato (--from):
// dall'UE i video caricati non si estendono né si modificano.
func GeminiOps() []Op {
	refused := map[string]string{"video": "dall'UE Gemini estende e modifica solo video generati dal modello, " +
		"non file caricati: usa --from <job_id> di un video fatto con giv video --provider gemini"}
	return []Op{
		{
			Op: "extend", Model: GeminiVideoModel, Flags: []string{"from", "duration", "prompt"}, Required: []string{"from", "prompt"},
			Durations: intRange(3, 10), DefaultDuration: GeminiExtendDuration, TotalMax: 30, Refused: refused,
			Pricing: geminiPricing(), Price: geminiClipPrice, Source: geminiSource,
		},
		{
			Op: "edit", Model: GeminiVideoModel, Flags: []string{"from", "prompt"}, Required: []string{"from", "prompt"},
			SourceMax: 15, Refused: refused,
			Pricing: geminiPricing(), Price: geminiClipPrice, Source: geminiSource,
		},
	}
}

// geminiClipPrice stima extend ed edit sui secondi d'uscita alla risoluzione
// del video di partenza; extend conta tutto il video (partenza più aggiunta),
// finché la fatturazione dei soli secondi aggiunti non è verificata.
func geminiClipPrice(req provider.ClipRequest) (float64, string, string) {
	src := req.Source
	if src.Duration <= 0 {
		return 0, "", "durata del video di partenza non nota (job non trovato in giv-log.jsonl di questa cartella)"
	}
	res := GeminiResolution(src.Height)
	if res == "" {
		return 0, "", "risoluzione del video di partenza non nota"
	}
	secs := src.Duration
	detail := fmt.Sprintf("duration_seconds_%s × %.2f s del video di partenza", res, secs)
	if req.Op == "extend" {
		add := req.Duration
		if add <= 0 {
			add = GeminiExtendDuration
		}
		secs += float64(add)
		detail = fmt.Sprintf("duration_seconds_%s × (%.2f s di partenza + %d s aggiunti)", res, src.Duration, add)
	}
	return geminiRate(res) * secs, detail, ""
}

// GeminiHeight è l'altezza nominale di una risoluzione di Gemini (720p = 720,
// 4k = 2160); 0 se non è una di quelle ammesse.
func GeminiHeight(res string) int {
	return map[string]int{"360p": 360, "720p": 720, "1080p": 1080, "4k": 2160}[strings.ToLower(res)]
}

// GeminiResolution è la risoluzione di listino per un'altezza nominale; "" se non nota.
func GeminiResolution(height int) string {
	switch {
	case height <= 0:
		return ""
	case height <= 360:
		return "360p"
	case height <= 720:
		return "720p"
	case height <= 1080:
		return "1080p"
	}
	return "4k"
}

// GeminiUsage sono i token riportati da Gemini a fine job.
type GeminiUsage struct {
	Input       int64 // ingresso (testo, immagini, video di partenza)
	OutputVideo int64 // uscita video
	OutputOther int64 // altra uscita (testo, o non suddivisa per modalità)
	Thought     int64 // ragionamento
}

// GeminiUsageCost è il costo dei token riportati × listino; ok false se non
// ci sono token o i numeri non sono validi. L'uscita non suddivisa per
// modalità va passata come video (tariffa più alta).
func GeminiUsageCost(u GeminiUsage) (usd float64, source string, ok bool) {
	if u.Input < 0 || u.OutputVideo < 0 || u.OutputOther < 0 || u.Thought < 0 {
		return 0, "", false
	}
	if u.Input+u.OutputVideo+u.OutputOther+u.Thought == 0 {
		return 0, "", false
	}
	usd = (float64(u.OutputVideo)*geminiVideoOutPerM + float64(u.OutputOther+u.Thought)*geminiTextOutPerM +
		float64(u.Input)*geminiInPerM) / 1e6
	return usd, "token riportati × listino gemini 22/11/2026 (aistudio.google.com/docs/pricing.md)", true
}

func intRange(lo, hi int) []int {
	v := make([]int, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		v = append(v, i)
	}
	return v
}
