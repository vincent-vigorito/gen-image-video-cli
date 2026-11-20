package openaicompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
)

// pollInterval e' l'attesa fra due controlli dello stato di un job video;
// i test del package la accorciano.
var pollInterval = 10 * time.Second

// Options distingue i dialetti dei provider OpenAI-compatible.
type Options struct {
	Router   bool // endpoint dedicati /images e /videos di OpenRouter
	Grok     bool // video via API /videos/generations di x.ai
	UseSize  bool // mappa --aspect sul parametro size (OpenAI)
	B64Param bool // invia response_format=b64_json (x.ai; gpt-image-1 lo rifiuta)
}

type Client struct {
	name    string
	baseURL string
	apiKey  string
	opts    Options
	http    *http.Client
}

func New(name, baseURL, apiKey string, opts Options) *Client {
	return &Client{
		name: name, baseURL: baseURL, apiKey: apiKey, opts: opts,
		http: &http.Client{Timeout: 300 * time.Second},
	}
}

func (c *Client) Name() string { return c.name }

func (c *Client) do(ctx context.Context, method, url, contentType string, body []byte, out any) error {
	return c.doWith(ctx, httpx.Retry, method, url, contentType, body, out)
}

func (c *Client) doWith(ctx context.Context, retry httpx.RetryFunc, method, url, contentType string, body []byte, out any) error {
	return retry(ctx, func() error {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return &httpx.Error{Prefix: c.name, Status: resp.StatusCode, Msg: apiErrorMessage(data),
				RetryAfter: httpx.ParseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		if out != nil {
			return json.Unmarshal(data, out)
		}
		return nil
	})
}

func (c *Client) doJSON(ctx context.Context, method, url string, body, out any) error {
	return c.doJSONWith(ctx, httpx.Retry, method, url, body, out)
}

func (c *Client) doJSONWith(ctx context.Context, retry httpx.RetryFunc, method, url string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	ct := ""
	if payload != nil {
		ct = "application/json"
	}
	return c.doWith(ctx, retry, method, url, ct, payload, out)
}

func apiErrorMessage(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(data))
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	return s
}

// --- models ---

func (c *Client) Models(ctx context.Context) ([]provider.ModelInfo, error) {
	var out []provider.ModelInfo
	if c.opts.Router {
		var resp struct {
			Data []struct {
				ID           string         `json:"id"`
				Name         string         `json:"name"`
				Pricing      map[string]any `json:"pricing"`
				Architecture struct {
					OutputModalities []string `json:"output_modalities"`
				} `json:"architecture"`
			} `json:"data"`
		}
		// senza filtro /models elenca solo i modelli con output testuale
		if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/models?output_modalities=all", nil, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Data {
			kind := "other"
			for _, mod := range m.Architecture.OutputModalities {
				if mod == "image" {
					kind = "image"
				}
				if mod == "video" {
					kind = "video"
				}
			}
			out = append(out, provider.ModelInfo{Name: m.ID, DisplayName: m.Name, Kind: kind, Pricing: m.Pricing})
		}
	} else {
		var resp struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/models", nil, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Data {
			lower := strings.ToLower(m.ID)
			kind := "other"
			switch {
			case strings.Contains(lower, "sora") || strings.Contains(lower, "video"):
				kind = "video"
			case strings.Contains(lower, "image") || strings.Contains(lower, "dall-e"):
				kind = "image"
			}
			out = append(out, provider.ModelInfo{Name: m.ID, Kind: kind})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// VideoCaps sono le capacità dei modelli video: per OpenRouter dal catalogo
// /videos/models (gratuito, letto a ogni chiamata), per xAI dalla tabella di giv.
func (c *Client) VideoCaps(ctx context.Context) ([]caps.Model, error) {
	switch {
	case c.opts.Router:
		var raw json.RawMessage
		if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/videos/models", nil, &raw); err != nil {
			return nil, err
		}
		return caps.ParseOpenRouter(raw)
	case c.opts.Grok:
		return caps.XAI(), nil
	}
	return nil, fmt.Errorf("capacità video non disponibili per %s", c.name)
}

