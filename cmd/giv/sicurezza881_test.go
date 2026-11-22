package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CLI-881 R6: dopo il POST del fotogramma chiave lo stato non si salva: nessun
// POST video, codice 1, riserva alla stima.
func TestStoryRunFotogrammaStatoNonSalvatoNessunVideo(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignora i permessi")
	}
	key := `,"fotogramma_chiave":{"prompt":"un gatto fermo","costo_dichiarato_usd":0.14}`
	e := newRunEnv(t, sb("", sc("s1", key)))
	e.writeClip(t, "clip/x.mp4")
	inner := e.srv.Config.Handler
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r)
		if r.Method == http.MethodPost && r.URL.Path == "/images" {
			os.Chmod(e.dir, 0o555)
		}
	}))
	t.Cleanup(proxy.Close)
	baseURLs["openrouter"] = proxy.URL
	t.Cleanup(func() { os.Chmod(e.dir, 0o755) })
	_, se, code := e.run(t)
	if code == 0 || e.images != 1 || e.posts != 0 || !strings.Contains(se, "stato non salvato, nessuna richiesta video inviata") {
		t.Fatalf("code=%d images=%d posts=%d\n%s", code, e.images, e.posts, se)
	}
}

// CLI-881 R5: --new-job.
func TestStoryRunNewJob(t *testing.T) {
	t.Run("senza flag: stop, suggerisce --new-job", func(t *testing.T) {
		e := newRunEnv(t, sb("", sc("s1", "")))
		e.pendingState(t, "s1", "job-9", "")
		e.pollCode = 400
		_, se, code := e.run(t)
		if code == 0 || e.posts != 0 || !strings.Contains(se, "--scene s1 --new-job") {
			t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
		}
	})
	t.Run("senza --scene o scena senza job: codice 2", func(t *testing.T) {
		e := newRunEnv(t, sb("", sc("s1", ""), sc("s2", "")))
		e.pendingState(t, "s1", "job-9", "")
		if _, se, code := e.run(t, "--new-job"); code != 2 || e.posts != 0 || e.polls != 0 {
			t.Fatalf("senza scene: code=%d posts=%d\n%s", code, e.posts, se)
		}
		if _, se, code := e.run(t, "--scene", "s2", "--new-job"); code != 2 || e.posts != 0 {
			t.Fatalf("scena senza job: code=%d posts=%d\n%s", code, e.posts, se)
		}
	})
	t.Run("abbandona il job, nuovo POST, addebito vecchio resta", func(t *testing.T) {
		e := newRunEnv(t, sb("", sc("s1", "")))
		e.pendingState(t, "s1", "job-9", "")
		e.pollFail, e.pollOnly = true, "job-9" // il vecchio job, se interrogato, risulterebbe fallito
		_, se, code := e.run(t, "--scene", "s1", "--new-job")
		if code != 0 || e.polls != 1 {
			t.Fatalf("code=%d posts=%d polls=%d\n%s", code, e.posts, e.polls, se)
		}
		if e.posts != 1 {
			t.Fatalf("posts=%d\n%s", e.posts, se)
		}
		raw, _ := os.ReadFile(e.statePath())
		s := string(raw)
		for _, want := range []string{`"previous"`, `"kind": "job"`, `"job_id": "job-9"`} {
			if !strings.Contains(strings.ReplaceAll(s, `":"`, `": "`), want) {
				t.Errorf("stato senza %s:\n%s", want, s)
			}
		}
		if got := strings.Count(s, `"cost_est_usd"`); got < 2 {
			t.Errorf("attesi >=2 addebiti, %d:\n%s", got, s)
		}
	})
	t.Run("tetto: stato identico, nessun POST", func(t *testing.T) {
		e := newRunEnv(t, sb(`,"tetto_costo_usd":0.6`, sc("s1", "")))
		e.pendingState(t, "s1", "job-9", "")
		before, _ := os.ReadFile(e.statePath())
		_, se, code := e.run(t, "--scene", "s1", "--new-job")
		after, _ := os.ReadFile(e.statePath())
		if code != 2 || e.posts != 0 || !bytes.Equal(before, after) {
			t.Fatalf("code=%d posts=%d identico=%v\n%s", code, e.posts, bytes.Equal(before, after), se)
		}
	})
	t.Run("stato con previous non valido: codice 2", func(t *testing.T) {
		for name, prev := range map[string]string{
			"job con file":      `{"kind":"job","job_id":"j1","file":"clip/x.mp4","done":"2026-01-01T00:00:00Z"}`,
			"job_id non valido": `{"kind":"job","job_id":"j 1","done":"2026-01-01T00:00:00Z"}`,
			"error su non job":  `{"file":"clip/x.mp4","error":"boom","done":"2026-01-01T00:00:00Z"}`,
		} {
			t.Run(name, func(t *testing.T) {
				e := newRunEnv(t, sb("", sc("s1", "")))
				e.writeClip(t, "clip/x.mp4")
				e.writeState(t, fmt.Sprintf(`{"version":1,"scenes":{"s1":{"status":"partito","job":{"provider":"openrouter","id":"job-9","polling_url":%q,"model":"alibaba/wan-3.0","started":"2026-01-01T00:00:00Z"},"charges":[{"kind":"clip","job_id":"job-9","cost_est_usd":0.5,"at":"2026-01-01T00:00:00Z"}],"previous":[%s]}}}`,
					e.srv.URL+"/videos/job-9", prev))
				if _, se, code := e.run(t, "--scene", "s1", "--new-job"); code != 2 || e.posts != 0 {
					t.Errorf("%s: code=%d posts=%d\n%s", name, code, e.posts, se)
				}
			})
		}
	})
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// CLI-881 R3: story cut con cartella dello storyboard inesistente: codice 2.
func TestStoryCutCartellaInesistente(t *testing.T) {
	_, se, code := cutRun(t, "cut", filepath.Join(t.TempDir(), "assente", "spot.json"))
	if code != 2 {
		t.Fatalf("code=%d\n%s", code, se)
	}
}

// R5 (ee926b5): job con polling_url su host estraneo + --new-job: codice 2,
// nessuna richiesta, stato invariato.
func TestStoryRunNewJobHostEstraneo(t *testing.T) {
	e := newRunEnv(t, sb("", sc("s1", "")))
	e.writeState(t, `{"version":1,"scenes":{"s1":{"status":"partito","job":{"provider":"openrouter","id":"job-9","polling_url":"https://evil.example.com/videos/job-9","model":"alibaba/wan-3.0","started":"2026-01-01T00:00:00Z"},"charges":[{"kind":"clip","job_id":"job-9","cost_est_usd":0.5,"at":"2026-01-01T00:00:00Z"}]}}}`)
	before, _ := os.ReadFile(e.statePath())
	_, se, code := e.run(t, "--scene", "s1", "--new-job")
	after, _ := os.ReadFile(e.statePath())
	if code != 2 || e.posts != 0 || e.polls != 0 || !bytes.Equal(before, after) {
		t.Fatalf("code=%d posts=%d polls=%d identico=%v\n%s", code, e.posts, e.polls, bytes.Equal(before, after), se)
	}
}
