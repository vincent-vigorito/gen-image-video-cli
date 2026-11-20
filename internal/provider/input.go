package provider

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Limiti delle immagini in ingresso ai video (file locali e download): tengono
// sotto controllo memoria, disco e corpo della richiesta.
const (
	MaxInputBytes = 20 << 20 // per immagine
	MaxInputTotal = 50 << 20 // somma delle immagini di un video
	MaxRefs       = 14       // --ref per video: il massimo dei modelli (xAI 1.5)
)

// ImageMime riconosce dai primi byte un'immagine supportata (png, jpeg, webp,
// gif); "" se non lo è.
func ImageMime(head []byte) string {
	switch ct := http.DetectContentType(head); ct {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return ct
	}
	return ""
}

// openImage apre un file regolare (anche dietro un symlink) entro MaxInputBytes.
func openImage(path string) (*os.File, int64, error) {
	if st, err := os.Stat(path); err != nil {
		return nil, 0, errors.New("file non trovato o non leggibile")
	} else if !st.Mode().IsRegular() {
		return nil, 0, errors.New("non è un file regolare")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, errors.New("file non trovato o non leggibile")
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, 0, errors.New("non è un file regolare")
	}
	if st.Size() > MaxInputBytes {
		f.Close()
		return nil, 0, fmt.Errorf("%d byte, oltre il limite di %d MiB per immagine", st.Size(), MaxInputBytes>>20)
	}
	return f, st.Size(), nil
}

var errNotImage = errors.New("non è un'immagine png, jpeg, webp o gif")

// CheckImage controlla un'immagine locale senza leggerla tutta: file regolare,
// entro MaxInputBytes, png/jpeg/webp/gif riconosciuta dai byte. Restituisce la
// dimensione.
func CheckImage(path string) (int64, error) {
	f, size, err := openImage(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	if ImageMime(head[:n]) == "" {
		return 0, errNotImage
	}
	return size, nil
}

// ReadImage legge un'immagine locale con gli stessi controlli di CheckImage e
// ne restituisce il tipo riconosciuto dai byte.
func ReadImage(path string) (mime string, data []byte, err error) {
	f, _, err := openImage(path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	if data, err = io.ReadAll(io.LimitReader(f, MaxInputBytes+1)); err != nil {
		return "", nil, err
	}
	if len(data) > MaxInputBytes {
		return "", nil, fmt.Errorf("oltre il limite di %d MiB per immagine", MaxInputBytes>>20)
	}
	if mime = ImageMime(data); mime == "" {
		return "", nil, errNotImage
	}
	return mime, data, nil
}