// --- immagini ---

func (c *Client) GenerateImage(ctx context.Context, req provider.ImageRequest) (*provider.Result, error) {
	if c.opts.Router {
		return c.routerImages(ctx, req)
	}
	if req.Seed != 0 {
		fmt.Fprintf(os.Stderr, "avviso: --seed non supportato da %s, ignorato\n", c.name)
	}
	var resp imagesResponse
	var err error
	if len(req.Inputs) > 0 {
		resp, err = c.imagesEdits(ctx, req)
	} else {
		resp, err = c.imagesGenerations(ctx, req)
	}
	if err != nil {
		return nil, err
	}
	media, err := c.mediaFromImages(ctx, resp)
	if err != nil {
		return nil, err
	}
	return &provider.Result{Media: media}, nil
}

func (c *Client) imagesGenerations(ctx context.Context, req provider.ImageRequest) (imagesResponse, error) {
	body := map[string]any{"model": req.Model, "prompt": req.Prompt}
	if req.N > 1 {
		body["n"] = req.N
	}
	if req.Aspect != "" {
		if c.opts.UseSize {
			body["size"] = sizeForAspect(req.Aspect)
		} else {
			fmt.Fprintf(os.Stderr, "avviso: --aspect non supportato da %s, ignorato\n", c.name)
		}
	}
	if c.opts.B64Param {
		body["response_format"] = "b64_json"
	}
	var resp imagesResponse
	err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/images/generations", body, &resp)
	return resp, err
}

func (c *Client) imagesEdits(ctx context.Context, req provider.ImageRequest) (imagesResponse, error) {
	var resp imagesResponse
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("model", req.Model)
	w.WriteField("prompt", req.Prompt)
	if req.N > 1 {
		w.WriteField("n", strconv.Itoa(req.N))
	}
	if req.Aspect != "" && c.opts.UseSize {
		w.WriteField("size", sizeForAspect(req.Aspect))
	}
	for _, path := range req.Inputs {
		if err := addFilePart(w, "image[]", path); err != nil {
			return resp, err
		}
	}
	if err := w.Close(); err != nil {
		return resp, err
	}
	err := c.do(ctx, http.MethodPost, c.baseURL+"/images/edits", w.FormDataContentType(), buf.Bytes(), &resp)
	return resp, err
}

// routerImages usa l'endpoint dedicato /images di OpenRouter (unico supportato
// dai modelli ByteDance; accetta anche quelli Google/OpenAI).
func (c *Client) routerImages(ctx context.Context, req provider.ImageRequest) (*provider.Result, error) {
	body := map[string]any{"model": req.Model, "prompt": req.Prompt}
	if req.Aspect != "" {
		body["aspect_ratio"] = req.Aspect
	}
	if req.Seed != 0 {
		body["seed"] = req.Seed
	}
	if len(req.Inputs) > 0 {
		var refs []map[string]any
		for _, path := range req.Inputs {
			u, err := dataURL(path)
			if err != nil {
				return nil, err
			}
			refs = append(refs, map[string]any{
				"type":      "image_url",
				"image_url": map[string]string{"url": u},
			})
		}
		body["input_references"] = refs
	}
	// /images accetta solo n=1: con -n > 1 una richiesta per immagine, costi sommati
	res := &provider.Result{}
	for i := 0; i < max(req.N, 1); i++ {
		var resp imagesResponse
		if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/images", body, &resp); err != nil {
			return nil, err
		}
		media, err := c.mediaFromImages(ctx, resp)
		if err != nil {
			return nil, err
		}
		res.Media = append(res.Media, media...)
		res.CostUSD += resp.Usage.Cost
	}
	return res, nil
}

type imagesResponse struct {
	Data []struct {
		B64JSON   string `json:"b64_json"`
		URL       string `json:"url"`
		MediaType string `json:"media_type"`
	} `json:"data"`
	Usage struct {
		Cost float64 `json:"cost"`
	} `json:"usage"`
}

