package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// CLI-1015 (giv 0.13.0): integrazione in story run, contro un OpenRouter
// finto (parEnv, story_parallel_test.go).

func listFiles(dir string) []string {
	var files []string
	filepath.Walk(dir, func(p string, i os.FileInfo, err error) error {
		if err == nil && !i.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, stamp.ReplaceAllString(rel, "-T"))
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// storyboard 0.12.x senza novità: richieste e file uguali con e senza
// --parallel 1; nessun file nuovo (bozze, approvazioni, giudizi).
func TestIntegrazioneStoryboard012xInvariato(t *testing.T) {
	e := newParEnv(t, sb("", ps("s1", ""), ps("s2", "")), false)
	out, se, code := e.run(t)
	if code != 0 || e.posts != 2 || out.Started != 2 {
		t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
	}
	for _, f := range listFiles(e.dir) {
		if strings.Contains(f, "bozza") || strings.Contains(f, "giudizi") {
			t.Errorf("file nuovo in uno storyboard senza novità: %s", f)
		}
	}
	// seconda esecuzione: niente da fare, nessuna richiesta in più
	if _, se, code := e.run(t); code != 0 || e.posts != 2 {
		t.Fatalf("seconda: code=%d posts=%d\n%s", code, e.posts, se)
	}
}

// --keyframes con --bozza, e --bozza senza default.bozza: codice 2 senza richieste.
func TestIntegrazioneOpzioniRifiutate(t *testing.T) {
	board := sb("", ps("s1", ""))
	for name, args := range map[string][]string{
		"keyframes e bozza":        {"--keyframes", "--bozza"},
		"bozza senza profilo":      {"--bozza"},
		"bozza senza profilo (p2)": {"--bozza", "--parallel", "2"},
	} {
		e := newParEnv(t, board, false)
		_, se, code := e.run(t, args...)
		if code != 2 || e.reqs != 0 {
			t.Errorf("%s: code=%d richieste=%d\n%s", name, code, e.reqs, se)
		}
	}
}

// ib è uno storyboard con default esteso (dflt), campi di primo livello (top)
// e le scene date.
func ib(dflt, top string, scene ...string) string {
	return `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"` + dflt + `}` +
		top + `,"scene":[` + strings.Join(scene, ",") + `]}`
}

const keyfr = `,"fotogramma_chiave":{"prompt":"un gatto fermo","costo_dichiarato_usd":0.14}`

// rawRun lancia story run e ritorna stdout grezzo, stderr e codice.
func (e *parEnv) rawRun(t *testing.T, args ...string) (stdout, errOut string, code int) {
	t.Helper()
	so, se := grabFile(&os.Stdout), grabFile(&os.Stderr)
	err := cmdStory(append([]string{"run"}, append(args, e.board)...))
	stdout, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

// realMP4 è un video vero di un secondo, per il giudice (estrae fotogrammi con ffmpeg).
func realMP4(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg mancante")
	}
	out := filepath.Join(t.TempDir(), "v.mp4")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=128x72:rate=10:duration=1",
		"-pix_fmt", "yuv420p", "-y", out).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg: %v %s", err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (e *parEnv) withJudge(t *testing.T) {
	t.Helper()
	e.mp4 = realMP4(t)
	old := baseURLs["gemini"]
	baseURLs["gemini"] = e.srv.URL
	t.Cleanup(func() { baseURLs["gemini"] = old })
	t.Setenv("GEMINI_API_KEY", "fake")
}

// --keyframes: solo immagini, nessun video, foglio delle approvazioni.
func TestIntegrazioneKeyframesSoloImmagini(t *testing.T) {
	e := newParEnv(t, ib("", `,"approvazione":true`, ps("s1", keyfr), ps("s2", keyfr)), false)
	so, se, code := e.rawRun(t, "--keyframes")
	if code != 0 || e.images != 2 || e.posts != 0 || e.polls != 0 {
		t.Fatalf("code=%d images=%d posts=%d polls=%d\n%s", code, e.images, e.posts, e.polls, se)
	}
	if !strings.Contains(so, `"keyframes"`) || !strings.Contains(so, `"mode": "keyframes"`) && !strings.Contains(so, `"mode":"keyframes"`) {
		t.Errorf("uscita senza mode keyframes: %s", so)
	}
	found := false
	for _, f := range listFiles(e.dir) {
		if strings.Contains(f, "foglio-approvazione") {
			found = true
		}
		if strings.HasSuffix(f, ".mp4") {
			t.Errorf("video generato con --keyframes: %s", f)
		}
	}
	if !found {
		t.Errorf("foglio delle approvazioni mancante: %v", listFiles(e.dir))
	}
}

// due su tre approvate: il definitivo solo per loro, la terza «da approvare»;
// --scene sulla non approvata: codice 2 senza richieste.
func TestIntegrazioneApprovazioneDueSuTre(t *testing.T) {
	e := newParEnv(t, ib("", `,"approvazione":true`, ps("s1", keyfr), ps("s2", keyfr), ps("s3", keyfr)), false)
	if _, se, code := e.rawRun(t, "--keyframes"); code != 0 || e.images != 3 {
		t.Fatalf("keyframes: code=%d images=%d\n%s", code, e.images, se)
	}
	so, se := grabFile(&os.Stdout), grabFile(&os.Stderr)
	err := cmdStory([]string{"approve", e.board, "s1", "s2"})
	so()
	if es := se(); err != nil {
		t.Fatalf("approve: %v %s", err, es)
	}
	out, se2, code := e.rawRun(t)
	if code != 0 || e.posts != 2 {
		t.Fatalf("run: code=%d posts=%d\n%s\n%s", code, e.posts, out, se2)
	}
	if !strings.Contains(out, "da approvare") || !strings.Contains(out, "awaiting_approval") {
		t.Errorf("s3 non elencata come da approvare: %s", out)
	}
	if got := e.bodyOrder(); got != "s1,s2" {
		t.Errorf("avvii %s", got)
	}
	imgs, posts := e.images, e.posts
	_, se3, code := e.rawRun(t, "--scene", "s3")
	if code != 2 || e.posts != posts || e.images != imgs {
		t.Errorf("--scene s3: code=%d posts=%d images=%d\n%s", code, e.posts, e.images, se3)
	}
}

// --bozza --parallel 2: bozze col profilo di bozza (480p), spesa come bozza,
// tetto rispettato, nessun definitivo.
func TestIntegrazioneBozzaParallela(t *testing.T) {
	e := newParEnv(t, ib(`,"bozza":{"modello":"alibaba/wan-3.0","risoluzione":"480p"}`, `,"tetto_costo_usd":0.6`, ps("s1", ""), ps("s2", "")), false)
	e.cost = 0.25
	so, se, code := e.rawRun(t, "--bozza", "--parallel", "2")
	if code != 0 || e.posts != 2 {
		t.Fatalf("code=%d posts=%d\n%s\n%s", code, e.posts, so, se)
	}
	for _, b := range e.bodies {
		if !strings.Contains(b, "480p") || strings.Contains(b, "720p") {
			t.Errorf("la bozza non usa il profilo di bozza: %.300s", b)
		}
	}
	if !strings.Contains(so, "bozza") {
		t.Errorf("uscita senza mode bozza: %s", so)
	}
	raw, _ := os.ReadFile(e.statePath())
	var st struct {
		Scenes map[string]struct {
			Clip    any `json:"clip"`
			Draft   any `json:"draft"`
			Charges []struct {
				Kind    string  `json:"kind"`
				CostEst float64 `json:"cost_est_usd"`
				Cost    float64 `json:"cost_usd"`
			} `json:"charges"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	sum := 0.0
	for id, s := range st.Scenes {
		if s.Draft == nil || s.Clip != nil {
			t.Errorf("%s: draft=%v clip=%v", id, s.Draft, s.Clip)
		}
		for _, c := range s.Charges {
			if c.Kind != "bozza" {
				t.Errorf("%s: spesa di tipo %q, atteso bozza", id, c.Kind)
			}
			if c.Cost > 0 {
				sum += c.Cost
			} else {
				sum += c.CostEst
			}
		}
	}
	if sum == 0 || sum > 0.6 {
		t.Errorf("spesa registrata %v, tetto 0.6", sum)
	}
	ents, _ := os.ReadDir(filepath.Join(e.dir, "clip", "bozza"))
	if len(ents) != 2 {
		t.Errorf("file in clip/bozza: %d", len(ents))
	}
}

// aderenza accesa: un giudizio per ripresa, nessun reinvio alla seconda
// esecuzione; con risposta illeggibile (incerto) nemmeno.
func TestIntegrazioneAderenzaUnGiudizioPerRipresa(t *testing.T) {
	for name, txt := range map[string]string{"aderente": "", "incerto": "non è json"} {
		t.Run(name, func(t *testing.T) {
			e := newParEnv(t, ib("", `,"tetto_costo_usd":3,"controllo":{"aderenza":true}`, ps("s1", ""), ps("s2", "")), false)
			e.withJudge(t)
			e.judgeTxt = txt
			so, se, code := e.rawRun(t)
			if code != 0 || e.posts != 2 || e.judges != 2 {
				t.Fatalf("code=%d posts=%d giudizi=%d\n%s\n%s", code, e.posts, e.judges, so, se)
			}
			if !strings.Contains(so, `"judgements"`) {
				t.Errorf("uscita senza judgements: %s", so)
			}
			if _, se, code := e.rawRun(t); code != 0 || e.posts != 2 || e.judges != 2 {
				t.Errorf("seconda: code=%d posts=%d giudizi=%d\n%s", code, e.posts, e.judges, se)
			}
		})
	}
}

// aderenza spenta (assente): zero invii al giudice.
func TestIntegrazioneAderenzaSpentaZeroInvii(t *testing.T) {
	e := newParEnv(t, ib("", `,"tetto_costo_usd":3`, ps("s1", "")), false)
	e.withJudge(t)
	if _, se, code := e.rawRun(t); code != 0 || e.posts != 1 || e.judges != 0 {
		t.Fatalf("code=%d posts=%d giudizi=%d\n%s", code, e.posts, e.judges, se)
	}
}

// il tetto basta per la scena ma non per il giudizio: il giudizio non parte.
func TestIntegrazioneTettoNonBastaPerIGiudizi(t *testing.T) {
	e := newParEnv(t, ib("", `,"tetto_costo_usd":0.6005,"controllo":{"aderenza":true}`, ps("s1", "")), false)
	e.withJudge(t)
	e.cost = 0.6 // costo reale sopra la stima 0.5: resta meno del costo del giudizio
	so, se, code := e.rawRun(t)
	if e.judges != 0 || e.posts != 1 {
		t.Fatalf("code=%d posts=%d giudizi=%d\n%s\n%s", code, e.posts, e.judges, so, se)
	}
	t.Logf("code=%d\n%s\n%s", code, so, se)
}

// costo non stimabile con tetto (tariffa del video assente), aderenza accesa:
// codice 2 prima di ogni avvio e nessun invio al giudice.
func TestIntegrazioneNonStimabileConTetto(t *testing.T) {
	e := newParEnv(t, ib("", `,"tetto_costo_usd":5,"controllo":{"aderenza":true}`, ps("s1", "")), true)
	e.withJudge(t)
	_, se, code := e.rawRun(t)
	if code != 2 || e.posts != 0 || e.judges != 0 {
		t.Fatalf("code=%d posts=%d giudizi=%d\n%s", code, e.posts, e.judges, se)
	}
}
