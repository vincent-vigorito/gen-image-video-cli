package provider

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"syscall"
)

// Limiti delle immagini in ingresso ai video (file locali e download): tengono
// sotto controllo memoria, disco e corpo della richiesta.
const (
	MaxInputBytes = 20 << 20 // per immagine
	MaxInputTotal = 50 << 20 // somma delle immagini di un video
	MaxRefs       = 14       // --ref per video: il massimo dei modelli (xAI 1.5)

	// ingressi delle operazioni sulle clip (giv extend, edit, upscale, avatar)
	MaxVideoBytes = 50 << 20 // clip sorgente: tetto di POST /v1/files di xAI e dell'edit di OpenRouter
	MaxAudioBytes = 25 << 20 // voce dell'avatar
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
	return openRegular(path, MaxInputBytes, "immagine")
}

// openRegular apre un file regolare (anche dietro un symlink) entro limit byte.
// L'apertura è senza blocco (una FIFO o un device non fermano la CLI) e il tipo
// si controlla sul descrittore aperto, non sul path.
func openRegular(path string, limit int64, what string) (*os.File, int64, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, errors.New("file non trovato o non leggibile")
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, 0, errors.New("non è un file regolare")
	}
	if st.Size() > limit {
		f.Close()
		return nil, 0, fmt.Errorf("%d byte, oltre il limite di %d MiB per %s", st.Size(), limit>>20, what)
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

// VideoMime riconosce dai primi byte una clip mp4 (box ftyp con un marchio
// mp4, la regola di net/http); "" se non lo è.
func VideoMime(head []byte) string {
	if http.DetectContentType(head) == "video/mp4" {
		return "video/mp4"
	}
	return ""
}

// AudioMime riconosce dai primi byte un audio wav (RIFF…WAVE) o mp3 (tag ID3
// o intestazione di un frame MPEG layer III); "" se non lo è.
func AudioMime(head []byte) string {
	switch {
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WAVE":
		return "audio/wav"
	case len(head) >= 3 && string(head[:3]) == "ID3",
		len(head) >= 2 && head[0] == 0xFF && head[1]&0xE6 == 0xE2:
		return "audio/mpeg"
	}
	return ""
}

var (
	errNotVideo = errors.New("non è un video mp4")
	errNotAudio = errors.New("non è un audio wav o mp3")
)

// OpenVideo apre una clip locale: file regolare, entro MaxVideoBytes, mp4
// riconosciuto dai byte. Il file torna riavvolto all'inizio e va chiuso da chi
// lo usa; per l'invio si leggono al massimo size byte (il file può crescere
// dopo il controllo).
func OpenVideo(path string) (f *os.File, mime string, size int64, err error) {
	return openMedia(path, MaxVideoBytes, "video", VideoMime, errNotVideo)
}

// OpenAudio apre un audio locale (wav o mp3, entro MaxAudioBytes) come OpenVideo.
func OpenAudio(path string) (f *os.File, mime string, size int64, err error) {
	return openMedia(path, MaxAudioBytes, "audio", AudioMime, errNotAudio)
}

// CheckVideo controlla una clip locale senza leggerla tutta (regole di
// OpenVideo) e ne restituisce la dimensione.
func CheckVideo(path string) (int64, error) {
	f, _, size, err := OpenVideo(path)
	if err != nil {
		return 0, err
	}
	f.Close()
	return size, nil
}

// CheckAudio controlla un audio locale (regole di OpenAudio) e ne restituisce
// tipo e dimensione.
func CheckAudio(path string) (string, int64, error) {
	f, mime, size, err := OpenAudio(path)
	if err != nil {
		return "", 0, err
	}
	f.Close()
	return mime, size, nil
}

func openMedia(path string, limit int64, what string, sniff func([]byte) string, notOK error) (*os.File, string, int64, error) {
	f, size, err := openRegular(path, limit, what)
	if err != nil {
		return nil, "", 0, err
	}
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	mime := sniff(head[:n])
	if mime == "" {
		f.Close()
		return nil, "", 0, notOK
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, "", 0, errors.New("file non leggibile")
	}
	return f, mime, size, nil
}
