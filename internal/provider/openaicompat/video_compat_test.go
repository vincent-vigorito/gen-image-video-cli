package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"gen-image-video-cli/internal/provider"
)

// serverCorpo: registra il corpo JSON della POST di creazione e fa fallire subito il polling.
func serverCorpo(t *testing.T, createPath, jobJSON string) (*httptest.Server, func() map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var got map[string]any
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == createPath {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			json.Unmarshal(b, &got)
			mu.Unlock()
			fmt.Fprint(w, fmt.Sprintf(jobJSON, srv.URL+"/poll"))
			return
		}
		http.Error(w, "vietato", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]any { mu.Lock(); defer mu.Unlock(); return got }
}

func pngTemp(t *testing.T) (path, url string) {
	p := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(p, []byte("IMG"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, "data:image/png;base64,SU1H"
}

// Caso 2: coi soli flag di oggi il corpo inviato resta quello di main.
func TestVideoCorpoInvariatoConFlagDiOggi(t *testing.T) {
	fastPoll(t)
	img, durl := pngTemp(t)

	t.Run("xai", func(t *testing.T) {
		srv, body := serverCorpo(t, "/videos/generations", `{"request_id":"r1","x":%q}`)
		c := New("xai", srv.URL, "k", Options{B64Param: true, Grok: true})
		c.GenerateVideo(context.Background(), provider.VideoRequest{
			Model: "m", Prompt: "p", Aspect: "16:9", Resolution: "720p", Duration: 5, Image: img,
		})
		want := map[string]any{
			"model": "m", "prompt": "p", "duration": float64(5), "aspect_ratio": "16:9",
			"resolution": "720p", "image": map[string]any{"url": durl},
		}
		if !reflect.DeepEqual(body(), want) {
			t.Errorf("corpo xai=%v\natteso %v", body(), want)
		}
	})

	t.Run("xai minimo", func(t *testing.T) {
		srv, body := serverCorpo(t, "/videos/generations", `{"request_id":"r1","x":%q}`)
		c := New("xai", srv.URL, "k", Options{B64Param: true, Grok: true})
		c.GenerateVideo(context.Background(), provider.VideoRequest{Model: "m", Prompt: "p"})
		want := map[string]any{"model": "m", "prompt": "p"}
		if !reflect.DeepEqual(body(), want) {
			t.Errorf("corpo xai=%v\natteso %v", body(), want)
		}
	})

	t.Run("openrouter", func(t *testing.T) {
		srv, body := serverCorpo(t, "/videos", `{"id":"j1","polling_url":%q}`)
		c := New("openrouter", srv.URL, "k", Options{Router: true})
		c.GenerateVideo(context.Background(), provider.VideoRequest{
			Model: "m", Prompt: "p", Aspect: "16:9", Resolution: "720p", Duration: 5, Image: img,
		})
		want := map[string]any{
			"model": "m", "prompt": "p", "duration": float64(5), "aspect_ratio": "16:9", "resolution": "720p",
			"frame_images": []any{map[string]any{
				"type": "image_url", "image_url": map[string]any{"url": durl}, "frame_type": "first_frame",
			}},
		}
		if !reflect.DeepEqual(body(), want) {
			t.Errorf("corpo openrouter=%v\natteso %v", body(), want)
		}
	})

	t.Run("openrouter minimo", func(t *testing.T) {
		srv, body := serverCorpo(t, "/videos", `{"id":"j1","polling_url":%q}`)
		c := New("openrouter", srv.URL, "k", Options{Router: true})
		c.GenerateVideo(context.Background(), provider.VideoRequest{Model: "m", Prompt: "p"})
		want := map[string]any{"model": "m", "prompt": "p"}
		if !reflect.DeepEqual(body(), want) {
			t.Errorf("corpo openrouter=%v\natteso %v", body(), want)
		}
	})
}
