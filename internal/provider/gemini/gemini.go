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

	"gen-image-video-cli/internal/httpx"
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
	return retry(ctx, func() error {
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("x-goog-api-key", c.APIKey)
		if payload != nil {
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
			return &httpx.Error{Prefix: "gemini", Status: resp.StatusCode, Msg: apiErrorMessage(data),
				RetryAfter: httpx.ParseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		if out != nil {
			return json.Unmarshal(data, out)
		}
		return nil
	})
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

func (c *Client) GenerateImage(ctx context.Context, req provider.ImageRequest) (*provider.Result, error) {
	media, err := c.generateContentImages(ctx, req)
	if err != nil {
		return nil, err
	}
	return &provider.Result{Media: media}, nil
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
	if req.Seed != 0 {
		genCfg["seed"] = req.Seed
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

// --- video ---

// GenerateVideo rifiuta sempre: Veo sulla Gemini API e' spento dal 22/10/2026 e
// il sostituto (Gemini Omni) usa un'altra API, non implementata. Nessuna chiamata.
func (c *Client) GenerateVideo(ctx context.Context, req provider.VideoRequest) (*provider.Result, error) {
	return nil, fmt.Errorf("gemini: generazione video non disponibile (Veo spento sulla Gemini API): usa --provider openrouter")
}
