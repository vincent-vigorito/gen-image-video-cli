package provider

import "context"

type ModelInfo struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"` // image | video | other
	DisplayName string `json:"display_name,omitempty"`
}

type ImageRequest struct {
	Prompt string
	Model  string
	N      int
	Aspect string
	Inputs []string // path di immagini di riferimento (editing/reference)
}

type VideoRequest struct {
	Prompt     string
	Model      string
	Aspect     string
	Resolution string
	Negative   string
	Image      string // frame iniziale opzionale
}

type Media struct {
	Mime string
	Data []byte
}

type Provider interface {
	Name() string
	Models(ctx context.Context) ([]ModelInfo, error)
	GenerateImage(ctx context.Context, req ImageRequest) ([]Media, error)
	GenerateVideo(ctx context.Context, req VideoRequest) ([]Media, error)
}
