package openaicompat

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"gen-image-video-cli/internal/provider"
)

func TestRouterImagesNPiuRichieste(t *testing.T) {
	var hits int32
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nxx"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != "/images" {
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprintf(w, `{"data":[{"b64_json":%q}],"usage":{"cost":0.25}}`, png)
	}))
	defer srv.Close()
	c := New("openrouter", srv.URL, "k", Options{Router: true})
	res, err := c.GenerateImage(context.Background(), provider.ImageRequest{Model: "m", Prompt: "p", N: 3})
	if err != nil {
		t.Fatal(err)
	}
	if hits != 3 || len(res.Media) != 3 {
		t.Errorf("richieste=%d immagini=%d, attese 3/3", hits, len(res.Media))
	}
	if res.CostUSD != 0.75 {
		t.Errorf("costo=%v atteso 0.75", res.CostUSD)
	}
}
