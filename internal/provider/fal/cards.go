package fal

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"gen-image-video-cli/internal/provider"
)

// card è una scheda video: endpoint della coda e corpo della richiesta. Le
// capacità, le regole e i prezzi sono in caps.Fal(), con gli stessi id.
type card struct {
	endpoint string
	// body costruisce l'ingresso del modello; up carica un'immagine locale e
	// ne restituisce file_url
	body func(req provider.VideoRequest, up func(path string) (string, error)) (map[string]any, error)
}

var cards = map[string]*card{
	"kling-v3-pro":     {endpoint: "fal-ai/kling-video/v3/pro/image-to-video", body: klingV3Body},
	"seedance-2.0-ref": {endpoint: "bytedance/seedance-2.0/reference-to-video", body: seedanceBody},
}

var endpoints = func() map[string]string {
	m := map[string]string{}
	for id, c := range cards {
		m[id] = c.endpoint
	}
	return m
}()

// Cards sono gli id delle schede video, in ordine.
func Cards() []string { return slices.Sorted(maps.Keys(cards)) }

// HasCard dice se id è una scheda video di fal.
func HasCard(id string) bool { return cards[id] != nil }

// base sono i parametri passanti (già controllati sulla scheda): i campi di
// giv li sovrascrivono.
func base(req provider.VideoRequest) map[string]any {
	b := map[string]any{}
	maps.Copy(b, req.Params)
	return b
}

func uploadAll(paths []string, up func(string) (string, error)) ([]string, error) {
	urls := make([]string, len(paths))
	for i, p := range paths {
		var err error
		if urls[i], err = up(p); err != nil {
			return nil, err
		}
	}
	return urls, nil
}

// klingV3Body: fal-ai/kling-video/v3/pro/image-to-video. Durata come stringa;
// i riferimenti sono un solo elemento (il primo frontale, gli altri di
// riferimento).
func klingV3Body(req provider.VideoRequest, up func(string) (string, error)) (map[string]any, error) {
	b := base(req)
	if req.Negative != "" && b["negative_prompt"] == nil {
		b["negative_prompt"] = req.Negative
	}
	if req.Prompt != "" {
		b["prompt"] = req.Prompt
	}
	start, err := up(req.Image)
	if err != nil {
		return nil, err
	}
	b["start_image_url"] = start
	if req.LastFrame != "" {
		if b["end_image_url"], err = up(req.LastFrame); err != nil {
			return nil, err
		}
	}
	if len(req.Refs) > 0 {
		urls, err := uploadAll(req.Refs, up)
		if err != nil {
			return nil, err
		}
		b["elements"] = []map[string]any{{"frontal_image_url": urls[0], "reference_image_urls": urls[1:]}}
	}
	if req.Duration > 0 {
		b["duration"] = strconv.Itoa(req.Duration)
	}
	if req.Audio != nil {
		b["generate_audio"] = *req.Audio
	}
	return b, nil
}

// seedanceBody: bytedance/seedance-2.0/reference-to-video. Prompt
// obbligatorio, riferimenti in image_urls, durata come stringa.
func seedanceBody(req provider.VideoRequest, up func(string) (string, error)) (map[string]any, error) {
	b := base(req)
	b["prompt"] = req.Prompt
	urls, err := uploadAll(req.Refs, up)
	if err != nil {
		return nil, err
	}
	b["image_urls"] = urls
	if req.Duration > 0 {
		b["duration"] = strconv.Itoa(req.Duration)
	}
	if req.Resolution != "" {
		b["resolution"] = strings.ToLower(req.Resolution)
	}
	if req.Aspect != "" {
		b["aspect_ratio"] = req.Aspect
	}
	if req.Audio != nil {
		b["generate_audio"] = *req.Audio
	}
	return b, nil
}
