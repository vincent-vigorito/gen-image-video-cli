package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"
	"syscall"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/provider"
)

// Ingressi dei comandi sulle clip (giv extend, edit, upscale, avatar): file
// controllati dai byte prima di chiave e chiamate (codice 2), misure lette con
// ffprobe se c'è.

// mediaTool legge le misure dei file in ingresso (i test lo puntano a un
// ffprobe finto o assente).
var mediaTool = montage.Tool{}

// checkClipVideo controlla --video: file locale mp4 entro MaxVideoBytes, o URL
// https passato così com'è al provider (non scaricato da giv).
func checkClipVideo(p string) error {
	if isURL(p) {
		return checkClipURL("--video", p, "https")
	}
	if _, err := provider.CheckVideo(p); err != nil {
		return usageError("--video %s: %v", p, err)
	}
	return nil
}

// checkClipAudio controlla --audio: solo file locale wav o mp3 entro MaxAudioBytes.
func checkClipAudio(p string) error {
	if isURL(p) {
		return usageError("--audio %s: serve un file locale wav o mp3", redactURL(p))
	}
	if _, _, err := provider.CheckAudio(p); err != nil {
		return usageError("--audio %s: %v", p, err)
	}
	return nil
}

// checkClipImage controlla --image locale come giv video; di un URL qui
// sintassi, schema e host, il contenuto dopo il download.
func checkClipImage(p string) error {
	if isURL(p) {
		return checkClipURL("--image", p, "http", "https")
	}
	if _, err := provider.CheckImage(p); err != nil {
		return usageError("--image %s: %v", p, err)
	}
	return nil
}

// checkClipURL controlla un URL d'ingresso prima di chiave e chiamate:
// sintassi, schema fra quelli ammessi e host presente. Negli errori l'URL è
// ridotto a schema e host.
func checkClipURL(flag, p string, schemes ...string) error {
	u, err := url.Parse(p)
	if err != nil || u.Opaque != "" || u.Hostname() == "" {
		return usageError("%s %s: URL non valido (serve schema e host)", flag, redactURL(p))
	}
	if !slices.Contains(schemes, u.Scheme) {
		return usageError("%s %s: serve un URL %s", flag, redactURL(p), strings.Join(schemes, " o "))
	}
	return nil
}

// clipHash è l'impronta breve di un ingresso dei comandi sulle clip: file
// regolare aperto senza blocco e letto al massimo per limit byte; "" se non è
// regolare, non si legge o supera il limite.
func clipHash(path string, limit int64) string {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return ""
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, limit+1))
	if err != nil || n > limit {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// probeClip legge con ffprobe le misure dei file locali di req (video e
// audio). Senza ffprobe o con un URL le misure restano a zero, con un avviso;
// un file che ffprobe non legge, o senza la traccia attesa, è un rifiuto.
func probeClip(req *provider.ClipRequest) ([]string, error) {
	var warns []string
	probe := func(flag, path string) (*montage.Info, error) {
		info, err := mediaTool.Probe(context.Background(), path)
		if errors.Is(err, montage.ErrMissingTool) {
			warns = append(warns, fmt.Sprintf("ffprobe non trovato: misure di %s %s non lette", flag, path))
			return nil, nil
		}
		if err != nil {
			return nil, usageError("%s %s: non leggibile con ffprobe (%v)", flag, path, err)
		}
		return info, nil
	}
	if req.Video != "" {
		if isURL(req.Video) {
			warns = append(warns, "--video da URL: misure del sorgente non lette")
		} else {
			info, err := probe("--video", req.Video)
			if err != nil {
				return nil, err
			}
			if info != nil {
				if info.Video == nil {
					return nil, usageError("--video %s: nessuna traccia video", req.Video)
				}
				req.Source.Duration, req.Source.Width, req.Source.Height = info.Duration, info.Video.Width, info.Video.Height
			}
		}
	}
	if req.Audio != "" {
		info, err := probe("--audio", req.Audio)
		if err != nil {
			return nil, err
		}
		if info != nil {
			if info.Audio == nil {
				return nil, usageError("--audio %s: nessuna traccia audio", req.Audio)
			}
			req.Source.AudioDuration = info.Duration
		}
	}
	return warns, nil
}