func (c *Client) mediaFromImages(ctx context.Context, resp imagesResponse) ([]provider.Media, error) {
	var media []provider.Media
	for _, d := range resp.Data {
		var data []byte
		var err error
		switch {
		case d.B64JSON != "":
			data, err = base64.StdEncoding.DecodeString(d.B64JSON)
		case d.URL != "":
			data, err = c.download(ctx, d.URL)
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		mime := d.MediaType
		if mime == "" {
			mime = http.DetectContentType(data)
		}
		media = append(media, provider.Media{Mime: mime, Data: data})
	}
	if len(media) == 0 {
		return nil, fmt.Errorf("%s: nessuna immagine nella risposta", c.name)
	}
	return media, nil
}

// --- video ---

func (c *Client) GenerateVideo(ctx context.Context, req provider.VideoRequest) (*provider.Result, error) {
	switch {
	case c.opts.Router:
		return c.routerVideo(ctx, req)
	case c.opts.Grok:
		return c.grokVideo(ctx, req)
	default:
		return nil, fmt.Errorf("generazione video non supportata per %s", c.name)
	}
}

// grokVideo usa l'API asincrona /videos/generations di x.ai (grok-imagine-video).
func (c *Client) grokVideo(ctx context.Context, req provider.VideoRequest) (res *provider.Result, err error) {
	if req.Negative != "" {
		fmt.Fprintf(os.Stderr, "avviso: --negative non supportato da %s, ignorato\n", c.name)
	}
	if req.Seed != 0 {
		fmt.Fprintf(os.Stderr, "avviso: --seed non supportato da %s, ignorato\n", c.name)
	}
	if len(req.Params) > 0 {
		fmt.Fprintf(os.Stderr, "avviso: --param non supportato da %s, ignorato\n", c.name)
	}
	if req.Image != "" && req.Aspect != "" {
		fmt.Fprintf(os.Stderr, "avviso: con --image %s ignora --aspect (segue l'immagine)\n", c.name)
	}
	body := map[string]any{"model": req.Model, "prompt": req.Prompt}
	if req.Duration > 0 {
		body["duration"] = req.Duration
	}
	if req.Aspect != "" {
		body["aspect_ratio"] = req.Aspect
	}
	if req.Resolution != "" {
		body["resolution"] = req.Resolution
	}
	if req.Image != "" {
		u, err := imageDataURL(req.Image)
		if err != nil {
			return nil, err
		}
		body["image"] = map[string]string{"url": u}
	}
	if req.LastFrame != "" {
		u, err := imageDataURL(req.LastFrame)
		if err != nil {
			return nil, err
		}
		body["last_frame"] = map[string]string{"url": u}
	}
	if len(req.Refs) > 0 {
		refs := make([]map[string]string, 0, len(req.Refs))
		for _, r := range req.Refs {
			u, err := imageDataURL(r)
			if err != nil {
				return nil, err
			}
			refs = append(refs, map[string]string{"url": u})
		}
		body["reference_images"] = refs
	}
	if req.Audio != nil {
		body["generate_audio"] = *req.Audio
	}

	var job struct {
		RequestID string `json:"request_id"`
	}
	// l'avvio del job non si ripete da solo: con un 5xx o un timeout potrebbe essere partito
	if err := c.doJSONWith(ctx, httpx.NoRetry, http.MethodPost, c.baseURL+"/videos/generations", body, &job); err != nil {
		return nil, httpx.Start(err)
	}
	if job.RequestID == "" {
		return nil, httpx.Start(fmt.Errorf("%s: nessun request_id nella risposta", c.name))
	}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni %s)\n", job.RequestID, pollInterval)
	// da qui ogni errore porta l'id del job
	defer func() { err = httpx.Job(job.RequestID, err) }()

	timeout := jobTimeout(req.Timeout)
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: timeout dopo %s", c.name, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
		var st struct {
			Status string `json:"status"`
			Video  struct {
				URL string `json:"url"`
			} `json:"video"`
			Error json.RawMessage `json:"error"`
		}
		if err := c.doJSONWith(ctx, httpx.RetryPoll, http.MethodGet, c.baseURL+"/videos/"+job.RequestID, nil, &st); err != nil {
			return nil, err
		}
		switch st.Status {
		case "done", "completed":
			fmt.Fprintln(os.Stderr)
			if st.Video.URL == "" {
				return nil, fmt.Errorf("%s: job completato ma nessun video nella risposta", c.name)
			}
			data, err := c.download(ctx, st.Video.URL)
			if err != nil {
				return nil, err
			}
			return &provider.Result{Media: []provider.Media{{Mime: "video/mp4", Data: data}}, JobID: job.RequestID}, nil
		case "failed", "error", "expired":
			fmt.Fprintln(os.Stderr)
			return nil, fmt.Errorf("%s: job %s: %s", c.name, st.Status, string(st.Error))
		default:
			fmt.Fprint(os.Stderr, ".")
		}
	}
}

