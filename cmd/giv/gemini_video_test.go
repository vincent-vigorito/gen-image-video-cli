package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// O-T8: video con Gemini diretto (interactions) contro un server finto.

const gemKey = "chiave-finta-gemini"

type fakeGem struct {
	srv    *httptest.Server
	mu     sync.Mutex
	reqs   []string // "METODO path"
	bodies []string // corpi dei POST
	keys   []string // chiave vista su ogni richiesta
	other  *httptest.Server
	otherN int
	mode   string // "uri": video via file; "inline": base64; "redirect": download rimanda altrove
}

func newFakeGem(t *testing.T, mode string) *fakeGem {
	t.Helper()
	g := &fakeGem{mode: mode}
	g.other = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.otherN++
		g.mu.Unlock()
		w.Write([]byte("MP4ALTROVE"))
	}))
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.reqs = append(g.reqs, r.Method+" "+r.URL.Path)
		g.keys = append(g.keys, r.Header.Get("x-goog-api-key")+"|"+r.URL.RawQuery)
		if r.Method == http.MethodPost {
			g.bodies = append(g.bodies, string(b))
		}
		g.mu.Unlock()
		base := "http://" + r.Host
		usage := `"usage":{"total_input_tokens":100,"total_output_tokens":57920,"total_thought_tokens":0,"output_tokens_by_modality":[{"modality":"video","tokens":57920}]}`
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/interactions":
			if mode == "post307" {
				http.Redirect(w, r, base+"/interactions-altro", http.StatusTemporaryRedirect)
				return
			}
			if mode == "noid" {
				fmt.Fprintf(w, `{"status":"completed","steps":[{"type":"model_output","content":[{"type":"video","mime_type":"video/mp4","data":%q}]}],%s}`,
					base64.StdEncoding.EncodeToString([]byte("MP4INLINE")), usage)
				return
			}
			if mode == "inline" {
				fmt.Fprintf(w, `{"id":"int-1","status":"completed","steps":[{"type":"model_output","content":[{"type":"video","mime_type":"video/mp4","data":%q}]}],%s}`,
					base64.StdEncoding.EncodeToString([]byte("MP4INLINE")), usage)
				return
			}
			fmt.Fprint(w, `{"id":"int-1","status":"in_progress"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/interactions/int-1":
			fmt.Fprintf(w, `{"id":"int-1","status":"completed","steps":[{"type":"model_output","content":[{"type":"video","mime_type":"video/mp4","uri":%q}]}],%s}`,
				base+"/files/f1:download?alt=media", usage)
		case r.Method == http.MethodGet && r.URL.Path == "/files/f1":
			fmt.Fprint(w, `{"name":"files/f1","state":"ACTIVE"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/files/f1:download":
			if mode == "redirect" {
				http.Redirect(w, r, g.other.URL+"/v.mp4", http.StatusFound)
				return
			}
			w.Write([]byte("MP4FILE"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.srv.Close)
	t.Cleanup(g.other.Close)
	old := baseURLs["gemini"]
	baseURLs["gemini"] = g.srv.URL
	t.Cleanup(func() { baseURLs["gemini"] = old })
	t.Setenv("GEMINI_API_KEY", gemKey)
	t.Chdir(t.TempDir())
	return g
}

func (g *fakeGem) posts() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.bodies)
}

