package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// F2-T3: giv story run contro un OpenRouter finto che conta gli avvii (POST
// /videos). Ogni scena lanciata attende un intervallo di polling reale del
// provider (10 s): le storie hanno poche scene.

type runEnv struct {
	dir     string
	board   string
	posts   int32
	polls   int32
	failAt  int32   // dal POST numero N in poi l'avvio fallisce (0 = mai)
	cost    float64 // usage.cost riportato a fine job
	srv     *httptest.Server
	catalog []byte
}

func newRunEnv(t *testing.T, boardJSON string) *runEnv {
	t.Helper()
	cat, err := os.ReadFile("testdata/or-video-models.json")
	if err != nil {
		t.Fatal(err)
	}
	e := &runEnv{cost: 0.5, catalog: cat}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/videos/models"):
			w.Write(e.catalog)
		case r.Method == http.MethodPost && r.URL.Path == "/videos":
			io.Copy(io.Discard, r.Body)
			n := atomic.AddInt32(&e.posts, 1)
			if e.failAt > 0 && n >= e.failAt {
				http.Error(w, `{"error":"rifiutato"}`, http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"id":"job-%d","polling_url":%q,"status":"pending"}`, n, e.srv.URL+"/poll")
		case r.URL.Path == "/poll":
			atomic.AddInt32(&e.polls, 1)
			fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q],"usage":{"cost":%v}}`, e.srv.URL+"/content", e.cost)
		case r.URL.Path == "/content":
			w.Write([]byte("MP4DATA"))
		default:
			t.Errorf("richiesta inattesa %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(e.srv.Close)
	old := baseURLs["openrouter"]
	baseURLs["openrouter"] = e.srv.URL
	t.Cleanup(func() { baseURLs["openrouter"] = old })
	t.Setenv("OPENROUTER_API_KEY", "fake")
	e.dir = t.TempDir()
	t.Chdir(e.dir)
	e.board = filepath.Join(e.dir, "spot.json")
	if err := os.WriteFile(e.board, []byte(boardJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *runEnv) statePath() string { return filepath.Join(e.dir, "spot.stato.json") }

func (e *runEnv) writeState(t *testing.T, s string) {
	t.Helper()
	if err := os.WriteFile(e.statePath(), []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *runEnv) writeClip(t *testing.T, rel string) {
	t.Helper()
	p := filepath.Join(e.dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
}

type runOut struct {
	Started   int `json:"jobs_started"`
	Recovered int `json:"jobs_recovered"`
	Scene     []struct {
		ID      string
		Status  string
		File    string
		ThisRun string  `json:"this_run"`
		Takes   int     `json:"takes"`
		CostUSD float64 `json:"cost_usd"`
	} `json:"scene"`
}

func (e *runEnv) run(t *testing.T, args ...string) (out runOut, errOut string, code int) {
	t.Helper()
	grab := func(f **os.File) func() string {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := io.ReadAll(r); done <- string(b) }()
		return func() string { w.Close(); *f = old; return <-done }
	}
	so, se := grab(&os.Stdout), grab(&os.Stderr)
	err := cmdStory(append([]string{"run"}, append(args, e.board)...))
	sout, serr := so(), se()
	errOut = serr
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
		return
	}
	if err := json.Unmarshal([]byte(sout), &out); err != nil {
		t.Fatalf("uscita non JSON: %v\n%s", err, sout)
	}
	return
}

func (e *runEnv) sceneState(t *testing.T, id string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(e.statePath())
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Scenes map[string]map[string]any `json:"scenes"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st.Scenes[id]
}

const doneState = `{"version":1,"scenes":{
 "s1":{"status":"fatto","clip":{"file":"clip/s1-r1-old.mp4","job_id":"j0","done":"2026-01-01T00:00:00Z"}},
 "s2":{"status":"fatto","clip":{"file":"clip/s2-r1-old.mp4","job_id":"j0","done":"2026-01-01T00:00:00Z"}}}}`

// 1, 2, 3: interruzione dopo la scena 2 (l'avvio della 3 e' rifiutato), ripresa
// dalla 3, poi seconda esecuzione senza avvii.
func TestStoryRunCompletaRipresaESecondaEsecuzione(t *testing.T) {
	e := newRunEnv(t, sb("", sc("s1", ""), sc("s2", ""), sc("s3", "")))
	e.failAt = 3
	_, se, code := e.run(t)
	if code == 0 || e.posts != 3 {
		t.Fatalf("attesa interruzione sul terzo avvio: code=%d posts=%d\n%s", code, e.posts, se)
	}
	if got := e.sceneState(t, "s1")["status"]; got != "fatto" {
		t.Errorf("s1=%v", got)
	}
	if got := e.sceneState(t, "s2")["status"]; got != "fatto" {
		t.Errorf("s2=%v", got)
	}
	if got := e.sceneState(t, "s3")["status"]; got == "fatto" {
		t.Errorf("s3 non doveva essere fatta")
	}
	// ripresa: parte dalla 3, un solo nuovo avvio
	e.failAt = 0
	atomic.StoreInt32(&e.posts, 0)
	out, se, code := e.run(t)
	if code != 0 || e.posts != 1 || out.Started != 1 {
		t.Fatalf("ripresa: code=%d posts=%d started=%d\n%s", code, e.posts, out.Started, se)
	}
	for _, s := range out.Scene {
		if s.Status != "fatto" {
			t.Errorf("scena %s status %s", s.ID, s.Status)
		}
		if s.ID != "s3" && s.ThisRun != "già fatta" {
			t.Errorf("scena %s this_run=%q, attesa già fatta", s.ID, s.ThisRun)
		}
	}
	// seconda esecuzione: zero avvii
	atomic.StoreInt32(&e.posts, 0)
	out, se, code = e.run(t)
	if code != 0 || e.posts != 0 || out.Started != 0 {
		t.Fatalf("seconda esecuzione: code=%d posts=%d\n%s", code, e.posts, se)
	}
}

// 1: esecuzione completa = un avvio per scena.
func TestStoryRunUnAvvioPerScena(t *testing.T) {
	e := newRunEnv(t, sb("", sc("s1", ""), sc("s2", "")))
	out, se, code := e.run(t)
	if code != 0 || e.posts != 2 || out.Started != 2 {
		t.Fatalf("code=%d posts=%d started=%d\n%s", code, e.posts, out.Started, se)
	}
	for _, s := range out.Scene {
		if s.Status != "fatto" || s.File == "" {
			t.Errorf("scena %+v", s)
		}
		if _, err := os.Stat(filepath.Join(e.dir, s.File)); err != nil {
			t.Errorf("clip mancante: %v", err)
		}
	}
}

// 4: job partito e non scaricato: si recupera con il solo polling.
func TestStoryRunRecuperaJobPartito(t *testing.T) {
	e := newRunEnv(t, sb("", sc("s1", "")))
	e.writeState(t, fmt.Sprintf(`{"version":1,"scenes":{"s1":{"status":"partito","job":{"provider":"openrouter","id":"job-9","polling_url":%q,"model":"alibaba/wan-3.0","started":"2026-01-01T00:00:00Z"},"charges":[{"kind":"clip","job_id":"job-9","cost_est_usd":0.5,"at":"2026-01-01T00:00:00Z"}]}}}`, e.srv.URL+"/poll"))
	out, se, code := e.run(t)
	if code != 0 || e.posts != 0 || out.Recovered != 1 || e.polls == 0 {
		t.Fatalf("code=%d posts=%d recovered=%d polls=%d\n%s", code, e.posts, out.Recovered, e.polls, se)
	}
	if out.Scene[0].Status != "fatto" || out.Scene[0].ThisRun != "recuperata" {
		t.Errorf("%+v", out.Scene[0])
	}
}

// 5: --scene rifa' solo quella scena; la ripresa precedente resta (stato e file).
func TestStoryRunSceneRifaSoloQuella(t *testing.T) {
	e := newRunEnv(t, sb("", sc("s1", ""), sc("s2", "")))
	e.writeClip(t, "clip/s1-r1-old.mp4")
	e.writeClip(t, "clip/s2-r1-old.mp4")
	e.writeState(t, doneState)
	out, se, code := e.run(t, "--scene", "s1")
	if code != 0 || e.posts != 1 {
		t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
	}
	s1 := e.sceneState(t, "s1")
	prev, _ := s1["previous"].([]any)
	if len(prev) != 1 || !strings.Contains(fmt.Sprint(prev[0]), "s1-r1-old.mp4") {
		t.Errorf("previous=%v", s1["previous"])
	}
	if clip := fmt.Sprint(s1["clip"]); strings.Contains(clip, "s1-r1-old.mp4") {
		t.Errorf("clip non aggiornata: %s", clip)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "clip/s1-r1-old.mp4")); err != nil {
		t.Errorf("la ripresa precedente e' stata cancellata: %v", err)
	}
	if !strings.Contains(fmt.Sprint(e.sceneState(t, "s2")["clip"]), "s2-r1-old.mp4") {
		t.Errorf("s2 toccata")
	}
	for _, s := range out.Scene {
		if s.ID == "s1" && s.Takes != 2 {
			t.Errorf("takes=%d", s.Takes)
		}
	}
}

// 6: tetto superato a meta': la prima scena costa davvero 0.9 (stima 0.5), la
// seconda non parte (0.9 + 0.5 > tetto); codice 2 e stato coerente.
func TestStoryRunTettoSuperatoAMeta(t *testing.T) {
	e := newRunEnv(t, sb(`,"tetto_costo_usd":1.05`, sc("s1", ""), sc("s2", "")))
	e.cost = 0.9
	_, se, code := e.run(t)
	if code != 2 || e.posts != 1 {
		t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
	}
	if !strings.Contains(se, "tetto") {
		t.Errorf("errore senza tetto: %s", se)
	}
	if got := e.sceneState(t, "s1")["status"]; got != "fatto" {
		t.Errorf("s1=%v", got)
	}
	if st := e.sceneState(t, "s2"); st != nil && st["status"] == "fatto" {
		t.Errorf("s2 non doveva partire: %v", st)
	}
}

// 7: file di stato corrotto: errore di codice 2, nessun avvio.
func TestStoryRunStatoCorrotto(t *testing.T) {
	e := newRunEnv(t, sb("", sc("s1", "")))
	e.writeState(t, `{"version":1,"scenes":{"s1":`)
	_, se, code := e.run(t)
	if code != 2 || e.posts != 0 {
		t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
	}
}
