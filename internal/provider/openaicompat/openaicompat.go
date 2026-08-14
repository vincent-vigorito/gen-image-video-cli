package openaicompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
)

// Options distingue i dialetti dei provider OpenAI-compatible.
type Options struct {
	Router   bool // endpoint dedicati /images e /videos di OpenRouter
	Sora     bool // video via API /videos di OpenAI (Sora)
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
	return httpx.Retry(ctx, func() error {
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
			return &httpx.Error{Prefix: c.name, Status: resp.StatusCode, Msg: apiErrorMessage(data)}
		}
		if out != nil {
			return json.Unmarshal(data, out)
		}
		return nil
	})
}

func (c *Client) doJSON(ctx context.Context, method, url string, body, out any) error {
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
	return c.do(ctx, method, url, ct, payload, out)
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
		if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/models", nil, &resp); err != nil {
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
	if req.N > 1 {
		body["n"] = req.N
	}
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
	var resp imagesResponse
	if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/images", body, &resp); err != nil {
		return nil, err
	}
	media, err := c.mediaFromImages(ctx, resp)
	if err != nil {
		return nil, err
	}
	return &provider.Result{Media: media, CostUSD: resp.Usage.Cost}, nil
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
	case c.opts.Sora:
		return c.soraVideo(ctx, req)
	case c.opts.Grok:
		return c.grokVideo(ctx, req)
	default:
		return nil, fmt.Errorf("generazione video non supportata per %s", c.name)
	}
}

// grokVideo usa l'API asincrona /videos/generations di x.ai (grok-imagine-video).
func (c *Client) grokVideo(ctx context.Context, req provider.VideoRequest) (*provider.Result, error) {
	if req.Negative != "" {
		fmt.Fprintf(os.Stderr, "avviso: --negative non supportato da %s, ignorato\n", c.name)
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
		u, err := dataURL(req.Image)
		if err != nil {
			return nil, err
		}
		body["image"] = map[string]string{"url": u}
	}

	var job struct {
		RequestID string `json:"request_id"`
	}
	if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/videos/generations", body, &job); err != nil {
		return nil, err
	}
	if job.RequestID == "" {
		return nil, fmt.Errorf("%s: nessun request_id nella risposta", c.name)
	}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni 10s)\n", job.RequestID)

	deadline := time.Now().Add(15 * time.Minute)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: timeout dopo 15m sul job %s", c.name, job.RequestID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
		}
		var st struct {
			Status string `json:"status"`
			Video  struct {
				URL string `json:"url"`
			} `json:"video"`
			Error json.RawMessage `json:"error"`
		}
		if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/videos/"+job.RequestID, nil, &st); err != nil {
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
			return &provider.Result{Media: []provider.Media{{Mime: "video/mp4", Data: data}}}, nil
		case "failed", "error", "expired":
			fmt.Fprintln(os.Stderr)
			return nil, fmt.Errorf("%s: job %s: %s", c.name, st.Status, string(st.Error))
		default:
			fmt.Fprint(os.Stderr, ".")
		}
	}
}

func (c *Client) routerVideo(ctx context.Context, req provider.VideoRequest) (*provider.Result, error) {
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
	if req.Image != "" {
		u, err := dataURL(req.Image)
		if err != nil {
			return nil, err
		}
		body["frame_images"] = []map[string]any{{
			"type":       "image_url",
			"image_url":  map[string]string{"url": u},
			"frame_type": "first_frame",
		}}
	}

	var job struct {
		ID         string `json:"id"`
		PollingURL string `json:"polling_url"`
		Status     string `json:"status"`
	}
	if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/videos", body, &job); err != nil {
		return nil, err
	}
	if job.PollingURL == "" {
		return nil, fmt.Errorf("%s: nessuna polling_url nella risposta (job %q, status %q)", c.name, job.ID, job.Status)
	}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni 10s)\n", job.ID)

	deadline := time.Now().Add(15 * time.Minute)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: timeout dopo 15m sul job %s", c.name, job.ID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
		}
		var st struct {
			Status       string          `json:"status"`
			UnsignedURLs []string        `json:"unsigned_urls"`
			Error        json.RawMessage `json:"error"`
			Usage        struct {
				Cost float64 `json:"cost"`
			} `json:"usage"`
		}
		if err := c.doJSON(ctx, http.MethodGet, job.PollingURL, nil, &st); err != nil {
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
			return &provider.Result{Media: media, CostUSD: st.Usage.Cost}, nil
		case "failed", "cancelled", "error":
			fmt.Fprintln(os.Stderr)
			return nil, fmt.Errorf("%s: job %s: %s", c.name, st.Status, string(st.Error))
		default:
			fmt.Fprint(os.Stderr, ".")
		}
	}
}

// soraVideo usa l'API asincrona /videos di OpenAI (sora-2, sora-2-pro).
func (c *Client) soraVideo(ctx context.Context, req provider.VideoRequest) (*provider.Result, error) {
	size := "1280x720"
	if req.Aspect == "9:16" || req.Aspect == "3:4" {
		size = "720x1280"
	}
	if req.Resolution != "" && req.Resolution != "720p" {
		fmt.Fprintf(os.Stderr, "avviso: sora via API lavora a 720p (size %s), --resolution %s ignorata\n", size, req.Resolution)
	}
	if req.Negative != "" {
		fmt.Fprintf(os.Stderr, "avviso: --negative non supportato da sora, ignorato\n")
	}

	var job soraJob
	if req.Image == "" {
		body := map[string]any{"model": req.Model, "prompt": req.Prompt, "size": size}
		if req.Duration > 0 {
			body["seconds"] = strconv.Itoa(req.Duration)
		}
		if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/videos", body, &job); err != nil {
			return nil, err
		}
	} else {
		// image-to-video: multipart con input_reference (deve avere esattamente la size del video)
		var buf bytes.Buffer
		w := multipart.NewWriter(&buf)
		w.WriteField("model", req.Model)
		w.WriteField("prompt", req.Prompt)
		w.WriteField("size", size)
		if req.Duration > 0 {
			w.WriteField("seconds", strconv.Itoa(req.Duration))
		}
		if err := addFilePart(w, "input_reference", req.Image); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		if err := c.do(ctx, http.MethodPost, c.baseURL+"/videos", w.FormDataContentType(), buf.Bytes(), &job); err != nil {
			return nil, err
		}
	}
	if job.ID == "" {
		return nil, fmt.Errorf("%s: nessun id video nella risposta", c.name)
	}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni 10s)\n", job.ID)

	deadline := time.Now().Add(15 * time.Minute)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: timeout dopo 15m sul job %s", c.name, job.ID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
		}
		var st soraJob
		if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/videos/"+job.ID, nil, &st); err != nil {
			return nil, err
		}
		switch st.Status {
		case "completed":
			fmt.Fprintln(os.Stderr)
			data, err := c.downloadAuth(ctx, c.baseURL+"/videos/"+job.ID+"/content")
			if err != nil {
				return nil, err
			}
			return &provider.Result{Media: []provider.Media{{Mime: "video/mp4", Data: data}}}, nil
		case "failed":
			fmt.Fprintln(os.Stderr)
			msg := "errore non specificato"
			if st.Error != nil && st.Error.Message != "" {
				msg = st.Error.Message
			}
			return nil, fmt.Errorf("%s: job failed: %s", c.name, msg)
		default:
			fmt.Fprint(os.Stderr, ".")
		}
	}
}

type soraJob struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
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
