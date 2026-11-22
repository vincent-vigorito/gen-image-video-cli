package openaicompat

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
// con la clip sorgente in input_references, attesa e scaricamento come giv
// video (wait, routerPoll). Un URL https va al provider così com'è; un file
// locale va nel corpo come data URI, che OpenRouter non documenta: se lo
// rifiuta, il messaggio chiede un URL https. Avvio, attesa e scaricamento
// passano dalle richieste protette di guard.go.

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
		err = httpx.Start(err)
		if local && bodyRejected(err) {
			err = fmt.Errorf("%w; %s", err, dataURIHint)
		}
		return nil, err
	}
	// un job senza id valido o senza un indirizzo di attesa recuperabile non si
	// registra né si attende: esito incerto, nessun nuovo avvio
	job := provider.Job{ID: resp.ID, PollingURL: resp.PollingURL}
	if err := c.startedJob(job); err != nil {
		return nil, httpx.Start(err)
	}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni %s)\n", job.ID, pollInterval)
	provider.ClipJobStarted(c.name, job, req)
	res, err := c.wait(ctx, job, req.Timeout, false)
	if err != nil && local && errors.Is(err, provider.ErrJobFailed) {
		err = fmt.Errorf("%w; %s", err, dataURIHint)
	}
	return res, err
}

// routerVideoURL è la clip sorgente nella forma di input_references: un URL
// https assoluto senza credenziali così com'è, un file locale come data URI
// (regole di provider.OpenVideo, letti al massimo i byte controllati). local
// dice se è un file locale.
func (c *Client) routerVideoURL(p string) (src string, local bool, err error) {
	if strings.HasPrefix(p, "https://") || strings.HasPrefix(p, "http://") || p == "" {
		if err := checkExternal(p); err != nil {
			return "", false, fmt.Errorf("%s: --video: %w", c.name, err)
		}
		return p, false, nil
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
