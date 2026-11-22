package openaicompat

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
)

// Operazioni sulle clip esistenti via OpenRouter (giv upscale): POST /videos
// con la clip sorgente in input_references, poi attesa e scaricamento come per
// i video (routerPoll). Un URL https va al provider così com'è; un file locale
// va nel corpo come data URI, che OpenRouter non documenta: se lo rifiuta, il
// messaggio chiede un URL https.

// dataURIHint accompagna un rifiuto quando la clip è andata come data URI.
const dataURIHint = "la clip locale è andata a openrouter come data URI, che OpenRouter non documenta: " +
	"se il rifiuto dipende da questo, pubblica il video e passa --video con un URL https"

// creativity di flux-video-upscale per --mode
var routerCreativity = map[string]int{"precise": 0, "creative": 1}

// GenerateClip avvia un'operazione su una clip (avvio mai ripetuto), la
// segnala al registro dei job, la attende e ne scarica il risultato.
func (c *Client) GenerateClip(ctx context.Context, req provider.ClipRequest) (*provider.Result, error) {
	switch {
	case c.opts.Router:
		return c.routerClip(ctx, req)
	}
	return nil, fmt.Errorf("giv %s non disponibile con %s", req.Op, c.name)
}

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
	src, local, err := c.routerVideoURL(req.Video)
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
	var resp struct {
		ID         string `json:"id"`
		PollingURL string `json:"polling_url"`
		Status     string `json:"status"`
	}
	// l'avvio non si ripete da solo: con un 5xx o un timeout potrebbe essere partito
	if err := c.doJSONWith(ctx, httpx.NoRetry, http.MethodPost, c.baseURL+"/videos", body, &resp); err != nil {
		err = httpx.Start(err)
		if local && bodyRejected(err) {
			err = fmt.Errorf("%w; %s", err, dataURIHint)
		}
		return nil, err
	}
	if resp.PollingURL == "" {
		return nil, httpx.Start(fmt.Errorf("%s: nessuna polling_url nella risposta (job %q, status %q)", c.name, resp.ID, resp.Status))
	}
	job := provider.Job{ID: resp.ID, PollingURL: resp.PollingURL}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni %s)\n", job.ID, pollInterval)
	provider.ClipJobStarted(c.name, job, req)
	res, err := c.wait(ctx, job, req.Timeout, false)
	if err != nil && local && errors.Is(err, provider.ErrJobFailed) {
		err = fmt.Errorf("%w; %s", err, dataURIHint)
	}
	return res, err
}

// routerVideoURL è la clip sorgente nella forma di input_references: un URL
// https così com'è, un file locale come data URI (regole di provider.OpenVideo,
// letti al massimo i byte controllati). local dice se è un file locale.
func (c *Client) routerVideoURL(p string) (src string, local bool, err error) {
	switch {
	case strings.HasPrefix(p, "https://"):
		return p, false, nil
	case p == "", strings.HasPrefix(p, "http://"):
		return "", false, fmt.Errorf("%s: --video deve essere un file mp4 locale o un URL https", c.name)
	}
	f, mime, size, err := provider.OpenVideo(p)
	if err != nil {
		return "", true, fmt.Errorf("--video %s: %w", p, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, size))
	if err != nil {
		return "", true, fmt.Errorf("--video %s: %w", p, err)
	}
	fmt.Fprintf(os.Stderr, "avviso: clip locale inviata a %s nel corpo della richiesta (data URI, %.1f MiB)\n",
		c.name, float64(len(data))/(1<<20))
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), true, nil
}

// bodyRejected dice se l'avvio è stato rifiutato per il contenuto della
// richiesta (corpo non valido, troppo grande o tipo non accettato).
func bodyRejected(err error) bool {
	var he *httpx.Error
	if !errors.As(err, &he) {
		return false
	}
	switch he.Status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return true
	}
	return false
}