func (g *fakeGem) body(t *testing.T, i int) map[string]any {
	t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	if i >= len(g.bodies) {
		t.Fatalf("nessun POST n. %d", i)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(g.bodies[i]), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func vcfg(m map[string]any) map[string]any {
	gc, _ := m["generation_config"].(map[string]any)
	vc, _ := gc["video_config"].(map[string]any)
	return vc
}

// 1: avvio, attesa (job in corso), risultato scaricato dall'API dei file, manifest col costo stimato.
func TestVideoGeminiAvvioAttesaRisultato(t *testing.T) {
	g := newFakeGem(t, "uri")
	out, code, err := runVideo(t, "--provider", "gemini", "--out", "o", "un gatto")
	if code != 0 {
		t.Fatalf("code=%d %v", code, err)
	}
	if g.posts() != 1 {
		t.Fatalf("POST=%d, atteso 1", g.posts())
	}
	b := g.body(t, 0)
	rf, _ := b["response_format"].(map[string]any)
	if b["input"] != "un gatto" || rf["type"] != "video" || vcfg(b)["task"] != "text_to_video" {
		t.Errorf("corpo: %s", g.bodies[0])
	}
	var man struct {
		Files []struct{ Path string } `json:"files"`
		Cost  *float64                `json:"cost_est_usd"`
		Prov  string                  `json:"provider"`
	}
	if err := json.Unmarshal([]byte(out), &man); err != nil || len(man.Files) != 1 || man.Cost == nil || *man.Cost <= 0 || man.Prov != "gemini" {
		t.Fatalf("manifest: %v %s", err, out)
	}
	if data, _ := os.ReadFile(man.Files[0].Path); string(data) != "MP4FILE" {
		t.Errorf("file scaricato %q", data)
	}
	// 57920 token video x 12/M + 100 ingresso x 1,5/M
	if want := 57920*12.0/1e6 + 100*1.5/1e6; *man.Cost < want-0.001 || *man.Cost > want+0.001 {
		t.Errorf("costo %v, atteso ~%v", *man.Cost, want)
	}
	var sawPoll, sawFile bool
	for _, r := range g.reqs {
		sawPoll = sawPoll || r == "GET /interactions/int-1"
		sawFile = sawFile || r == "GET /files/f1"
	}
	if !sawPoll || !sawFile {
		t.Errorf("richieste: %v", g.reqs)
	}
}

// 2: piu' immagini di riferimento nel corpo.
func TestVideoGeminiPiuRiferimenti(t *testing.T) {
	g := newFakeGem(t, "inline")
	writePNG(t)
	_, code, err := runVideo(t, "--provider", "gemini", "--out", "o", "--ref", "a.png", "--ref", "b.png", "un gatto")
	if code != 0 {
		t.Fatalf("code=%d %v", code, err)
	}
	b := g.body(t, 0)
	in, _ := b["input"].([]any)
	var imgs, texts int
	for _, it := range in {
		m, _ := it.(map[string]any)
		switch m["type"] {
		case "image":
			imgs++
			if m["data"] == "" || m["mime_type"] == "" {
				t.Errorf("immagine incompleta: %v", m)
			}
		case "text":
			texts++
		}
	}
	if imgs != 2 || texts != 1 || vcfg(b)["task"] != "reference_to_video" {
		t.Errorf("corpo: %s", g.bodies[0])
	}
}

// 3: estensione di un video generato con --from.
func TestVideoGeminiEstensioneConFrom(t *testing.T) {
	g := newFakeGem(t, "inline")
	err := cmdClip(clipSpec{Op: "extend", Providers: []string{"xai", "gemini"}},
		[]string{"--provider", "gemini", "--from", "int-0", "--out", "o", "continua la scena"})
	if err != nil {
		t.Fatal(err)
	}
	b := g.body(t, 0)
	if b["previous_interaction_id"] != "int-0" || b["input"] != "continua la scena" || vcfg(b)["task"] != "extend" {
		t.Errorf("corpo: %s", g.bodies[0])
	}
}

// 4: estensione o modifica con un file caricato: codice 2, nessuna richiesta.
func TestVideoGeminiFileCaricatoRifiutato(t *testing.T) {
	g := newFakeGem(t, "inline")
	os.WriteFile("v.mp4", []byte("MP4"), 0o644)
	for _, op := range []string{"extend", "edit"} {
		err := cmdClip(clipSpec{Op: op, Providers: []string{"xai", "gemini"}},
			[]string{"--provider", "gemini", "--video", "v.mp4", "--out", "o", "x"})
		if err == nil {
			t.Fatalf("%s: atteso errore", op)
		}
		if code, _ := exitCode(err); code != 2 {
			t.Errorf("%s: code=%d (%v)", op, code, err)
		}
	}
	if len(g.reqs) != 0 {
		t.Errorf("richieste: %v", g.reqs)
	}
}

// 5: --dry-run stima senza avviare; stima oltre --max-cost: codice 2, nessun avvio.
func TestVideoGeminiDryRunEMaxCost(t *testing.T) {
	g := newFakeGem(t, "inline")
	out, code, err := runVideo(t, "--provider", "gemini", "--dry-run", "--duration", "10", "--resolution", "720p", "x")
	if code != 0 {
		t.Fatalf("dry-run code=%d %v", code, err)
	}
	if c := costOf(t, out); c < 1.286 || c > 1.287 {
		t.Errorf("stima %v, attesa 1.2864 (video + ingresso e uscita testo)", c)
	}
	_, code, err = runVideo(t, "--provider", "gemini", "--max-cost", "0.1", "--duration", "10", "x")
	if code != 2 {
		t.Fatalf("max-cost: code=%d %v", code, err)
	}
	if len(g.reqs) != 0 {
		t.Errorf("richieste: %v", g.reqs)
	}
}

// 6: la chiave va solo all'host di Google (qui: il server finto), mai nell'URL
// e mai all'host verso cui un download viene rimandato.
func TestVideoGeminiChiaveSoloAlProvider(t *testing.T) {
	g := newFakeGem(t, "redirect")
	runVideo(t, "--provider", "gemini", "--out", "o", "un gatto")
	if len(g.keys) == 0 {
		t.Fatal("nessuna richiesta")
	}
	for i, k := range g.keys {
		parts := strings.SplitN(k, "|", 2)
		if parts[0] != gemKey || strings.Contains(parts[1], "key=") {
			t.Errorf("richiesta %s: chiave/query %q", g.reqs[i], k)
		}
	}
	if g.otherN != 0 {
		// il redirect verso http fuori origine è rifiutato: nessun contatto
		t.Errorf("host estraneo contattato %d volte", g.otherN)
	}
	if entries, _ := filepath.Glob("o/*"); len(entries) != 0 {
		t.Errorf("file scritto dopo redirect rifiutato: %v", entries)
	}
}

// D16: senza provider scelto (flag o GIV_PROVIDER) il video non parte, nemmeno in dry-run.
func TestVideoGeminiSoloConSceltaEsplicita(t *testing.T) {
	g := newFakeGem(t, "inline")
	t.Setenv("GIV_PROVIDER", "")
	for _, args := range [][]string{{"x"}, {"--dry-run", "x"}} {
		_, code, err := runVideo(t, args...)
		if code != 2 || err == nil || !strings.Contains(err.Error(), "nessun provider") {
			t.Errorf("%v: code=%d %v", args, code, err)
		}
	}
	if len(g.reqs) != 0 {
		t.Errorf("richieste: %v", g.reqs)
	}
	t.Setenv("GIV_PROVIDER", "gemini")
	if _, code, err := runVideo(t, "--dry-run", "x"); code != 0 {
		t.Errorf("GIV_PROVIDER=gemini: code=%d %v", code, err)
	}
}

// Il POST di avvio non segue un 307: un solo POST, esito incerto (errore), nessun file.
func TestVideoGeminiPostNonSegueRedirect(t *testing.T) {
	g := newFakeGem(t, "post307")
	_, code, err := runVideo(t, "--provider", "gemini", "--out", "o", "x")
	if code == 0 || err == nil {
		t.Fatalf("atteso errore, code=%d", code)
	}
	if g.posts() != 1 {
		t.Errorf("POST=%d, atteso 1: %v", g.posts(), g.reqs)
	}
	if m, _ := filepath.Glob("o/*"); len(m) != 0 {
		t.Errorf("file scritti: %v", m)
	}
}

// Risposta completed senza id: nessun risultato consegnato.
func TestVideoGeminiCompletedSenzaIdRifiutato(t *testing.T) {
	g := newFakeGem(t, "noid")
	_, code, err := runVideo(t, "--provider", "gemini", "--out", "o", "x")
	if code == 0 || err == nil || g.posts() != 1 {
		t.Fatalf("code=%d err=%v posts=%d", code, err, g.posts())
	}
	if m, _ := filepath.Glob("o/*"); len(m) != 0 {
		t.Errorf("file scritti: %v", m)
	}
}
