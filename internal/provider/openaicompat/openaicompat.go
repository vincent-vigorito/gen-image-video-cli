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

	"gen-image-video-cli/internal/provider"
)

// Options distingue i dialetti dei provider OpenAI-compatible.
type Options struct {
	Chat     bool // immagini via chat completions con modalities (OpenRouter)
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

func (c *Client) do(ctx context.Context, method, url, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
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
		return fmt.Errorf("%s: HTTP %d: %s", c.name, resp.StatusCode, apiErrorMessage(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, url string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(data)
	}
	ct := ""
	if body != nil {
		ct = "application/json"
	}
	return c.do(ctx, method, url, ct, rdr, out)
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
	if c.opts.Chat {
		var resp struct {
			Data []struct {
				ID           string `json:"id"`
				Name         string `json:"name"`
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
			out = append(out, provider.ModelInfo{Name: m.ID, DisplayName: m.Name, Kind: kind})
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

func (c *Client) GenerateImage(ctx context.Context, req provider.ImageRequest) ([]provider.Media, error) {
	if c.opts.Chat {
		return c.chatImages(ctx, req)
	}
	if len(req.Inputs) > 0 {
		return c.imagesEdits(ctx, req)
	}
	return c.imagesGenerations(ctx, req)
}

func (c *Client) imagesGenerations(ctx context.Context, req provider.ImageRequest) ([]provider.Media, error) {
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
	if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/images/generations", body, &resp); err != nil {
		return nil, err
	}
	return c.mediaFromImages(ctx, resp)
}

func (c *Client) imagesEdits(ctx context.Context, req provider.ImageRequest) ([]provider.Media, error) {
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
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition",
			fmt.Sprintf(`form-data; name="image[]"; filename="%s"`, filepath.Base(path)))
		h.Set("Content-Type", mimeForFile(path))
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	var resp imagesResponse
	if err := c.do(ctx, http.MethodPost, c.baseURL+"/images/edits", w.FormDataContentType(), &buf, &resp); err != nil {
		return nil, err
	}
	return c.mediaFromImages(ctx, resp)
}

type imagesResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
		URL     string `json:"url"`
	} `json:"data"`
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
		media = append(media, provider.Media{Mime: http.DetectContentType(data), Data: data})
	}
	if len(media) == 0 {
		return nil, fmt.Errorf("%s: nessuna immagine nella risposta", c.name)
	}
	return media, nil
}

func (c *Client) chatImages(ctx context.Context, req provider.ImageRequest) ([]provider.Media, error) {
	if req.Aspect != "" {
		fmt.Fprintf(os.Stderr, "avviso: --aspect non supportato da %s, ignorato\n", c.name)
	}
	content := []map[string]any{{"type": "text", "text": req.Prompt}}
	for _, path := range req.Inputs {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		content = append(content, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:" + mimeForFile(path) + ";base64," + base64.StdEncoding.EncodeToString(data),
			},
		})
	}
	body := map[string]any{
		"model":      req.Model,
		"messages":   []map[string]any{{"role": "user", "content": content}},
		"modalities": []string{"image", "text"},
	}

	n := req.N
	if n < 1 {
		n = 1
	}
	var media []provider.Media
	// una chiamata per sample, come per gemini generateContent
	for i := 0; i < n; i++ {
		var resp struct {
			Choices []struct {
				Message struct {
					Content json.RawMessage `json:"content"`
					Images  []struct {
						ImageURL struct {
							URL string `json:"url"`
						} `json:"image_url"`
					} `json:"images"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/chat/completions", body, &resp); err != nil {
			return nil, err
		}
		got := false
		for _, ch := range resp.Choices {
			var text string
			if json.Unmarshal(ch.Message.Content, &text) == nil && strings.TrimSpace(text) != "" {
				fmt.Fprintln(os.Stderr, strings.TrimSpace(text))
			}
			for _, img := range ch.Message.Images {
				m, err := mediaFromDataURL(img.ImageURL.URL)
				if err != nil {
					return nil, err
				}
				media = append(media, m)
				got = true
			}
		}
		if !got {
			return media, fmt.Errorf("%s: il modello non ha restituito immagini (sample %d)", c.name, i+1)
		}
	}
	return media, nil
}

func mediaFromDataURL(u string) (provider.Media, error) {
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return provider.Media{}, fmt.Errorf("URL immagine inatteso (non data:): %.60s", u)
	}
	meta, b64, ok := strings.Cut(rest, ",")
	if !ok {
		return provider.Media{}, fmt.Errorf("data URL malformato")
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return provider.Media{}, err
	}
	mime := strings.TrimSuffix(meta, ";base64")
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return provider.Media{Mime: mime, Data: data}, nil
}

func (c *Client) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
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

// --- video ---

func (c *Client) GenerateVideo(ctx context.Context, req provider.VideoRequest) ([]provider.Media, error) {
	return nil, fmt.Errorf("generazione video non ancora supportata per %s (in roadmap: Sora via /videos)", c.name)
}
