package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strconv"
	"strings"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
)

// giv extend e giv edit con xAI (grok-imagine-video): POST /videos/extensions
// e /videos/edits, la clip locale caricata prima con POST /files, attesa e
// scaricamento col GET dei video. Costo: usage.cost_in_usd_ticks del GET.

const (
	grokFileExpiry  = 3600 // secondi di vita del file caricato su xAI
	grokTicksPerUSD = 1e10 // usage.cost_in_usd_ticks
	grokUploadName  = "giv-clip.mp4"
)

// clipCostKey segna l'attesa di un job dei comandi sulle clip: solo lì il
// costo riportato da xAI entra nel risultato (giv video resta com'è).
type clipCostKey struct{}

// GenerateClip esegue un'operazione su una clip esistente; ogni dialetto ha il
// suo caso (xAI qui).
func (c *Client) GenerateClip(ctx context.Context, req provider.ClipRequest) (*provider.Result, error) {
	switch {
	case c.opts.Grok:
		return c.grokClip(ctx, req)
	}
	return nil, fmt.Errorf("%s: giv %s non supportato", c.name, req.Op)
}

// grokClip avvia extend o edit, registra il job e ne attende il risultato.
// Clip, prompt e file_id vanno solo all'origine dell'API (redirect fuori
// origine non seguiti); l'avvio non si ripete da solo.
func (c *Client) grokClip(ctx context.Context, req provider.ClipRequest) (*provider.Result, error) {
	var path string
	switch req.Op {
	case "extend":
		path = "/videos/extensions"
	case "edit":
		path = "/videos/edits"
	default:
		return nil, fmt.Errorf("%s: giv %s non supportato", c.name, req.Op)
	}
	body := map[string]any{"model": req.Model, "prompt": req.Prompt}
	if req.Op == "extend" {
		d := req.Duration
		if o := caps.FindOp(caps.XAIOps(), req.Op, req.Model); d <= 0 && o != nil {
			d = o.DefaultDuration // esplicito: la stima è su questa durata
		}
		if d > 0 {
			body["duration"] = d
		}
	}
	switch {
	case strings.HasPrefix(req.Video, "https://"):
		body["video"] = map[string]string{"url": req.Video}
	case strings.HasPrefix(req.Video, "http://"), req.Video == "":
		return nil, fmt.Errorf("%s: giv %s vuole --video file mp4 o URL https", c.name, req.Op)
	default:
		id, err := c.grokUpload(ctx, req.Video)
		if err != nil {
			return nil, err
		}
		body["video"] = map[string]string{"file_id": id}
	}

	var started struct {
		RequestID string `json:"request_id"`
	}
	if err := c.doJSONWith(pollOnly(ctx), httpx.NoRetry, http.MethodPost, c.baseURL+path, body, &started); err != nil {
		return nil, httpx.Start(err)
	}
	if started.RequestID == "" {
		return nil, httpx.Start(fmt.Errorf("%s: nessun request_id nella risposta", c.name))
	}
	job := provider.Job{ID: started.RequestID, PollingURL: c.grokPollURL(started.RequestID)}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni %s)\n", job.ID, pollInterval)
	provider.ClipJobStarted(c.name, job, req)
	return c.wait(context.WithValue(ctx, clipCostKey{}, true), job, req.Timeout, false)
}

// grokUpload carica una clip locale con POST /files sull'origine dell'API e
// restituisce il file_id della risposta: file regolare mp4 entro
// MaxVideoBytes (controllato prima di leggerlo), letto al massimo per la
// dimensione controllata, nome fisso, scadenza dopo un'ora.
func (c *Client) grokUpload(ctx context.Context, path string) (string, error) {
	f, mime, size, err := provider.OpenVideo(path)
	if err != nil {
		return "", fmt.Errorf("--video %s: %w", path, err)
	}
	defer f.Close()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// expires_after va prima del file
	if err := w.WriteField("expires_after", strconv.Itoa(grokFileExpiry)); err != nil {
		return "", err
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+grokUploadName+`"`)
	h.Set("Content-Type", mime)
	part, err := w.CreatePart(h)
	if err != nil {
		return "", err
	}
	if n, err := io.Copy(part, io.LimitReader(f, size)); err != nil || n != size {
		return "", fmt.Errorf("--video %s: file non leggibile", path)
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	var up struct {
		ID     string `json:"id"`
		FileID string `json:"file_id"`
	}
	// redirect fuori origine non seguiti: il file parte solo verso xAI
	if err := c.do(pollOnly(ctx), http.MethodPost, c.baseURL+"/files", w.FormDataContentType(), buf.Bytes(), &up); err != nil {
		return "", fmt.Errorf("caricamento di --video: %w", err)
	}
	id := up.ID
	if id == "" {
		id = up.FileID
	}
	if !validFileID(id) {
		return "", fmt.Errorf("%s: caricamento di --video senza un file_id valido nella risposta", c.name)
	}
	return id, nil
}

// validFileID accetta un file_id di xAI (es. file_5be118c3-…): lettere, cifre,
// '_', '-' e '.', al massimo 128 caratteri, non vuoto.
func validFileID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// grokClipCost è il costo riportato nel GET di un job dei comandi sulle clip
// (usage.cost_in_usd_ticks), 0 se manca o non è un numero valido.
func grokClipCost(ctx context.Context, usage json.RawMessage) float64 {
	if ctx.Value(clipCostKey{}) == nil || len(usage) == 0 {
		return 0
	}
	var u struct {
		Ticks float64 `json:"cost_in_usd_ticks"`
	}
	if err := json.Unmarshal(usage, &u); err != nil || math.IsNaN(u.Ticks) || math.IsInf(u.Ticks, 0) || u.Ticks <= 0 {
		return 0
	}
	return u.Ticks / grokTicksPerUSD
}
