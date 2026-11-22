package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeOR è un OpenRouter finto: serve il catalogo e conta le richieste di avvio.
func fakeOR(t *testing.T, catalog []byte) (starts *int32, base string) {
	t.Helper()
	starts = new(int32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/videos/models":
			w.Header().Set("Content-Type", "application/json")
			w.Write(catalog)
		case r.Method == http.MethodPost && r.URL.Path == "/videos":
			atomic.AddInt32(starts, 1)
			http.Error(w, "stop", http.StatusBadRequest) // niente polling
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, ".png"):
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("\x89PNG\r\n\x1a\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := baseURLs["openrouter"]
	baseURLs["openrouter"] = srv.URL
	t.Cleanup(func() { baseURLs["openrouter"] = old })
	t.Setenv("OPENROUTER_API_KEY", "fake")
	t.Chdir(t.TempDir())
	return starts, srv.URL
}

// runVideo lancia cmdVideo in-process e ne ritorna stdout, codice d'uscita ed errore.
func runVideo(t *testing.T, args ...string) (string, int, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := cmdVideo(args)
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	code := 0
	if err != nil {
		code, _ = exitCode(err)
	}
	return string(b), code, err
}

var catalogPath, _ = filepath.Abs("testdata/or-video-models.json")

// writePNG crea due immagini minime nella cartella corrente (file locali: nessun download).
func writePNG(t *testing.T) {
	t.Helper()
	png := []byte("\x89PNG\r\n\x1a\n")
	for _, n := range []string{"a.png", "b.png"} {
		if err := os.WriteFile(n, png, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func catalogFile(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func costOf(t *testing.T, out string) float64 {
	t.Helper()
	var p struct {
		Cost *float64 `json:"cost_est_usd"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil || p.Cost == nil {
		t.Fatalf("piano senza stima: %q %v", out, err)
	}
	return *p.Cost
}

const grok = "x-ai/grok-imagine-video-1.5"

// CLI-879: image e ref insieme non sono ammessi su grok via OpenRouter.
func TestVideoGrokImageERefInsiemeRifiutato(t *testing.T) {
	for _, dry := range []bool{false, true} {
		starts, _ := fakeOR(t, catalogFile(t))
		writePNG(t)
		args := []string{"--provider", "openrouter", "--model", grok,
			"--image", "a.png", "--ref", "b.png", "un prompt"}
		if dry {
			args = append([]string{"--dry-run"}, args...)
		}
		_, code, err := runVideo(t, args...)
		if code != 2 || err == nil {
			t.Errorf("dry=%v: code=%d err=%v, atteso 2", dry, code, err)
		}
		if n := atomic.LoadInt32(starts); n != 0 {
			t.Errorf("dry=%v: %d richieste di avvio, attese 0", dry, n)
		}
	}
}

// CLI-879: solo image o solo ref passano come oggi (nessun rifiuto d'uso).
func TestVideoGrokSoloImageOSoloRefPassa(t *testing.T) {
	for _, flagName := range []string{"image", "ref"} {
		name := flagName
		starts, _ := fakeOR(t, catalogFile(t))
		writePNG(t)
		extra := []string{"--" + flagName, "a.png", "--duration", "5"}
		args := append([]string{"--provider", "openrouter", "--model", grok, "--dry-run"}, extra...)
		out, code, err := runVideo(t, append(args, "un prompt")...)
		if code == 2 || err != nil || !strings.Contains(out, `"dry_run": true`) && !strings.Contains(out, `"dry_run":true`) {
			t.Errorf("%s dry-run: code=%d err=%v out=%s", name, code, err, out)
		}
		if atomic.LoadInt32(starts) != 0 {
			t.Errorf("%s: dry-run ha avviato un job", name)
		}
		// senza --dry-run arriva fino alla richiesta di avvio (il server finto la rifiuta)
		_, code, err = runVideo(t, append(append([]string{"--provider", "openrouter", "--model", grok}, extra...), "un prompt")...)
		if code == 2 || atomic.LoadInt32(starts) != 1 {
			t.Errorf("%s: code=%d err=%v avvii=%d, attesi code!=2 e 1 avvio", name, code, err, atomic.LoadInt32(starts))
		}
	}
}

// catalogo con la tariffa 1080p mancante: ammessa dal modello ma non nel listino
func catalogSenzaTariffa(t *testing.T) []byte {
	t.Helper()
	var d struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(catalogFile(t), &d); err != nil {
		t.Fatal(err)
	}
	for _, m := range d.Data {
		if m["id"] == grok {
			delete(m["pricing_skus"].(map[string]any), "cents_per_video_output_second_1080p")
			d.Data = []map[string]any{m}
			b, _ := json.Marshal(d)
			return b
		}
	}
	t.Fatal("grok assente dal catalogo")
	return nil
}

// CLI-867: con --max-cost e una tariffa mancante si rifiuta prima dell'avvio.
func TestVideoTariffaMancanteConMaxCostRifiutata(t *testing.T) {
	for _, dry := range []bool{false, true} {
		starts, _ := fakeOR(t, catalogSenzaTariffa(t))
		args := []string{"--provider", "openrouter", "--model", grok, "--duration", "5", "--max-cost", "100", "un prompt"}
		if dry {
			args = append([]string{"--dry-run"}, args...)
		}
		_, code, err := runVideo(t, args...)
		if code != 2 || err == nil {
			t.Errorf("dry=%v: code=%d err=%v, atteso 2", dry, code, err)
		}
		if atomic.LoadInt32(starts) != 0 {
			t.Errorf("dry=%v: job avviato", dry)
		}
	}
}

// CLI-867: senza --max-cost il comportamento resta quello di oggi (nessun rifiuto d'uso).
func TestVideoTariffaMancanteSenzaMaxCostNonRifiuta(t *testing.T) {
	starts, _ := fakeOR(t, catalogSenzaTariffa(t))
	_, code, _ := runVideo(t, "--provider", "openrouter", "--model", grok, "--duration", "5", "un prompt")
	if code == 2 || atomic.LoadInt32(starts) != 1 {
		t.Errorf("code=%d avvii=%d, attesi code!=2 e 1 avvio", code, atomic.LoadInt32(starts))
	}
}

// CLI-867: le stime già coperte non cambiano.
func TestVideoStimeCoperteInvariate(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
		want  float64
	}{
		{"kling pro senza audio", []string{"--model", "kwaivgi/kling-v3.0-pro", "--audio", "off"}, 0.56},
		{"kling pro con audio", []string{"--model", "kwaivgi/kling-v3.0-pro", "--audio", "on"}, 0.84},
		{"omni 720p", []string{"--model", "google/gemini-omni-1.2-flash", "--resolution", "720p"}, 0.3475},
	}
	for _, c := range cases {
		fakeOR(t, catalogFile(t))
		args := append([]string{"--provider", "openrouter", "--dry-run", "--duration", "5"}, c.extra...)
		out, code, err := runVideo(t, append(args, "un prompt")...)
		if code != 0 || err != nil {
			t.Errorf("%s: code=%d err=%v", c.name, code, err)
			continue
		}
		if got := costOf(t, out); got != c.want {
			t.Errorf("%s: stima %v, attesa %v", c.name, got, c.want)
		}
	}
}
