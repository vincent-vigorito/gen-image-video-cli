package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
)

// Operazioni sulle clip esistenti via OpenRouter (giv upscale): POST /videos
// con la clip sorgente in input_references, attesa e scaricamento come giv
// video (wait, routerPoll). La clip va solo come URL https: OpenRouter rifiuta
// il file locale come data URI (400 «Only HTTPS URLs are allowed», prova
// CLI-929 del 22/11/2026) e giv rifiuta un --video locale prima di ogni
// chiamata. Avvio, attesa e scaricamento passano dalle richieste protette di
// guard.go.

// creativity di flux-video-upscale per --mode
var routerCreativity = map[string]int{"precise": 0, "creative": 1}

// routerClip fa l'upscale con black-forest-labs/flux-video-upscale: fattore e
// creativity sempre nel corpo (il default è quello della tabella, su cui è
// fatta la stima), prompt solo se indicato.
func (c *Client) routerClip(ctx context.Context, req provider.ClipRequest) (*provider.Result, error) {
	op := caps.FindOp(caps.OpenRouterOps(), req.Op, req.Model)
	if req.Op != "upscale" || op == nil {
		return nil, fmt.Errorf("%s: giv %s con %s non disponibile", c.name, req.Op, req.Model)
	}
	creativity, ok := routerCreativity[op.Mode(req)]
	if !ok {
		return nil, fmt.Errorf("%s: --mode %s non ammesso da %s", c.name, op.Mode(req), req.Model)
	}
	src, err := c.routerVideoURL(req.Video)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":            req.Model,
		"input_references": []map[string]any{{"type": "video_url", "video_url": map[string]string{"url": src}}},
		"upscale_factor":   op.Factor(req),
		"creativity":       creativity,
	}
	if req.Prompt != "" {
		body["prompt"] = req.Prompt
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ID         string `json:"id"`
		PollingURL string `json:"polling_url"`
	}
	// l'avvio non si ripete né segue redirect: con un 3xx, un 5xx o un timeout potrebbe essere partito
	if err := c.apiCall(ctx, httpx.NoRetry, http.MethodPost, c.baseURL+"/videos", "application/json", payload, &resp); err != nil {
		return nil, httpx.Start(err)
	}
	// un job senza id valido o senza un indirizzo di attesa recuperabile non si
	// registra né si attende: esito incerto, nessun nuovo avvio
	job := provider.Job{ID: resp.ID, PollingURL: resp.PollingURL}
	if err := c.startedJob(job); err != nil {
		return nil, httpx.Start(err)
	}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni %s)\n", job.ID, pollInterval)
	provider.ClipJobStarted(c.name, job, req)
	return c.wait(ctx, job, req.Timeout, false)
}

// routerVideoURL è la clip sorgente per input_references: solo un URL https
// assoluto senza credenziali, così com'è; un file locale si rifiuta.
func (c *Client) routerVideoURL(p string) (string, error) {
	if p != "" && !strings.HasPrefix(p, "https://") && !strings.HasPrefix(p, "http://") {
		return "", fmt.Errorf("%s: --video vuole un indirizzo https pubblico, non un file locale (oppure --provider fal)", c.name)
	}
	if err := checkExternal(p); err != nil {
		return "", fmt.Errorf("%s: --video: %w", c.name, err)
	}
	return p, nil
}
