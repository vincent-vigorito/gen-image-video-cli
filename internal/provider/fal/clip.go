package fal

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/provider"
)

// clipCard è una scheda delle operazioni sulle clip (giv avatar, giv upscale): operazione
// ed endpoint della coda. Capacità, regole e prezzi sono in caps.FalOps(), con
// gli stessi id.
type clipCard struct {
	op, endpoint string
}

var clipCards = map[string]clipCard{
	"avatar-v2-standard": {op: "avatar", endpoint: "fal-ai/kling-video/ai-avatar/v2/standard"},
	"avatar-v2-pro":      {op: "avatar", endpoint: "fal-ai/kling-video/ai-avatar/v2/pro"},
	"topaz-upscale":      {op: "upscale", endpoint: "fal-ai/topaz/upscale/video"},
}

// GenerateClip carica gli ingressi locali sullo storage di fal, avvia il job
// sulla coda (una volta sola) e lo attende fino al video scaricato.
func (c *Client) GenerateClip(ctx context.Context, req provider.ClipRequest) (*provider.Result, error) {
	cd, ok := clipCards[req.Model]
	if !ok || cd.op != req.Op {
		return nil, fmt.Errorf("fal: giv %s non disponibile con la scheda %q", req.Op, req.Model)
	}
	var body map[string]any
	var err error
	switch cd.op {
	case "upscale":
		body, err = c.upscaleBody(ctx, req)
	default:
		body, err = c.avatarBody(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	job, err := c.start(ctx, cd.endpoint, body)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni %s)\n", job.ID, c.poll)
	provider.ClipJobStarted(name, job, req)
	return c.wait(ctx, job, req.Timeout, false)
}

// avatarBody: fal-ai/kling-video/ai-avatar/v2/{standard,pro}. Immagine e voce
// sono file locali (un'immagine da URL è già scaricata da giv), caricati sullo
// storage di fal; il prompt è facoltativo (default del modello ".").
func (c *Client) avatarBody(ctx context.Context, req provider.ClipRequest) (map[string]any, error) {
	if req.Image == "" || req.Audio == "" {
		return nil, fmt.Errorf("fal: giv avatar vuole un'immagine e un audio")
	}
	img, err := c.upload(ctx, req.Image, 1)
	if err != nil {
		return nil, err
	}
	voice, err := c.uploadAudio(ctx, req.Audio, 2)
	if err != nil {
		return nil, err
	}
	b := map[string]any{"image_url": img, "audio_url": voice}
	if strings.TrimSpace(req.Prompt) != "" {
		b["prompt"] = req.Prompt
	}
	return b, nil
}

// upscaleBody: fal-ai/topaz/upscale/video. La clip è un file locale caricato
// sullo storage di fal, oppure un URL https passato così com'è; il fattore va
// sempre nel corpo (il default della scheda, su cui è fatta la stima).
func (c *Client) upscaleBody(ctx context.Context, req provider.ClipRequest) (map[string]any, error) {
	op := caps.FindOp(caps.FalOps(), req.Op, req.Model)
	if op == nil || req.Video == "" {
		return nil, fmt.Errorf("fal: giv upscale vuole --video")
	}
	src := req.Video
	if strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://") {
		if err := checkExternal(src); err != nil {
			return nil, fmt.Errorf("fal: --video: %w", err)
		}
	} else {
		var err error
		if src, err = c.uploadVideo(ctx, req.Video, 1); err != nil {
			return nil, err
		}
	}
	return map[string]any{"video_url": src, "upscale_factor": op.Factor(req)}, nil
}
