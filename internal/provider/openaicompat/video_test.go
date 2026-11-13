package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func fastPoll(t *testing.T) {
	old := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = old })
}

// serverVideo: POST /videos crea il job; il GET /poll segue i comportamenti dati.
func serverVideo(t *testing.T, poll func(n int32, w http.ResponseWriter, r *http.Request)) *httptest.Server {
	var n int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/videos":
			fmt.Fprintf(w, `{"id":"job-42","polling_url":%q,"status":"pending"}`, srv.URL+"/poll")
		case r.URL.Path == "/poll":
			poll(atomic.AddInt32(&n, 1), w, r)
		case r.URL.Path == "/content":
			w.Write([]byte("MP4DATA"))
		default:
			t.Errorf("richiesta inattesa %s %s", r.Method, r.URL.Path)
		}
	}))
	return srv
}

func TestRouterVideoAttesaSopravvive429ERete(t *testing.T) {
	fastPoll(t)
	var srv *httptest.Server
	srv = serverVideo(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		switch n {
		case 1:
			http.Error(w, "slow down", http.StatusTooManyRequests)
		case 2:
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			conn.Close()
		default:
			fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q],"usage":{"cost":0.5}}`, srv.URL+"/content")
		}
	})
	defer srv.Close()
	c := New("openrouter", srv.URL, "k", Options{Router: true})
	res, err := c.GenerateVideo(context.Background(), provider.VideoRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Media) != 1 || string(res.Media[0].Data) != "MP4DATA" || res.CostUSD != 0.5 {
		t.Errorf("risultato inatteso: %+v", res)
	}
}

func TestRouterVideoPollingDefinitivoPortaIdJob(t *testing.T) {
	fastPoll(t)
	srv := serverVideo(t, func(n int32, w http.ResponseWriter, r *http.Request) {
		http.Error(w, "vietato", http.StatusForbidden)
	})
	defer srv.Close()
	c := New("openrouter", srv.URL, "k", Options{Router: true})
	_, err := c.GenerateVideo(context.Background(), provider.VideoRequest{Model: "m", Prompt: "p"})
	if err == nil || !strings.Contains(err.Error(), "job-42") {
		t.Fatalf("atteso errore con l'id del job, ottenuto %v", err)
	}
}
