package output

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
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
		f, err := createNew(dir, name, Ext(m.Mime))
		if err != nil {
			return nil, err
		}
		_, err = f.Write(m.Data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
		files = append(files, FileInfo{Path: f.Name(), Mime: m.Mime, Bytes: len(m.Data)})
	}
	return files, nil
}

// createNew crea in dir un file nuovo name+ext con O_EXCL: un file che c'è
// già, anche come link simbolico, non si sovrascrive e non si segue; al suo
// posto si prova name-2, name-3…
func createNew(dir, name, ext string) (*os.File, error) {
	for n := 1; ; n++ {
		base := name
		if n > 1 {
			base = fmt.Sprintf("%s-%d", name, n)
		}
		f, err := os.OpenFile(filepath.Join(dir, base+ext), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) && n < 100 {
			continue
		}
		return f, err
	}
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

// MP4Duration legge la durata in secondi (arrotondata al centesimo) dal box
// moov/mvhd di un file MP4; false se il file non è un MP4 leggibile.
func MP4Duration(data []byte) (float64, bool) {
	moov, ok := mp4Box(data, "moov")
	if !ok {
		return 0, false
	}
	mvhd, ok := mp4Box(moov, "mvhd")
	if !ok || len(mvhd) < 1 {
		return 0, false
	}
	var scale uint32
	var dur uint64
	switch mvhd[0] { // versione: 0 = campi a 32 bit, 1 = date e durata a 64 bit
	case 0:
		if len(mvhd) < 20 {
			return 0, false
		}
		scale, dur = binary.BigEndian.Uint32(mvhd[12:]), uint64(binary.BigEndian.Uint32(mvhd[16:]))
	case 1:
		if len(mvhd) < 32 {
			return 0, false
		}
		scale, dur = binary.BigEndian.Uint32(mvhd[20:]), binary.BigEndian.Uint64(mvhd[24:])
	default:
		return 0, false
	}
	if scale == 0 {
		return 0, false
	}
	return math.Round(float64(dur)/float64(scale)*100) / 100, true
}

// mp4Box restituisce il contenuto del primo box di tipo typ al livello di data.
func mp4Box(data []byte, typ string) ([]byte, bool) {
	for len(data) >= 8 {
		size, hdr := uint64(binary.BigEndian.Uint32(data)), uint64(8)
		name := string(data[4:8])
		switch size {
		case 0: // fino alla fine del file
			size = uint64(len(data))
		case 1: // dimensione a 64 bit dopo il tipo
			if len(data) < 16 {
				return nil, false
			}
			size, hdr = binary.BigEndian.Uint64(data[8:]), 16
		}
		if size < hdr || size > uint64(len(data)) {
			return nil, false
		}
		if name == typ {
			return data[hdr:size], true
		}
		data = data[size:]
	}
	return nil, false
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

// AppendLog appende una riga JSONL al registro delle generazioni.
func AppendLog(path string, v any) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(v)
}