func (c *Client) routerVideo(ctx context.Context, req provider.VideoRequest) (res *provider.Result, err error) {
	body := map[string]any{"model": req.Model, "prompt": req.Prompt}
	if req.Duration > 0 {
		body["duration"] = req.Duration
	}
	if req.Resolution != "" {
		body["resolution"] = req.Resolution
	}
	if req.Aspect != "" {
		body["aspect_ratio"] = req.Aspect
	}
	var frames []map[string]any
	for _, f := range []struct{ path, kind string }{{req.Image, "first_frame"}, {req.LastFrame, "last_frame"}} {
		if f.path == "" {
			continue
		}
		part, err := imageURLPart(f.path)
		if err != nil {
			return nil, err
		}
		part["frame_type"] = f.kind
		frames = append(frames, part)
	}
	if len(frames) > 0 {
		body["frame_images"] = frames
	}
	if len(req.Refs) > 0 {
		refs := make([]map[string]any, 0, len(req.Refs))
		for _, r := range req.Refs {
			part, err := imageURLPart(r)
			if err != nil {
				return nil, err
			}
			refs = append(refs, part)
		}
		body["input_references"] = refs
	}
	if req.Audio != nil {
		body["generate_audio"] = *req.Audio
	}
	if req.Seed != 0 {
		body["seed"] = req.Seed
	}
	params := req.Params
	if req.Negative != "" {
		key := routerNegativeKey(req.Model)
		switch {
		case key == "":
			fmt.Fprintf(os.Stderr, "avviso: --negative non supportato da %s, ignorato\n", req.Model)
		case hasKey(params, key):
			fmt.Fprintf(os.Stderr, "avviso: --param %s prevale su --negative\n", key)
		default:
			params = maps.Clone(params)
			if params == nil {
				params = map[string]any{}
			}
			params[key] = req.Negative
		}
	}
	if len(params) > 0 {
		body["provider"] = map[string]any{"options": routerOptions(req.Model, params)}
	}

	var job struct {
		ID         string `json:"id"`
		PollingURL string `json:"polling_url"`
		Status     string `json:"status"`
	}
	// l'avvio del job non si ripete da solo: con un 5xx o un timeout potrebbe essere partito
	if err := c.doJSONWith(ctx, httpx.NoRetry, http.MethodPost, c.baseURL+"/videos", body, &job); err != nil {
		return nil, httpx.Start(err)
	}
	if job.PollingURL == "" {
		return nil, httpx.Start(fmt.Errorf("%s: nessuna polling_url nella risposta (job %q, status %q)", c.name, job.ID, job.Status))
	}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni %s)\n", job.ID, pollInterval)
	// da qui ogni errore porta l'id del job
	defer func() { err = httpx.Job(job.ID, err) }()

	timeout := jobTimeout(req.Timeout)
	deadline := time.Now().Add(timeout)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: timeout dopo %s", c.name, timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
		var st struct {
			Status       string          `json:"status"`
			UnsignedURLs []string        `json:"unsigned_urls"`
			Error        json.RawMessage `json:"error"`
			Usage        struct {
				Cost float64 `json:"cost"`
			} `json:"usage"`
		}
		if err := c.doJSONWith(ctx, httpx.RetryPoll, http.MethodGet, job.PollingURL, nil, &st); err != nil {
			return nil, err
		}
		switch st.Status {
		case "completed":
			fmt.Fprintln(os.Stderr)
			if len(st.UnsignedURLs) == 0 {
				return nil, fmt.Errorf("%s: job completato ma nessun video nella risposta", c.name)
			}
			var media []provider.Media
			for _, u := range st.UnsignedURLs {
				// le content URL stanno sullo stesso host API e richiedono l'Authorization
				data, err := c.downloadAuth(ctx, u)
				if err != nil {
					return nil, err
				}
				media = append(media, provider.Media{Mime: "video/mp4", Data: data})
			}
			return &provider.Result{Media: media, CostUSD: st.Usage.Cost, JobID: job.ID}, nil
		case "failed", "cancelled", "error":
			fmt.Fprintln(os.Stderr)
			return nil, fmt.Errorf("%s: job %s: %s", c.name, st.Status, string(st.Error))
		default:
			fmt.Fprint(os.Stderr, ".")
		}
	}
}

