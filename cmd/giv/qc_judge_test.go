package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// P-C (CLI-1010), giv 0.13.0: giudizio di aderenza con Gemini, solo server finto.

type judgeEnv struct {
	dir   string
	video string
	ref   string
	mu    sync.Mutex
	reqs  []string // corpi
	paths []string
	keys  []string
}

func (e *judgeEnv) hits() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.reqs) }

var judgeInline = regexp.MustCompile(`"(inline_data|inlineData)"`)

func newJudgeEnv(t *testing.T, h func(w http.ResponseWriter)) *judgeEnv {
	t.Helper()
	cutNeedFF(t)
	e := &judgeEnv{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.reqs = append(e.reqs, string(b))
		e.paths = append(e.paths, r.Method+" "+r.URL.Path)
		e.keys = append(e.keys, r.Header.Get("x-goog-api-key"))
		e.mu.Unlock()
		h(w)
	}))
	t.Cleanup(srv.Close)
	old := baseURLs["gemini"]
	baseURLs["gemini"] = srv.URL
	t.Cleanup(func() { baseURLs["gemini"] = old })
	t.Setenv("GEMINI_API_KEY", "fake-key-xyz")
	t.Setenv("HOME", t.TempDir())
	e.dir = t.TempDir()
	t.Chdir(e.dir)
	e.video = filepath.Join(e.dir, "clip.mp4")
	cutClip(t, e.video, "256x144", 25, "4")
	e.ref = filepath.Join(e.dir, "ref.png")
	cutFF(t, "-f", "lavfi", "-i", "testsrc=size=64x64", "-frames:v", "1", e.ref)
	return e
}

func geminiText(text string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"candidates":    []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": text}}}}},
			"usageMetadata": map[string]any{"promptTokenCount": 1200, "candidatesTokenCount": 80, "thoughtsTokenCount": 0},
		})
	}
}

func judgeVerdict(v string, motivi string) string {
	return `{"verdetto":"` + v + `","riconoscibile":"si","azione":"rispettata","motivi":[` + motivi + `],"difetti":[],"non_valutabile":[]}`
}

func judgeRun(t *testing.T, args ...string) (out, errOut string, code int) {
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
	err := cmdQC(args)
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

type judgeOut struct {
	DryRun   bool `json:"dry_run"`
	Judgment *struct {
		Verdict string   `json:"verdict"`
		Reasons []string `json:"reasons"`
	} `json:"judgment"`
	Request *struct {
		Destination string `json:"destination"`
		Images      []struct {
			Role string `json:"role"`
		} `json:"images"`
	} `json:"request"`
	CostEst *float64 `json:"cost_est_usd"`
}

func judgeParse(t *testing.T, out string) judgeOut {
	t.Helper()
	var o judgeOut
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatalf("stdout non e' JSON: %v\n%s", err, out)
	}
	return o
}

func TestJudgeSpentoZeroInvii(t *testing.T) {
	e := newJudgeEnv(t, geminiText(judgeVerdict("aderente", "")))
	out, se, code := judgeRun(t, e.video)
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if e.hits() != 0 {
		t.Fatalf("invii senza --judge: %d", e.hits())
	}
	if o := judgeParse(t, out); o.Judgment != nil || o.Request != nil {
		t.Errorf("giudizio presente senza --judge: %s", out)
	}
}

func TestJudgeAderenteQuattroFotogrammi(t *testing.T) {
	e := newJudgeEnv(t, geminiText(judgeVerdict("aderente", `"Anja e' riconoscibile"`)))
	out, se, code := judgeRun(t, "--video", e.video, "--judge", "--prompt", "Anja mostra ARGO", "--max-cost", "1")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if e.hits() != 1 {
		t.Fatalf("POST: %d", e.hits())
	}
	if !strings.Contains(e.paths[0], ":generateContent") || e.keys[0] != "fake-key-xyz" {
		t.Errorf("richiesta inattesa %s key=%q", e.paths[0], e.keys[0])
	}
	if n := len(judgeInline.FindAllString(e.reqs[0], -1)); n != 4 {
		t.Errorf("fotogrammi inviati: %d", n)
	}
	if !strings.Contains(e.reqs[0], "Anja mostra ARGO") {
		t.Error("la richiesta non e' nel corpo")
	}
	o := judgeParse(t, out)
	if o.Judgment == nil || o.Judgment.Verdict != "aderente" {
		t.Errorf("giudizio: %s", out)
	}
}

