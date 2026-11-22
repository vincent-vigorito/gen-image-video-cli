package main

import (
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// F2-T4c: percorso completo check -> run (OpenRouter finto, clip sintetiche) ->
// cut. Ogni scena lanciata attende un intervallo di polling reale (10 s).

type cutRunEnv struct {
	dir   string
	board string
	posts int32
	srv   *httptest.Server
}

func newCutRunEnv(t *testing.T, boardJSON string, mp4 []byte) *cutRunEnv {
	t.Helper()
	cat, err := os.ReadFile("testdata/or-video-models.json")
	if err != nil {
		t.Fatal(err)
	}
	e := &cutRunEnv{}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/videos/models"):
			w.Write(cat)
		case r.Method == http.MethodPost && r.URL.Path == "/videos":
			io.Copy(io.Discard, r.Body)
			n := atomic.AddInt32(&e.posts, 1)
			fmt.Fprintf(w, `{"id":"job-%d","polling_url":%q,"status":"pending"}`, n, e.srv.URL+"/videos/job-"+fmt.Sprint(n))
		case strings.HasPrefix(r.URL.Path, "/videos/job-"):
			fmt.Fprintf(w, `{"status":"completed","unsigned_urls":[%q],"usage":{"cost":0.5}}`, e.srv.URL+"/content")
		case r.URL.Path == "/content":
			w.Write(mp4)
		default:
			t.Errorf("richiesta inattesa %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
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

func TestStoryCheckRunCutPercorsoCompleto(t *testing.T) {
	cutNeedFF(t)
	tmp := t.TempDir()
	src := filepath.Join(tmp, "srv.mp4")
	cutClip(t, src, "160x90", 24, "5")
	mp4, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	board := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},
"scene":[` + sc("s1", "") + `,` + sc("s2", "") + `,` + sc("s3", "") + `,{"id":"fine","tipo":"immagine","file":"fine.png","durata":2}]}`
	e := newCutRunEnv(t, board, mp4)
	f, _ := os.Create(filepath.Join(e.dir, "fine.png"))
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 8, 8)))
	f.Close()

	// check
	if _, se, code := cutRun(t, "check", e.board); code != 0 {
		t.Fatalf("check code=%d\n%s", code, se)
	}
	if e.posts != 0 {
		t.Fatalf("check ha avviato job: %d", e.posts)
	}
	// cut prima di run: rifiuto, nessuna uscita
	if _, se, code := cutRun(t, "cut", e.board); code != 2 || !strings.Contains(se, "s1") {
		t.Errorf("cut senza clip: code=%d %q", code, se)
	}
	// run: un avvio per scena video
	if _, se, code := cutRun(t, "run", e.board); code != 0 || e.posts != 3 {
		t.Fatalf("run code=%d posts=%d\n%s", code, e.posts, se)
	}
	// cut: un file, durata 3x5 + 2 = 17 s
	out, se, code := cutRun(t, "cut", e.board)
	if code != 0 {
		t.Fatalf("cut code=%d\n%s", code, se)
	}
	var r struct {
		Output     string  `json:"output"`
		DurationS  float64 `json:"duration_s"`
		Height     int     `json:"height"`
		VideoCodec string  `json:"video_codec"`
		AudioCodec string  `json:"audio_codec"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if math.Abs(r.DurationS-17) > 0.1 || r.Height != 144 || r.VideoCodec != "h264" || r.AudioCodec != "aac" {
		t.Errorf("risultato %+v", r)
	}
	if _, err := os.Stat(r.Output); err != nil {
		t.Errorf("uscita assente: %v", err)
	}
	if e.posts != 3 {
		t.Errorf("il cut ha lanciato avvii: %d", e.posts)
	}
}

func TestStoryCutSceneVideoMancante(t *testing.T) {
	cutNeedFF(t)
	e := newCutRunEnv(t, sb("", sc("s1", ""), sc("s2", "")), nil)
	if _, se, code := cutRun(t, "cut", e.board); code != 2 || !strings.Contains(se, "s1") || !strings.Contains(se, "s2") {
		t.Errorf("code=%d (atteso 2 con le scene mancanti): %q", code, se)
	}
	// una scena fatta (stato + clip) e una no: indica solo quella mancante
	cutClip(t, filepath.Join(e.dir, "clip-s1.mp4"), "160x90", 24, "5")
	st := `{"version":1,"scenes":{"s1":{"status":"fatto","clip":{"file":"clip-s1.mp4","job_id":"j0","done":"2026-01-01T00:00:00Z"}}}}`
	if err := os.WriteFile(filepath.Join(e.dir, "spot.stato.json"), []byte(st), 0o600); err != nil {
		t.Fatal(err)
	}
	_, se, code := cutRun(t, "cut", e.board)
	if code != 2 || !strings.Contains(se, "scena s2") || strings.Contains(se, "scena s1") {
		t.Errorf("code=%d: %q", code, se)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "spot.mp4")); err == nil {
		t.Errorf("uscita scritta con una scena mancante")
	}
}
