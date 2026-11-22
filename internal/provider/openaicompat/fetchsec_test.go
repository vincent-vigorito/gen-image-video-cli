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

	"gen-image-video-cli/internal/provider"
)

// F2-T2b: M1 redirect, M2 indirizzo di attesa, B1 tetto di tempo.

func TestFetchRedirectPollingFuoriOrigineRifiutatoDownloadSenzaBearer(t *testing.T) {
	fastPoll(t)
	var otherAuth, otherHits int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&otherHits, 1)
		if r.Header.Get("Authorization") != "" {
			atomic.AddInt32(&otherAuth, 1)
		}
		if r.URL.Path == "/file" {
			w.Write([]byte("MP4DATA"))
			return
		}
		fmt.Fprintf(w, `{"status":"completed","unsigned_urls":["https://%s/file"]}`, r.Host)
	}))
	defer other.Close()
	oldRT := resultTransport
	resultTransport = other.Client().Transport
	defer func() { resultTransport = oldRT }()
	var apiAuthSame int32
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/videos/job-42": // attesa: redirect fuori origine
			http.Redirect(w, r, other.URL+"/x", http.StatusFound)
		case "/videos/job-44": // attesa ok, scaricamento con redirect fuori origine
			fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q]}`, api.URL+"/content")
		case "/videos/job-43": // attesa: redirect sulla stessa origine
			http.Redirect(w, r, "/videos/same", http.StatusFound)
		case "/videos/same":
			if r.Header.Get("Authorization") == "Bearer k" {
				atomic.AddInt32(&apiAuthSame, 1)
			}
			fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q]}`, api.URL+"/content")
		case "/content": // scaricamento: redirect fuori origine
			http.Redirect(w, r, other.URL+"/file", http.StatusFound)
		}
	}))
	defer api.Close()
	c := New("openrouter", api.URL, "k", Options{Router: true})

	// polling: redirect fuori origine = errore 3xx, nessuna richiesta esterna
	_, err := c.FetchVideo(context.Background(), provider.Job{ID: "job-42", PollingURL: api.URL + "/videos/job-42"}, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect del polling fuori origine: atteso errore 3xx, err=%v", err)
	}
	if errors.Is(err, provider.ErrJobFailed) {
		t.Errorf("il redirect non e' un job fallito: %v", err)
	}
	if n := atomic.LoadInt32(&otherHits); n != 0 {
		t.Fatalf("il polling ha seguito il redirect fuori origine (%d richieste)", n)
	}
	// scaricamento: il redirect esterno si segue, senza Authorization
	if _, err := c.FetchVideo(context.Background(), provider.Job{ID: "job-44", PollingURL: api.URL + "/videos/job-44"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&otherHits) == 0 {
		t.Fatal("il redirect del download non e' stato seguito: test inutile")
	}
	if n := atomic.LoadInt32(&otherAuth); n != 0 {
		t.Errorf("Authorization arrivata fuori origine %d volte", n)
	}
	// stessa origine: il redirect si segue e la chiave resta
	if _, err := c.FetchVideo(context.Background(), provider.Job{ID: "job-43", PollingURL: api.URL + "/videos/job-43"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&apiAuthSame); n != 1 {
		t.Errorf("Authorization sulla stessa origine=%d, attesa 1", n)
	}
}

func TestCheckJobIndirizzoDiversoDalJob(t *testing.T) {
	var hits int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer api.Close()
	c := New("openrouter", api.URL, "k", Options{Router: true})
	ok := api.URL + "/videos/job-42"
	if err := c.CheckJob(provider.Job{ID: "job-42", PollingURL: ok}); err != nil {
		t.Fatalf("indirizzo valido rifiutato: %v", err)
	}
	other := strings.Replace(api.URL, "127.0.0.1", "127.0.0.1", 1)
	for name, u := range map[string]string{
		"credits":        api.URL + "/credits",
		"altro id":       api.URL + "/videos/altro",
		"query":          ok + "?x=1",
		"traversal":      api.URL + "/videos/../credits",
		"altra porta":    "http://127.0.0.1:1/videos/job-42",
		"altro schema":   strings.Replace(other, "http://", "https://", 1) + "/videos/job-42",
		"segmento extra": ok + "/content",
	} {
		job := provider.Job{ID: "job-42", PollingURL: u}
		if err := c.CheckJob(job); err == nil {
			t.Errorf("%s: CheckJob doveva rifiutare %s", name, u)
		}
		if _, err := c.FetchVideo(context.Background(), job, time.Second); err == nil {
			t.Errorf("%s: FetchVideo doveva rifiutare", name)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Errorf("richieste=%d, attese 0", n)
	}
}

func TestFetchTimeoutBreveAttesaEScaricamentoLenti(t *testing.T) {
	fastPoll(t)
	slow := make(chan struct{})
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/videos/wait":
			select { // attesa lenta
			case <-slow:
			case <-r.Context().Done():
			}
		case "/videos/dl":
			fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q]}`, api.URL+"/content")
		case "/content": // scaricamento lento
			select {
			case <-slow:
			case <-r.Context().Done():
			}
		}
	}))
	defer api.Close()
	defer close(slow)
	c := New("openrouter", api.URL, "k", Options{Router: true})
	for _, id := range []string{"wait", "dl"} {
		start := time.Now()
		_, err := c.FetchVideo(context.Background(), provider.Job{ID: id, PollingURL: api.URL + "/videos/" + id}, 300*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), "timeout") || !strings.Contains(err.Error(), id) {
			t.Errorf("%s: atteso timeout con id, ottenuto %v", id, err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("%s: terminato dopo %s", id, d)
		}
	}
}
