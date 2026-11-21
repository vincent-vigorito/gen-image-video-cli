package openaicompat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gen-image-video-cli/internal/provider"
)

// F2-T2: FetchVideo riprende un job già partito, senza POST di avvio.
func fetchServer(t *testing.T, poll func(n int32, w http.ResponseWriter, srvURL string)) (*httptest.Server, *int32) {
	var posts, n int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			atomic.AddInt32(&posts, 1)
		case r.URL.Path == "/videos/job-42":
			poll(atomic.AddInt32(&n, 1), w, srv.URL)
		case r.URL.Path == "/content":
			w.Write([]byte("MP4DATA"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &posts
}

func TestFetchVideoCompletatoZeroAvvii(t *testing.T) {
	fastPoll(t)
	srv, posts := fetchServer(t, func(n int32, w http.ResponseWriter, u string) {
		fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q],"usage":{"cost":0.5}}`, u+"/content")
	})
	c := New("openrouter", srv.URL, "k", Options{Router: true})
	res, err := c.FetchVideo(context.Background(), provider.Job{ID: "job-42", PollingURL: srv.URL + "/videos/job-42"}, time.Minute)
	if err != nil || len(res.Media) != 1 || string(res.Media[0].Data) != "MP4DATA" || res.JobID != "job-42" || res.CostUSD != 0.5 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if *posts != 0 {
		t.Errorf("POST=%d, attese 0", *posts)
	}
}

func TestFetchVideoInCorsoAttendePoiScarica(t *testing.T) {
	fastPoll(t)
	srv, posts := fetchServer(t, func(n int32, w http.ResponseWriter, u string) {
		if n < 3 {
			fmt.Fprint(w, `{"status":"in_progress"}`)
			return
		}
		fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q]}`, u+"/content")
	})
	c := New("openrouter", srv.URL, "k", Options{Router: true})
	res, err := c.FetchVideo(context.Background(), provider.Job{ID: "job-42", PollingURL: srv.URL + "/videos/job-42"}, time.Minute)
	if err != nil || string(res.Media[0].Data) != "MP4DATA" || *posts != 0 {
		t.Fatalf("res=%+v err=%v posts=%d", res, err, *posts)
	}
}

func TestFetchVideoFallitoPortaId(t *testing.T) {
	fastPoll(t)
	srv, _ := fetchServer(t, func(n int32, w http.ResponseWriter, u string) {
		fmt.Fprint(w, `{"status":"failed","error":"boom"}`)
	})
	c := New("openrouter", srv.URL, "k", Options{Router: true})
	_, err := c.FetchVideo(context.Background(), provider.Job{ID: "job-42", PollingURL: srv.URL + "/videos/job-42"}, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "job-42") {
		t.Fatalf("atteso errore con l'id, ottenuto %v", err)
	}
}

func TestFetchVideoHostDiversoRifiutatoSenzaRichieste(t *testing.T) {
	var hits int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer other.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer api.Close()
	c := New("openrouter", api.URL, "k", Options{Router: true})
	job := provider.Job{ID: "job-42", PollingURL: other.URL + "/poll"}
	if err := c.CheckJob(job); err == nil {
		t.Error("CheckJob doveva rifiutare")
	}
	if _, err := c.FetchVideo(context.Background(), job, time.Minute); err == nil {
		t.Error("FetchVideo doveva rifiutare")
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("richieste=%d, attese 0", n)
	}
}
