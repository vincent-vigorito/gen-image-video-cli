package openaicompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
)

func TestRouterVideoCreazione500NonRipetuta(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := New("openrouter", srv.URL, "k", Options{Router: true})
	_, err := c.GenerateVideo(context.Background(), provider.VideoRequest{Model: "m", Prompt: "p"})
	if err == nil {
		t.Fatal("atteso errore")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("richieste di creazione=%d, attesa 1", n)
	}
	var se *httpx.StartError
	if !errors.As(err, &se) {
		t.Errorf("atteso StartError, ottenuto %T: %v", err, err)
	}
}
