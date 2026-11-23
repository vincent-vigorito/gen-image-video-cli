package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// CLI-1008 (giv 0.13.0, filone A): story run con scene in parallelo e tetto di
// spesa, contro un OpenRouter finto. Ogni job attende un intervallo di
// polling reale (10 s): poche scene per test.

type parEnv struct {
	dir, board   string
	mu           sync.Mutex
	bodies       []string
	posts        int32
	reqs         int32 // ogni richiesta ricevuta
	polls        int32
	pollsAtFirst int32 // POST già arrivati al primo polling
	delay        map[string]time.Duration
	dropAt       int32         // al POST N: connessione chiusa dopo aver letto il corpo (0 = mai)
	contentCode  int32         // se != 0, lo scaricamento risponde con questo stato
	onPost       func(n int32) // dopo aver letto il POST numero n
	gate         chan struct{} // se != nil, il POST attende la chiusura del canale
	srv          *httptest.Server
}

func newParEnv(t *testing.T, boardJSON string, noPrice bool) *parEnv {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	cat, err := os.ReadFile(filepath.Join(filepath.Dir(here), "testdata", "or-video-models.json"))
	if err != nil {
		t.Fatal(err)
	}
	if noPrice {
		var m map[string]any
		if err := json.Unmarshal(cat, &m); err != nil {
			t.Fatal(err)
		}
		for _, x := range m["data"].([]any) {
			if mm := x.(map[string]any); mm["id"] == "alibaba/wan-3.0" {
				delete(mm, "pricing_skus")
			}
		}
		if cat, err = json.Marshal(m); err != nil {
			t.Fatal(err)
		}
	}
	e := &parEnv{delay: map[string]time.Duration{}}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&e.reqs, 1)
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/videos/models"):
			w.Write(cat)
		case r.Method == http.MethodPost && r.URL.Path == "/videos":
			b, _ := io.ReadAll(r.Body)
			e.mu.Lock()
			e.bodies = append(e.bodies, string(b))
			e.mu.Unlock()
			n := atomic.AddInt32(&e.posts, 1)
			if e.onPost != nil {
				e.onPost(n)
			}
			if e.gate != nil {
				<-e.gate
			}
			if e.dropAt > 0 && n == e.dropAt {
				if hj, ok := w.(http.Hijacker); ok {
					conn, _, _ := hj.Hijack()
					conn.Close()
					return
				}
			}
			fmt.Fprintf(w, `{"id":"job-%d","polling_url":%q,"status":"pending"}`, n, e.srv.URL+"/videos/job-"+fmt.Sprint(n))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/videos/job-"):
			atomic.AddInt32(&e.polls, 1)
			atomic.CompareAndSwapInt32(&e.pollsAtFirst, 0, atomic.LoadInt32(&e.posts))
			e.mu.Lock()
			d := e.delay[strings.TrimPrefix(r.URL.Path, "/videos/")]
			e.mu.Unlock()
			time.Sleep(d)
			fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q],"usage":{"cost":0.5}}`, e.srv.URL+"/content")
		case r.URL.Path == "/content":
			if c := atomic.LoadInt32(&e.contentCode); c != 0 {
				http.Error(w, "no", int(c))
				return
			}
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

// ps è una scena video il cui prompt è p-<id>: si riconosce nel corpo del POST.
func ps(id, extra string) string {
	return fmt.Sprintf(`{"id":"%s","tipo":"video","durata":5,"prompt":"p-%s"%s}`, id, id, extra)
}

func (e *parEnv) statePath() string { return filepath.Join(e.dir, "spot.stato.json") }

type parOut struct {
	Started   int `json:"jobs_started"`
	Recovered int `json:"jobs_recovered"`
	Scene     []struct {
		ID, Status, File string
	} `json:"scenes"`
}

// run lancia giv story run <args> <storyboard>.
func (e *parEnv) run(t *testing.T, args ...string) (out parOut, errOut string, code int) {
	t.Helper()
	so, se := grabFile(&os.Stdout), grabFile(&os.Stderr)
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

func grabFile(f **os.File) func() string {
	old := *f
	r, w, _ := os.Pipe()
	*f = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	return func() string { w.Close(); *f = old; return <-done }
}

type parState struct {
	Scenes map[string]struct {
		Status string `json:"status"`
		Job    *struct {
			ID string `json:"id"`
		} `json:"job"`
		Charges []struct {
			Kind    string  `json:"kind"`
			CostEst float64 `json:"cost_est_usd"`
			Cost    float64 `json:"cost_usd"`
		} `json:"charges"`
	} `json:"scenes"`
}

func (e *parEnv) state(t *testing.T) parState {
	t.Helper()
	var st parState
	b, err := os.ReadFile(e.statePath())
	if os.IsNotExist(err) {
		return st
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

var stamp = regexp.MustCompile(`-\d{8}-\d{6}`)

func (e *parEnv) bodyOrder() string {
	var ids []string
	for _, b := range e.bodies {
		for _, id := range []string{"s1", "s2", "s3"} {
			if strings.Contains(b, "p-"+id) {
				ids = append(ids, id)
			}
		}
	}
	return strings.Join(ids, ",")
}

// tre scene indipendenti con --parallel 3: tre job aperti insieme (tutti e tre
// i POST prima del primo polling), avvii nell'ordine dello storyboard, uscita
// nell'ordine dello storyboard con completamenti in ordine inverso.
func TestParallelTreSceneInsiemeEOrdine(t *testing.T) {
	e := newParEnv(t, sb("", ps("s1", ""), ps("s2", ""), ps("s3", "")), false)
	e.delay["job-1"], e.delay["job-2"] = 1200*time.Millisecond, 600*time.Millisecond
	out, se, code := e.run(t, "--parallel", "3")
	if code != 0 || e.posts != 3 || out.Started != 3 {
		t.Fatalf("code=%d posts=%d started=%d\n%s", code, e.posts, out.Started, se)
	}
	if e.pollsAtFirst != 3 {
		t.Errorf("al primo polling erano partiti %d job, attesi 3 aperti insieme", e.pollsAtFirst)
	}
	if got := e.bodyOrder(); got != "s1,s2,s3" {
		t.Errorf("ordine degli avvii %s", got)
	}
	if len(out.Scene) != 3 {
		t.Fatalf("scene %+v", out.Scene)
	}
	for i, id := range []string{"s1", "s2", "s3"} {
		s := out.Scene[i]
		if s.ID != id || s.Status != "fatto" {
			t.Errorf("posizione %d: %+v", i, s)
		}
		if _, err := os.Stat(filepath.Join(e.dir, s.File)); err != nil {
			t.Errorf("clip %s: %v", id, err)
		}
	}
}

// una scena che dipende da un'altra (continua_da) aspetta: al primo polling i
// POST sono due, il terzo parte dopo.
func TestParallelSceneDipendenteAspetta(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 3, 5))); err != nil {
		t.Fatal(err)
	}
	f := &fakeFrames{png: b.Bytes()}
	old := storyFrames
	storyFrames = f
	t.Cleanup(func() { storyFrames = old })
	e := newParEnv(t, sb("", ps("s1", ""), ps("s2", ""), ps("s3", `,"continua_da":"s1"`)), false)
	out, se, code := e.run(t, "--parallel", "3")
	if code != 0 || e.posts != 3 || out.Started != 3 {
		t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
	}
	if e.pollsAtFirst != 2 {
		t.Errorf("al primo polling erano partiti %d job, attesi 2 (s3 aspetta s1)", e.pollsAtFirst)
	}
	if !strings.Contains(e.bodies[2], "p-s3") || !strings.Contains(e.bodies[2], "first_frame") {
		t.Errorf("s3 non parte dopo s1 dal suo ultimo fotogramma: %.200s", e.bodies[2])
	}
}

// --parallel fuori da 1..4: codice 2 e nessuna richiesta.
func TestParallelFuoriIntervallo(t *testing.T) {
	for _, n := range []string{"5", "0"} {
		e := newParEnv(t, sb("", ps("s1", ""), ps("s2", "")), false)
		_, se, code := e.run(t, "--parallel", n)
		if code != 2 || e.reqs != 0 {
			t.Errorf("--parallel %s: code=%d richieste=%d\n%s", n, code, e.reqs, se)
		}
		if _, err := os.Stat(e.statePath()); err == nil {
			t.Errorf("--parallel %s: stato scritto", n)
		}
	}
}

// due scene ciascuna sotto il tetto ma insieme sopra: codice 2, nessun POST
// oltre il tetto, nello stato niente sopra il tetto.
func TestParallelDueSceneInsiemeOltreIlTetto(t *testing.T) {
	e := newParEnv(t, sb(`,"tetto_costo_usd":0.7`, ps("s1", ""), ps("s2", "")), false)
	_, se, code := e.run(t, "--parallel", "2")
	if code != 2 || e.posts > 1 {
		t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
	}
	if !strings.Contains(se, "tetto") {
		t.Errorf("errore senza tetto: %s", se)
	}
	sum := 0.0
	for _, s := range e.state(t).Scenes {
		for _, c := range s.Charges {
			sum += c.CostEst + c.Cost
		}
	}
	if sum > 0.7 {
		t.Errorf("riserve e addebiti %v oltre il tetto 0.7", sum)
	}
}

// costo non stimabile con un tetto: codice 2 prima di qualsiasi avvio.
func TestParallelNonStimabileConTetto(t *testing.T) {
	e := newParEnv(t, sb(`,"tetto_costo_usd":5`, ps("s1", ""), ps("s2", "")), true)
	_, se, code := e.run(t, "--parallel", "2")
	if code != 2 || e.posts != 0 {
		t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
	}
}

// arresto dopo la riserva e prima della risposta: la riserva resta nello stato
// e alla ripresa il POST di quella scena non si ripete.
func TestParallelRiservaIncertaNonSiRilancia(t *testing.T) {
	e := newParEnv(t, sb(`,"tetto_costo_usd":3`, ps("s1", ""), ps("s2", "")), false)
	e.dropAt = 1 // solo il primo POST perde la risposta
	e.run(t, "--parallel", "2")
	st := e.state(t)
	s1 := st.Scenes["s1"]
	if s1.Status != "incerto" || len(s1.Charges) != 1 || s1.Charges[0].CostEst != 0.5 {
		t.Fatalf("s1 dopo l'arresto: %+v", s1)
	}
	e.dropAt = 0
	before := strings.Count(strings.Join(e.bodies, "|"), "p-s1")
	_, se, code := e.run(t, "--parallel", "2")
	if code != 2 {
		t.Errorf("ripresa: code=%d\n%s", code, se)
	}
	if got := strings.Count(strings.Join(e.bodies, "|"), "p-s1"); got != before || before != 1 {
		t.Errorf("POST di s1: %d prima, %d dopo la ripresa", before, got)
	}
	if st := e.state(t).Scenes["s1"]; st.Status != "incerto" || len(st.Charges) != 1 {
		t.Errorf("la riserva incerta non deve sparire: %+v", st)
	}
}

// job noto con scaricamento fallito: alla ripresa si recupera, nessun secondo POST.
func TestParallelJobNotoScaricamentoFallito(t *testing.T) {
	e := newParEnv(t, sb("", ps("s1", ""), ps("s2", "")), false)
	atomic.StoreInt32(&e.contentCode, http.StatusNotFound)
	if _, se, code := e.run(t, "--parallel", "2"); code == 0 {
		t.Fatalf("atteso errore dallo scaricamento\n%s", se)
	}
	posts := e.posts
	jobs := 0
	for id, s := range e.state(t).Scenes {
		if s.Job == nil {
			t.Fatalf("%s: job non registrato: %+v", id, s)
		}
		jobs++
	}
	if posts < 1 || int(posts) != jobs {
		t.Fatalf("posts=%d, job nello stato %d", posts, jobs)
	}
	atomic.StoreInt32(&e.contentCode, 0)
	out, se, code := e.run(t, "--parallel", "2")
	if code != 0 || e.posts != 2 || out.Recovered != jobs {
		t.Fatalf("ripresa: code=%d posts=%d (prima %d) recovered=%d\n%s", code, e.posts, posts, out.Recovered, se)
	}
}

// errore di scrittura dello stato (cartella non scrivibile dopo il primo POST):
// gli avvii si fermano e ogni POST fatto ha la sua riserva salvata: il conto è esatto.
func TestParallelErroreScritturaStatoFermaGliAvvii(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root scrive comunque")
	}
	e := newParEnv(t, sb(`,"tetto_costo_usd":10`, ps("s1", ""), ps("s2", ""), ps("s3", "")), false)
	e.onPost = func(n int32) {
		if n == 1 {
			os.Chmod(e.dir, 0o500)
		}
	}
	_, se, code := e.run(t, "--parallel", "2")
	os.Chmod(e.dir, 0o755)
	if code == 0 || e.posts >= 3 {
		t.Fatalf("code=%d posts=%d: gli avvii dovevano fermarsi\n%s", code, e.posts, se)
	}
	n, sum := 0, 0.0
	for _, s := range e.state(t).Scenes {
		for _, c := range s.Charges {
			n++
			if c.Cost > 0 {
				sum += c.Cost
			} else {
				sum += c.CostEst
			}
		}
	}
	if n != int(e.posts) || sum != 0.5*float64(e.posts) {
		t.Errorf("conto nello stato: %d addebiti/riserve, somma %v; POST fatti %d", n, sum, e.posts)
	}
}

// seconda esecuzione simultanea sullo stesso stato: rifiutata, nessun POST suo.
func TestParallelSecondaEsecuzioneSimultaneaRifiutata(t *testing.T) {
	e := newParEnv(t, sb("", ps("s1", "")), false)
	e.gate = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := runStory(context.Background(), e.board, storyRunOptions{})
		done <- err
	}()
	deadline := time.Now().Add(15 * time.Second)
	for atomic.LoadInt32(&e.posts) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if e.posts != 1 {
		t.Fatal("la prima esecuzione non è partita")
	}
	_, se, code := e.run(t, "--parallel", "2")
	close(e.gate)
	if code == 0 || e.posts != 1 {
		t.Errorf("seconda esecuzione: code=%d posts=%d\n%s", code, e.posts, se)
	}
	if err := <-done; err != nil {
		t.Errorf("prima esecuzione: %v", err)
	}
}

// --parallel 1 esplicito = senza opzione (0.12.1): stesse richieste, stessi file.
func TestParallelUnoEquivaleAlPredefinito(t *testing.T) {
	snap := func(args ...string) (bodies []string, files []string) {
		e := newParEnv(t, sb("", ps("s1", ""), ps("s2", "")), false)
		out, se, code := e.run(t, args...)
		if code != 0 || out.Started != 2 {
			t.Fatalf("%v: code=%d\n%s", args, code, se)
		}
		for _, s := range out.Scene {
			files = append(files, s.ID+"="+filepath.Dir(s.File))
		}
		filepath.Walk(e.dir, func(p string, i os.FileInfo, err error) error {
			if err == nil && !i.IsDir() {
				rel, _ := filepath.Rel(e.dir, p)
				files = append(files, stamp.ReplaceAllString(rel, "-T"))
			}
			return nil
		})
		sort.Strings(files)
		return e.bodies, files
	}
	b0, f0 := snap()
	b1, f1 := snap("--parallel", "1")
	if strings.Join(b0, "\n") != strings.Join(b1, "\n") {
		t.Errorf("richieste diverse:\n%q\n%q", b0, b1)
	}
	if strings.Join(f0, "\n") != strings.Join(f1, "\n") {
		t.Errorf("file diversi:\n%q\n%q", f0, f1)
	}
}
