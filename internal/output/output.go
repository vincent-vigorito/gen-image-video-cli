package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gen-image-video-cli/internal/provider"
)

type FileInfo struct {
	Path  string `json:"path"`
	Mime  string `json:"mime"`
	Bytes int    `json:"bytes"`
}

// SaveAll scrive i media in dir come <slug>-<timestamp>[-<n>].<ext>.
func SaveAll(dir, slug string, media []provider.Media) ([]FileInfo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	ts := time.Now().Format("20060102-150405")
	var files []FileInfo
	for i, m := range media {
		name := slug + "-" + ts
		if len(media) > 1 {
			name = fmt.Sprintf("%s-%d", name, i+1)
		}
		path := filepath.Join(dir, name+Ext(m.Mime))
		if err := os.WriteFile(path, m.Data, 0o644); err != nil {
			return nil, err
		}
		files = append(files, FileInfo{Path: path, Mime: m.Mime, Bytes: len(m.Data)})
	}
	return files, nil
}

func Ext(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	default:
		if strings.HasPrefix(mime, "image/") {
			return ".png"
		}
		if strings.HasPrefix(mime, "video/") {
			return ".mp4"
		}
		return ".bin"
	}
}

// Slug deriva un nome file dal prompt (max 40 caratteri).
func Slug(s string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= 40 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "media"
	}
	return out
}

// PrintJSON stampa il manifest su stdout (il canale dati per gli agent).
func PrintJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
