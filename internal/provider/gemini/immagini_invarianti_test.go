package gemini

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gen-image-video-cli/internal/provider"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// O-T8 caso 7: le immagini Gemini producono lo stesso corpo di richiesta di
// prima (valore atteso fissato sul codice di main 06192eb).
func TestImmaginiCorpoInvariato(t *testing.T) {
	ref := filepath.Join(t.TempDir(), "r.png")
	os.WriteFile(ref, []byte("PNGDATA"), 0o644)
	var gotURL, gotBody, gotKey string
	c := New("k-img")
	c.HTTP = &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		gotURL, gotBody, gotKey = r.URL.String(), string(b), r.Header.Get("x-goog-api-key")
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"UE5H"}}]}}]}`))}, nil
	})}
	res, err := c.GenerateImage(context.Background(), provider.ImageRequest{Prompt: "un gatto", Model: "gemini-3.1-flash-image", N: 1, Aspect: "1:1", Seed: 7, Inputs: []string{ref}})
	if err != nil || len(res.Media) != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if gotURL != "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.1-flash-image:generateContent" {
		t.Errorf("url %s", gotURL)
	}
	want := `{"contents":[{"parts":[{"text":"un gatto"},{"inlineData":{"mimeType":"image/png","data":"UE5HREFUQQ=="}}]}],"generationConfig":{"imageConfig":{"aspectRatio":"1:1"},"responseModalities":["TEXT","IMAGE"],"seed":7}}`
	if gotBody != want {
		t.Errorf("corpo cambiato:\n got %s\nwant %s", gotBody, want)
	}
	if gotKey != "k-img" {
		t.Errorf("chiave non inviata come prima: %q", gotKey)
	}
}
