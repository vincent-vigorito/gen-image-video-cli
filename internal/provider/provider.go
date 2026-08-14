package provider

import "context"

type ModelInfo struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"` // image | video | other
	DisplayName string         `json:"display_name,omitempty"`
	Pricing     map[string]any `json:"pricing,omitempty"` // raw del provider (per ora: openrouter)
}

type ImageRequest struct {
	Prompt string
	Model  string
	N      int
	Aspect string
	Seed   int      // 0 = non impostato
	Inputs []string // path di immagini di riferimento (editing/reference)
}

type VideoRequest struct {
	Prompt     string
	Model      string
	Aspect     string
	Resolution string
	Negative   string
	Image      string // frame iniziale opzionale
	Duration   int    // secondi, 0 = default del modello
}

type Media struct {
	Mime string
	Data []byte
}

type Result struct {
	Media   []Media
	CostUSD float64 // 0 = costo non riportato dal provider
}

type Provider interface {
	Name() string
	Models(ctx context.Context) ([]ModelInfo, error)
	GenerateImage(ctx context.Context, req ImageRequest) (*Result, error)
	GenerateVideo(ctx context.Context, req VideoRequest) (*Result, error)
}
