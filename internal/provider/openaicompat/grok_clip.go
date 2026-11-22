package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"regexp"
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

// noRedirectKey segna i POST dei comandi sulle clip (caricamento e avvio):
// nessun redirect seguito, così corpo e file non partono una seconda volta né
// verso un altro host.
type noRedirectKey struct{}

func noRedirect(ctx context.Context) context.Context {
	return context.WithValue(ctx, noRedirectKey{}, true)
}

// clipInfo accompagna nel contesto le richieste di un comando sulle clip:
// solo lì il costo riportato da xAI entra nel risultato (giv video resta
// com'è) e i messaggi d'errore perdono URL e file_id.
type clipInfo struct {
	hidden []string // URL del sorgente e file_id: mai nei messaggi
}

type clipKey struct{}

func clipFrom(ctx context.Context) *clipInfo {
	ci, _ := ctx.Value(clipKey{}).(*clipInfo)
	return ci
}

var urlInText = regexp.MustCompile(`https?://[^\s"'<>\\]+`)

// redact toglie da un testo i valori nascosti e riduce ogni URL a schema e host.
func (ci *clipInfo) redact(s string) string {
	for _, h := range ci.hidden {
		if h != "" {
			s = strings.ReplaceAll(s, h, "<…>")
		}
	}
	return urlInText.ReplaceAllStringFunc(s, shortURL)
}

// scrub ripulisce i messaggi del provider e gli URL degli errori di rete nella
// catena di err, lasciando tipi e classificazione (avvio incerto, job, stato
// HTTP).
func (ci *clipInfo) scrub(err error) error {
	if err == nil {
		return nil
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		he.Msg = ci.redact(he.Msg)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = shortURL(ue.URL)
	}
	return err
}

// jobError è il motivo di un job fallito: codice e messaggio di xAI ripuliti.
func (ci *clipInfo) jobError(raw json.RawMessage) string {
	var e struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		if json.Unmarshal(raw, &e.Message) != nil {
			return "(dettaglio non leggibile)"
		}
	}
	msg := strings.TrimSpace(e.Message)
	if e.Code != "" {
		msg = strings.TrimSpace(e.Code + ": " + msg)
	}
	return ci.redact(msg)
}

// shortURL riduce un URL a schema e host.
func shortURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<url>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// GenerateClip esegue un'operazione su una clip esistente; ogni dialetto ha il
// suo caso (xAI qui).
func (c *Client) GenerateClip(ctx context.Context, req provider.ClipRequest) (*provider.Result, error) {
	switch {
	case c.opts.Grok:
		return c.grokClip(ctx, req)
	}
	return nil, fmt.Errorf("%s: giv %s non supportato", c.name, req.Op)
}

// grokClip è grokClipRun con i messaggi d'errore ripuliti da URL e file_id.
func (c *Client) grokClip(ctx context.Context, req provider.ClipRequest) (*provider.Result, error) {
	ci := &clipInfo{}
	if strings.HasPrefix(req.Video, "https://") {
		ci.hidden = append(ci.hidden, req.Video)
	}
	res, err := c.grokClipRun(context.WithValue(ctx, clipKey{}, ci), ci, req)
	return res, ci.scrub(err)
}

// grokClipRun avvia extend o edit, registra il job e ne attende il risultato.
// Clip, prompt e file_id vanno solo all'API; caricamento e avvio non si
// ripetono e non seguono redirect.
func (c *Client) grokClipRun(ctx context.Context, ci *clipInfo, req provider.ClipRequest) (*provider.Result, error) {
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
		ci.hidden = append(ci.hidden, id)
		body["video"] = map[string]string{"file_id": id}
	}

	var started struct {
		RequestID string `json:"request_id"`
	}
	if err := c.doJSONWith(noRedirect(ctx), httpx.NoRetry, http.MethodPost, c.baseURL+path, body, &started); err != nil {
		return nil, httpx.Start(err)
	}
	if started.RequestID == "" {
		return nil, httpx.Start(fmt.Errorf("%s: nessun request_id nella risposta", c.name))
	}
	job := provider.Job{ID: started.RequestID, PollingURL: c.grokPollURL(started.RequestID)}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni %s)\n", job.ID, pollInterval)
	provider.ClipJobStarted(c.name, job, req)
	return c.wait(ctx, job, req.Timeout, false)
}

// grokUpload carica una clip locale con POST /files sull'origine dell'API e
// restituisce il file_id della risposta: file regolare mp4 entro
// MaxVideoBytes (controllato prima di leggerlo), letto al massimo per la
// dimensione controllata, nome fisso, scadenza dopo un'ora. Una sola
// richiesta, senza redirect: lo stesso file non parte due volte.
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
	if err := c.doWith(noRedirect(ctx), httpx.NoRetry, http.MethodPost, c.baseURL+"/files", w.FormDataContentType(), buf.Bytes(), &up); err != nil {
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
// (usage.cost_in_usd_ticks), 0 se manca, non è un numero valido o il job non è
// di un comando sulle clip.
func grokClipCost(ctx context.Context, usage json.RawMessage) float64 {
	if clipFrom(ctx) == nil || len(usage) == 0 {
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
