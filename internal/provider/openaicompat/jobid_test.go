package openaicompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"gen-image-video-cli/internal/provider"
)

// Un job riuscito riporta il job_id nel risultato (xAI e OpenRouter), per il manifest.
func TestVideoRiuscitoRiportaJobID(t *testing.T) {
	fastPoll(t)
	t.Run("openrouter", func(t *testing.T) {
		var srv *httptest.Server
		srv = serverVideo(t, func(n int32, w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q],"usage":{"cost":0.5}}`, srv.URL+"/content")
		})
		defer srv.Close()
		c := New("openrouter", srv.URL, "k", Options{Router: true})
		res, err := c.GenerateVideo(context.Background(), provider.VideoRequest{Model: "kwaivgi/kling-v3.0-pro", Prompt: "p"})
		if err != nil || res.JobID != "job-42" || res.CostUSD != 0.5 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
	t.Run("xai", func(t *testing.T) {
		// il video sta su una CDN https, scaricata senza chiave dal trasporto di prova
		cdn := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("MP4DATA")) }))
		defer cdn.Close()
		old := resultTransport
		resultTransport = cdn.Client().Transport
		defer func() { resultTransport = old }()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/videos/generations":
				fmt.Fprint(w, `{"request_id":"req-7"}`)
			case r.URL.Path == "/videos/req-7":
				fmt.Fprintf(w, `{"status":"done","video":{"url":%q}}`, cdn.URL+"/file.mp4")
			default:
				t.Errorf("richiesta inattesa %s %s", r.Method, r.URL.Path)
			}
		}))
		defer srv.Close()
		c := New("xai", srv.URL, "k", Options{B64Param: true, Grok: true})
		res, err := c.GenerateVideo(context.Background(), provider.VideoRequest{Model: "grok-imagine-video", Prompt: "p"})
		if err != nil || res.JobID != "req-7" || res.CostUSD != 0 {
			t.Fatalf("res=%+v err=%v", res, err)
		}
	})
}