func TestJudgeConRiferimentoCinqueImmagini(t *testing.T) {
	e := newJudgeEnv(t, geminiText(judgeVerdict("aderente", "")))
	_, se, code := judgeRun(t, "--video", e.video, "--judge", "--prompt", "x", "--ref", e.ref)
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if n := len(judgeInline.FindAllString(e.reqs[0], -1)); n != 5 {
		t.Errorf("immagini inviate con --ref: %d", n)
	}
}

func TestJudgeNonAderenteConMotivo(t *testing.T) {
	e := newJudgeEnv(t, geminiText(judgeVerdict("non_aderente", `"il prodotto ha un dettaglio diverso"`)))
	out, se, code := judgeRun(t, "--video", e.video, "--judge", "--prompt", "x")
	if code != 0 && code != 1 {
		t.Fatalf("code=%d %s", code, se)
	}
	o := judgeParse(t, out)
	if o.Judgment == nil || o.Judgment.Verdict != "non aderente" || len(o.Judgment.Reasons) == 0 ||
		!strings.Contains(o.Judgment.Reasons[0], "dettaglio") {
		t.Errorf("giudizio: %s", out)
	}
}

func TestJudgeRispostaIlleggibileIncerto(t *testing.T) {
	for name, body := range map[string]string{"testo": "ciao, non e' json", "vuota": ""} {
		t.Run(name, func(t *testing.T) {
			e := newJudgeEnv(t, geminiText(body))
			out, se, code := judgeRun(t, "--video", e.video, "--judge", "--prompt", "x")
			if code != 0 {
				t.Fatalf("code=%d %s", code, se)
			}
			if e.hits() != 1 {
				t.Errorf("invii: %d (nessun secondo invio)", e.hits())
			}
			if o := judgeParse(t, out); o.Judgment == nil || o.Judgment.Verdict != "incerto" {
				t.Errorf("giudizio: %s", out)
			}
		})
	}
}

func TestJudgeDryRunNonInvia(t *testing.T) {
	e := newJudgeEnv(t, geminiText(judgeVerdict("aderente", "")))
	out, se, code := judgeRun(t, "--video", e.video, "--judge", "--prompt", "x", "--dry-run")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if e.hits() != 0 {
		t.Fatalf("invii in dry-run: %d", e.hits())
	}
	o := judgeParse(t, out)
	if !o.DryRun || o.Request == nil || len(o.Request.Images) != 4 || o.Request.Destination == "" || o.CostEst == nil {
		t.Errorf("dry-run senza destinatario/dati/stima: %s", out)
	}
	if o.Judgment != nil {
		t.Errorf("giudizio in dry-run: %s", out)
	}
}

func TestJudgeTariffaMancanteConTetto(t *testing.T) {
	e := newJudgeEnv(t, geminiText(judgeVerdict("aderente", "")))
	_, se, code := judgeRun(t, "--video", e.video, "--judge", "--prompt", "x", "--model", "modello-senza-tariffa", "--max-cost", "1")
	if code != 2 {
		t.Fatalf("code=%d %s", code, se)
	}
	if e.hits() != 0 {
		t.Errorf("invii: %d", e.hits())
	}
}

func TestJudgeTettoEsaurito(t *testing.T) {
	e := newJudgeEnv(t, geminiText(judgeVerdict("aderente", "")))
	_, se, code := judgeRun(t, "--video", e.video, "--judge", "--prompt", "x", "--max-cost", "0.0000001")
	if code != 2 {
		t.Fatalf("code=%d %s", code, se)
	}
	if e.hits() != 0 {
		t.Errorf("invii: %d", e.hits())
	}
}

// nessun giudizio fa partire una generazione: l'unica richiesta e' generateContent.
func TestJudgeNessunaGenerazione(t *testing.T) {
	e := newJudgeEnv(t, geminiText(judgeVerdict("non_aderente", `"m"`)))
	judgeRun(t, "--video", e.video, "--judge", "--prompt", "x")
	for _, p := range e.paths {
		if !strings.HasPrefix(p, "POST ") || !strings.Contains(p, ":generateContent") || strings.Contains(p, "predictLongRunning") {
			t.Errorf("richiesta non di giudizio: %s", p)
		}
	}
	entries, _ := os.ReadDir(e.dir)
	for _, en := range entries {
		if strings.HasSuffix(en.Name(), ".stato.json") {
			t.Errorf("stato creato da qc: %s", en.Name())
		}
	}
}
