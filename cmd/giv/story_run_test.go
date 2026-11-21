package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	images  int32 // richieste POST /images (fotogramma chiave)
	bodies  []string
	mu      sync.Mutex
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
			b, _ := io.ReadAll(r.Body)
			e.mu.Lock()
			e.bodies = append(e.bodies, string(b))
			e.mu.Unlock()
			n := atomic.AddInt32(&e.posts, 1)
			if e.failAt > 0 && n >= e.failAt {
				http.Error(w, `{"error":"rifiutato"}`, http.StatusBadRequest)
				return
			}
			fmt.Fprintf(w, `{"id":"job-%d","polling_url":%q,"status":"pending"}`, n, e.srv.URL+"/poll")
		case r.Method == http.MethodPost && r.URL.Path == "/images":
			io.Copy(io.Discard, r.Body)
			atomic.AddInt32(&e.images, 1)
			var b bytes.Buffer
			png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4)))
			fmt.Fprintf(w, `{"data":[{"b64_json":%q,"media_type":"image/png"}],"usage":{"cost":0.14}}`, base64.StdEncoding.EncodeToString(b.Bytes()))
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

// 8: fotogramma chiave generato una volta sola e riusato alla ripresa; il suo
// costo dichiarato entra nel tetto.
func TestStoryRunFotogrammaChiaveRiusato(t *testing.T) {
	key := `,"fotogramma_chiave":{"prompt":"un gatto fermo","costo_dichiarato_usd":0.14}`
	t.Run("tetto", func(t *testing.T) {
		// clip 0.5 + fotogramma 0.14 = 0.64 > 0.6
		e := newRunEnv(t, sb(`,"tetto_costo_usd":0.6`, sc("s1", key)))
		_, se, code := e.run(t)
		if code != 2 || e.posts != 0 || e.images != 0 {
			t.Fatalf("code=%d posts=%d images=%d\n%s", code, e.posts, e.images, se)
		}
	})
	t.Run("riuso", func(t *testing.T) {
		e := newRunEnv(t, sb(`,"tetto_costo_usd":2`, sc("s1", key)))
		out, se, code := e.run(t)
		if code != 0 || e.images != 1 || e.posts != 1 {
			t.Fatalf("prima: code=%d images=%d posts=%d\n%s", code, e.images, e.posts, se)
		}
		if !strings.Contains(e.bodies[0], "first_frame") {
			t.Errorf("la clip non parte dal fotogramma chiave: %s", e.bodies[0])
		}
		_ = out
		// seconda ripresa della stessa scena: nuova clip, fotogramma riusato
		_, se, code = e.run(t, "--scene", "s1")
		if code != 0 || e.images != 1 || e.posts != 2 {
			t.Fatalf("--scene: code=%d images=%d posts=%d\n%s", code, e.images, e.posts, se)
		}
	})
}

type fakeFrames struct {
	png  []byte
	clip string
}

func (f *fakeFrames) Check() error { return nil }
func (f *fakeFrames) LastFrame(_ context.Context, clip, out string, _ bool) error {
	f.clip = clip
	return os.WriteFile(out, f.png, 0o644)
}

// 9: continua_da: la scena parte dall'ultimo fotogramma della scena indicata
// (estrattore finto: nessun bisogno di ffmpeg, i video del server finto non sono veri).
func TestStoryRunContinuaDaPartoDallUltimoFotogramma(t *testing.T) {
	var b bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 3, 5)) // misure uniche: si riconosce nel corpo
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	f := &fakeFrames{png: b.Bytes()}
	old := storyFrames
	storyFrames = f
	t.Cleanup(func() { storyFrames = old })
	e := newRunEnv(t, sb("", sc("s1", ""), sc("s2", `,"continua_da":"s1"`)))
	_, se, code := e.run(t)
	if code != 0 || e.posts != 2 {
		t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
	}
	if strings.Contains(e.bodies[0], "first_frame") {
		t.Errorf("s1 non deve avere un primo fotogramma: %s", e.bodies[0])
	}
	if !strings.Contains(e.bodies[1], "first_frame") || !strings.Contains(e.bodies[1], base64.StdEncoding.EncodeToString(f.png)) {
		t.Errorf("s2 non parte dall'ultimo fotogramma di s1: %.300s", e.bodies[1])
	}
	if !strings.Contains(filepath.ToSlash(f.clip), "clip/s1-r1-") {
		t.Errorf("fotogramma estratto dalla clip sbagliata: %s", f.clip)
	}
}
