package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
)

// uploadLifecycle è la scadenza dei file caricati: un'ora, sempre esplicita
// (la durata di default dello storage di fal non è chiara).
const uploadLifecycle = `{"expiration_duration_seconds":3600}`

// upload carica un'immagine locale sullo storage di fal in due passi (put): initiate
// su rest.fal.ai con la chiave e la scadenza, poi PUT dei byte sull'indirizzo
// firmato senza chiave. Restituisce file_url, pubblico per chi lo conosce
// fino alla scadenza. Il nome inviato è neutro (giv-<n>.<ext>).
func (c *Client) upload(ctx context.Context, path string, n int) (string, error) {
	mime, data, err := provider.ReadImage(path)
	if err != nil {
		return "", fmt.Errorf("input %s: %w", path, err)
	}
	return c.put(ctx, path, mime, data, n)
}

// uploadAudio carica un audio locale (wav o mp3, regole di provider.OpenAudio)
// come upload, letto al massimo per la dimensione controllata.
func (c *Client) uploadAudio(ctx context.Context, path string, n int) (string, error) {
	f, mime, size, err := provider.OpenAudio(path)
	if err != nil {
		return "", fmt.Errorf("audio %s: %w", path, err)
	}
	data, err := io.ReadAll(io.LimitReader(f, size))
	f.Close()
	if err != nil {
		return "", fmt.Errorf("audio %s: file non leggibile", path)
	}
	return c.put(ctx, path, mime, data, n)
}

// uploadVideo carica una clip locale (mp4, regole di provider.OpenVideo) come
// upload, letta al massimo per la dimensione controllata.
func (c *Client) uploadVideo(ctx context.Context, path string, n int) (string, error) {
	f, mime, size, err := provider.OpenVideo(path)
	if err != nil {
		return "", fmt.Errorf("video %s: %w", path, err)
	}
	data, err := io.ReadAll(io.LimitReader(f, size))
	f.Close()
	if err != nil {
		return "", fmt.Errorf("video %s: file non leggibile", path)
	}
	return c.put(ctx, path, mime, data, n)
}

// put carica data (di tipo mime, letto da path) in due passi e restituisce
// file_url. Un solo tentativo per passo, nessun redirect seguito: un errore
// (anche un 5xx dopo la ricezione o un 3xx) ferma il comando prima di ogni
// avvio. I byte vanno solo a un upload_url dello storage di fal.
func (c *Client) put(ctx context.Context, path, mime string, data []byte, n int) (string, error) {
	body, _ := json.Marshal(map[string]string{"file_name": fmt.Sprintf("giv-%d%s", n, uploadExt[mime]), "content_type": mime})
	header := http.Header{
		"X-Fal-Object-Lifecycle":            {uploadLifecycle},
		"X-Fal-Object-Lifecycle-Preference": {uploadLifecycle},
	}
	var init struct {
		UploadURL string `json:"upload_url"`
		FileURL   string `json:"file_url"`
	}
	if err := c.do(ctx, httpx.NoRetry, http.MethodPost, c.rest.String()+"/storage/upload/initiate?storage_type=fal-cdn-v3", body, header, &init); err != nil {
		return "", fmt.Errorf("caricamento di %s su fal: %w", path, err)
	}
	if err := falStorage(init.UploadURL); err != nil {
		return "", fmt.Errorf("caricamento di %s su fal: upload_url: %w", path, err)
	}
	if err := checkExternal(init.FileURL); err != nil {
		return "", fmt.Errorf("caricamento di %s su fal: file_url: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, init.UploadURL, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("caricamento di %s su fal: upload_url %s non valido", path, redact(init.UploadURL))
	}
	req.Header.Set("Content-Type", mime)
	resp, err := c.ext.Do(req)
	if err != nil {
		return "", fmt.Errorf("caricamento di %s su fal: PUT su %s: %w", path, redact(init.UploadURL), netError(err))
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("caricamento di %s su fal: %w", path,
			&httpx.Error{Prefix: name, Status: resp.StatusCode, Msg: "PUT su " + redact(init.UploadURL) + " rifiutato"})
	}
	return init.FileURL, nil
}

var uploadExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif",
	"audio/wav": ".wav", "audio/mpeg": ".mp3", "video/mp4": ".mp4"}