// imageURLPart è un'immagine nella forma di OpenRouter (frame_images, input_references).
func imageURLPart(path string) (map[string]any, error) {
	u, err := imageDataURL(path)
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": "image_url", "image_url": map[string]string{"url": u}}, nil
}

// routerNegativeKey è il parametro passante del negative prompt per famiglia di
// modello (allowed_passthrough_parameters di /videos/models, 20/11/2026); "" se
// la famiglia non lo prevede.
func routerNegativeKey(model string) string {
	switch {
	case strings.HasPrefix(model, "google/veo"):
		return "negativePrompt"
	case strings.HasPrefix(model, "kwaivgi/kling"), strings.HasPrefix(model, "alibaba/wan"):
		return "negative_prompt"
	}
	return ""
}

// routerOptionSlugs sono i provider di OpenRouter che servono i modelli di un
// autore; senza voce vale l'autore stesso. OpenRouter inoltra solo le opzioni
// del provider instradato, le altre non arrivano a nessuno.
var routerOptionSlugs = map[string][]string{
	"google":    {"google-vertex"},
	"bytedance": {"seed", "byteplus"},
}

// routerOptions mette i parametri passanti in provider.options.<slug>; per
// google-vertex vanno annidati in "parameters".
func routerOptions(model string, params map[string]any) map[string]any {
	author, _, _ := strings.Cut(model, "/")
	slugs := routerOptionSlugs[author]
	if slugs == nil {
		slugs = []string{author}
	}
	opts := make(map[string]any, len(slugs))
	for _, s := range slugs {
		if s == "google-vertex" {
			opts[s] = map[string]any{"parameters": params}
		} else {
			opts[s] = params
		}
	}
	return opts
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

// jobTimeout è il tetto di attesa di un job video: 15 minuti se non indicato.
func jobTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return 15 * time.Minute
	}
	return d
}

// --- helper ---

func (c *Client) download(ctx context.Context, url string) ([]byte, error) {
	return c.get(ctx, url, false)
}

// downloadAuth è per le URL sull'host API del provider: mai usarla su CDN esterne.
func (c *Client) downloadAuth(ctx context.Context, url string) ([]byte, error) {
	return c.get(ctx, url, true)
}

func (c *Client) get(ctx context.Context, url string, auth bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: download HTTP %d", c.name, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func addFilePart(w *multipart.Writer, field, path string) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, filepath.Base(path)))
	h.Set("Content-Type", mimeForFile(path))
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = part.Write(data)
	return err
}

// imageDataURL è un'immagine in ingresso a un video come data URL: tipo
// riconosciuto dai byte ed entro i limiti di provider.ReadImage.
func imageDataURL(path string) (string, error) {
	mime, data, err := provider.ReadImage(path)
	if err != nil {
		return "", fmt.Errorf("input %s: %w", path, err)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func dataURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "data:" + mimeForFile(path) + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func sizeForAspect(aspect string) string {
	switch aspect {
	case "1:1":
		return "1024x1024"
	case "9:16", "3:4", "2:3":
		return "1024x1536"
	default: // 16:9, 4:3, 3:2 e simili
		return "1536x1024"
	}
}

func mimeForFile(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "application/octet-stream"
	}
}
