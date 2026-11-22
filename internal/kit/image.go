package kit

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"syscall"
)

// maxChunks è il tetto dei blocchi PNG letti prima dei dati dell'immagine.
const maxChunks = 4096

var errImage = errors.New("immagine non leggibile")

// Transparent dice se un'immagine ha un canale di trasparenza, dalla sola
// intestazione riconosciuta dai byte (non dall'estensione): PNG con alfa
// (tipi di colore 4 e 6) o con un blocco tRNS prima dei dati; WebP col
// segnale alfa (VP8X, VP8L); JPEG mai. Legge solo l'intestazione, senza
// decodificare l'immagine; l'apertura è senza blocco e solo su un file normale.
func Transparent(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false, errors.New("file non trovato o non leggibile")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return false, errors.New("non è un file normale")
	}
	head := make([]byte, 32)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case n >= 8 && string(head[:8]) == "\x89PNG\r\n\x1a\n":
		return pngAlpha(f, st.Size())
	case n >= 30 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return webpAlpha(head), nil
	case n >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF:
		return false, nil
	}
	return false, errors.New("non è un'immagine png, jpeg o webp")
}

// pngAlpha scorre i blocchi PNG fino ai dati (IDAT): alfa nel tipo di colore
// di IHDR o un blocco tRNS. Ogni lettura è controllata sulla dimensione del file.
func pngAlpha(f *os.File, size int64) (bool, error) {
	off := int64(8)
	hdr := make([]byte, 8)
	for range maxChunks {
		if off+12 > size {
			return false, errImage
		}
		if _, err := f.ReadAt(hdr, off); err != nil {
			return false, errImage
		}
		length := int64(binary.BigEndian.Uint32(hdr[:4]))
		if length > 1<<31-1 || off+12+length > size {
			return false, errImage
		}
		switch string(hdr[4:8]) {
		case "IHDR":
			if length < 13 {
				return false, errImage
			}
			ct := make([]byte, 1)
			if _, err := f.ReadAt(ct, off+8+9); err != nil {
				return false, errImage
			}
			if ct[0] == 4 || ct[0] == 6 {
				return true, nil
			}
		case "tRNS":
			return true, nil
		case "IDAT", "IEND":
			return false, nil
		}
		off += 12 + length
	}
	return false, errImage
}

// webpAlpha legge il segnale alfa dal primo blocco di un WebP: VP8X (bit 4
// delle opzioni), VP8L (bit alpha_is_used dell'intestazione); VP8 senza VP8X
// non ha trasparenza.
func webpAlpha(head []byte) bool {
	switch string(head[12:16]) {
	case "VP8X":
		return head[20]&0x10 != 0
	case "VP8L":
		if head[20] != 0x2f {
			return false
		}
		return binary.LittleEndian.Uint32(head[21:25])>>28&1 == 1
	}
	return false
}
