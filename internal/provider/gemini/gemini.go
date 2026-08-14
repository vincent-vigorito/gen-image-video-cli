package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gen-image-video-cli/internal/provider"
)

const baseURL = "https://generativelanguage.googleapis.com/v1beta"

type Client struct {
	APIKey string
	HTTP   *http.Client
}

func New(apiKey string) *Client {
	return &Client{APIKey: apiKey, HTTP: &http.Client{Timeout: 180 * time.Second}}
}

func (c *Client) Name() string { return "gemini" }

func (c *Client) doJSON(ctx context.Context, method, url string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("x-goog-api-key", c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gemini: HTTP %d: %s", resp.StatusCode, apiErrorMessage(data))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
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
	pageToken := ""
	for {
		url := baseURL + "/models?pageSize=1000"
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}
		var resp struct {
			Models []struct {
				Name                       string   `json:"name"`
				DisplayName                string   `json:"displayName"`
				SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.doJSON(ctx, http.MethodGet, url, nil, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Models {
			name := strings.TrimPrefix(m.Name, "models/")
			out = append(out, provider.ModelInfo{
				Name:        name,
				DisplayName: m.DisplayName,
				Kind:        classify(name, m.SupportedGenerationMethods),
			})
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func classify(name string, methods []string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "veo"):
		return "video"
	case strings.Contains(lower, "image"):
		return "image"
	}
	for _, m := range methods {
		if m == "predictLongRunning" {
			return "video"
		}
		if m == "predict" {
			return "image"
		}
	}
	return "other"
}

// --- immagini ---

func (c *Client) GenerateImage(ctx context.Context, req provider.ImageRequest) ([]provider.Media, error) {
	if strings.HasPrefix(req.Model, "imagen") {
		return c.imagenPredict(ctx, req)
	}
	return c.generateContentImages(ctx, req)
}

func (c *Client) imagenPredict(ctx context.Context, req provider.ImageRequest) ([]provider.Media, error) {
	if len(req.Inputs) > 0 {
		return nil, fmt.Errorf("i modelli Imagen non accettano immagini di input: usa un modello gemini-*-image")
	}
	n := req.N
	if n < 1 {
		n = 1
	}
	params := map[string]any{"sampleCount": n}
	if req.Aspect != "" {
		params["aspectRatio"] = req.Aspect
	}
	body := map[string]any{
		"instances":  []map[string]any{{"prompt": req.Prompt}},
		"parameters": params,
	}
	var resp struct {
		Predictions []struct {
			MimeType           string `json:"mimeType"`
			BytesBase64Encoded string `json:"bytesBase64Encoded"`
		} `json:"predictions"`
	}
	url := fmt.Sprintf("%s/models/%s:predict", baseURL, req.Model)
	if err := c.doJSON(ctx, http.MethodPost, url, body, &resp); err != nil {
		return nil, err
	}
	var media []provider.Media
	for _, p := range resp.Predictions {
		data, err := base64.StdEncoding.DecodeString(p.BytesBase64Encoded)
		if err != nil {
			return nil, err
		}
		mime := p.MimeType
		if mime == "" {
			mime = "image/png"
		}
		media = append(media, provider.Media{Mime: mime, Data: data})
	}
	if len(media) == 0 {
		return nil, fmt.Errorf("gemini: nessuna immagine nella risposta")
	}
	return media, nil
}

type gcPart struct {
	Text       string    `json:"text,omitempty"`
	InlineData *gcInline `json:"inlineData,omitempty"`
}

type gcInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

func (c *Client) generateContentImages(ctx context.Context, req provider.ImageRequest) ([]provider.Media, error) {
	parts := []gcPart{{Text: req.Prompt}}
	for _, path := range req.Inputs {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		parts = append(parts, gcPart{InlineData: &gcInline{
			MimeType: mimeForFile(path),
			Data:     base64.StdEncoding.EncodeToString(data),
		}})
	}
	genCfg := map[string]any{"responseModalities": []string{"TEXT", "IMAGE"}}
	if req.Aspect != "" {
		genCfg["imageConfig"] = map[string]any{"aspectRatio": req.Aspect}
	}
	body := map[string]any{
		"contents":         []map[string]any{{"parts": parts}},
		"generationConfig": genCfg,
	}
	url := fmt.Sprintf("%s/models/%s:generateContent", baseURL, req.Model)

	n := req.N
	if n < 1 {
		n = 1
	}
	var media []provider.Media
	// candidateCount non è supportato dai modelli immagine: una chiamata per sample
	for i := 0; i < n; i++ {
		var resp struct {
			Candidates []struct {
				Content struct {
					Parts []gcPart `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := c.doJSON(ctx, http.MethodPost, url, body, &resp); err != nil {
			return nil, err
		}
		got := false
		for _, cand := range resp.Candidates {
			for _, p := range cand.Content.Parts {
				if p.InlineData != nil {
					data, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
					if err != nil {
						return nil, err
					}
					media = append(media, provider.Media{Mime: p.InlineData.MimeType, Data: data})
					got = true
				} else if p.Text != "" {
					fmt.Fprintln(os.Stderr, strings.TrimSpace(p.Text))
				}
			}
		}
		if !got {
			return media, fmt.Errorf("gemini: il modello non ha restituito immagini (sample %d)", i+1)
		}
	}
	return media, nil
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

// --- video (Veo, job asincrono) ---

func (c *Client) GenerateVideo(ctx context.Context, req provider.VideoRequest) ([]provider.Media, error) {
	instance := map[string]any{"prompt": req.Prompt}
	if req.Image != "" {
		data, err := os.ReadFile(req.Image)
		if err != nil {
			return nil, err
		}
		instance["image"] = map[string]any{
			"bytesBase64Encoded": base64.StdEncoding.EncodeToString(data),
			"mimeType":           mimeForFile(req.Image),
		}
	}
	params := map[string]any{}
	if req.Aspect != "" {
		params["aspectRatio"] = req.Aspect
	}
	if req.Resolution != "" {
		params["resolution"] = req.Resolution
	}
	if req.Negative != "" {
		params["negativePrompt"] = req.Negative
	}
	if req.Duration > 0 {
		params["durationSeconds"] = req.Duration
	}
	body := map[string]any{"instances": []any{instance}}
	if len(params) > 0 {
		body["parameters"] = params
	}

	var op struct {
		Name string `json:"name"`
	}
	url := fmt.Sprintf("%s/models/%s:predictLongRunning", baseURL, req.Model)
	if err := c.doJSON(ctx, http.MethodPost, url, body, &op); err != nil {
		return nil, err
	}
	if op.Name == "" {
		return nil, fmt.Errorf("gemini: nessun operation name nella risposta")
	}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni 10s)\n", op.Name)

	deadline := time.Now().Add(15 * time.Minute)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("gemini: timeout dopo 15m sul job %s", op.Name)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
		}
		var st struct {
			Done  bool `json:"done"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Response json.RawMessage `json:"response"`
		}
		if err := c.doJSON(ctx, http.MethodGet, baseURL+"/"+op.Name, nil, &st); err != nil {
			return nil, err
		}
		if !st.Done {
			fmt.Fprint(os.Stderr, ".")
			continue
		}
		fmt.Fprintln(os.Stderr)
		if st.Error != nil {
			return nil, fmt.Errorf("gemini: job fallito: %s", st.Error.Message)
		}
		uris, err := videoURIs(st.Response)
		if err != nil {
			return nil, err
		}
		var media []provider.Media
		for _, uri := range uris {
			data, err := c.download(ctx, uri)
			if err != nil {
				return nil, err
			}
			media = append(media, provider.Media{Mime: "video/mp4", Data: data})
		}
		return media, nil
	}
}

func videoURIs(raw json.RawMessage) ([]string, error) {
	var resp struct {
		GenerateVideoResponse struct {
			GeneratedSamples []struct {
				Video struct {
					URI string `json:"uri"`
				} `json:"video"`
			} `json:"generatedSamples"`
			GeneratedVideos []struct {
				Video struct {
					URI string `json:"uri"`
				} `json:"video"`
			} `json:"generatedVideos"`
		} `json:"generateVideoResponse"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	var uris []string
	for _, s := range resp.GenerateVideoResponse.GeneratedSamples {
		if s.Video.URI != "" {
			uris = append(uris, s.Video.URI)
		}
	}
	for _, s := range resp.GenerateVideoResponse.GeneratedVideos {
		if s.Video.URI != "" {
			uris = append(uris, s.Video.URI)
		}
	}
	if len(uris) == 0 {
		return nil, fmt.Errorf("gemini: job completato ma nessun video nella risposta: %s", string(raw))
	}
	return uris, nil
}

func (c *Client) download(ctx context.Context, uri string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", c.APIKey)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("gemini: download HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return io.ReadAll(resp.Body)
}
