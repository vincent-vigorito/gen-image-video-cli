package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gen-image-video-cli/internal/jobs"
)

// F2-T2: giv job fetch in-process contro un OpenRouter finto.
func jobFetch(t *testing.T, args ...string) (out, errOut string, code int) {
	t.Helper()
	capture := func(f **os.File) func() string {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		return func() string { w.Close(); *f = old; return <-done }
	}
	so, se := capture(&os.Stdout), capture(&os.Stderr)
	err := cmdJob(append([]string{"fetch"}, args...))
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

func jobEnv(t *testing.T, status string) (srv *httptest.Server, reqs *int32, posts *int32) {
	t.Helper()
	reqs, posts = new(int32), new(int32)
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(reqs, 1)
		if r.Method == http.MethodPost {
			atomic.AddInt32(posts, 1)
		}
		switch r.URL.Path {
		case "/poll":
			if status == "completed" {
				fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q],"usage":{"cost":0.25}}`, "http://"+r.Host+"/content")
			} else {
				fmt.Fprint(w, `{"status":"failed","error":"boom"}`)
			}
		case "/content":
			w.Write([]byte("MP4DATA"))
		}
	}))
	t.Cleanup(srv.Close)
	old := baseURLs["openrouter"]
	baseURLs["openrouter"] = srv.URL
	t.Cleanup(func() { baseURLs["openrouter"] = old })
	t.Setenv("OPENROUTER_API_KEY", "fake")
	t.Chdir(t.TempDir())
	return
}

func saveJob(t *testing.T, prov, id, poll string) {
	t.Helper()
	if err := jobs.Append(jobsFile, jobs.State{Provider: prov, ID: id, PollingURL: poll, Model: "alibaba/wan-3.0", Prompt: "un gatto", Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestJobFetchCompletatoZeroAvvii(t *testing.T) {
	srv, _, posts := jobEnv(t, "completed")
	saveJob(t, "openrouter", "job-1", srv.URL+"/poll")
	out, se, code := jobFetch(t, "--provider", "openrouter", "--out", "o", "job-1")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if *posts != 0 {
		t.Errorf("POST=%d", *posts)
	}
	if !strings.Contains(out, `"job fetch"`) || !strings.Contains(out, "job-1") {
		t.Errorf("manifest inatteso: %s", out)
	}
	if log, _ := os.ReadFile("giv-log.jsonl"); !strings.Contains(string(log), "job-1") {
		t.Errorf("riga di registro mancante: %q", log)
	}
}

func TestJobFetchFallitoPortaId(t *testing.T) {
	srv, _, _ := jobEnv(t, "failed")
	saveJob(t, "openrouter", "job-2", srv.URL+"/poll")
	_, se, code := jobFetch(t, "--provider", "openrouter", "--out", "o", "job-2")
	if code == 0 || !strings.Contains(se, "job-2") {
		t.Fatalf("code=%d %s", code, se)
	}
}

func TestJobFetchRifiuti(t *testing.T) {
	srv, reqs, _ := jobEnv(t, "completed")
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(reqs, 1)
	}))
	defer other.Close()
	saveJob(t, "openrouter", "job-evil", other.URL+"/poll")
	saveJob(t, "gemini", "job-g", srv.URL+"/poll")
	for name, args := range map[string][]string{
		"host diverso":   {"--provider", "openrouter", "job-evil"},
		"senza recupero": {"--provider", "gemini", "job-g"},
		"id inesistente": {"--provider", "openrouter", "nope"},
	} {
		t.Setenv("GEMINI_API_KEY", "fake")
		_, se, code := jobFetch(t, args...)
		if code != 2 {
			t.Errorf("%s: code=%d %s", name, code, se)
		}
	}
	if n := atomic.LoadInt32(reqs); n != 0 {
		t.Errorf("richieste=%d, attese 0", n)
	}
}
