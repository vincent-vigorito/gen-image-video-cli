package main

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// V-T7: kit, parlato e voce dai testi nello storyboard, contro un server
// finto (nessuna rete, nessuna spesa).

type v7Env struct {
	dir, board, kitDir string
	srv                *httptest.Server
	tts, videos, gem   int32
	mode               string // "", "drop" (connessione chiusa), "400"
	mu                 sync.Mutex
	ttsBodies          []string
	auth               []string
	stateAtTTS         []string // stato letto mentre arriva la richiesta di voce
}

func v7WAV(sec float64) []byte {
	n := int(24000 * sec)
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+2*n))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, []uint32{16})
	binary.Write(&b, binary.LittleEndian, []uint16{1, 1})
	binary.Write(&b, binary.LittleEndian, []uint32{24000, 48000})
	binary.Write(&b, binary.LittleEndian, []uint16{2, 16})
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(2*n))
	for i := 0; i < n; i++ {
		binary.Write(&b, binary.LittleEndian, int16(3000*math.Sin(float64(i)*0.05)))
	}
	return b.Bytes()
}

func v7Copy(t *testing.T, src, dst string) {
	t.Helper()
	filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(src, p)
		if fi.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, _ := os.ReadFile(p)
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
}

// newV7Env: cartella dello spot con lo storyboard e kit (copia di examples/kit)
// in una cartella accanto.
func newV7Env(t *testing.T, board string) *v7Env {
	t.Helper()
	e := &v7Env{}
	root := t.TempDir()
	e.dir, e.kitDir = filepath.Join(root, "spot"), filepath.Join(root, "marca")
	os.MkdirAll(e.dir, 0o755)
	v7Copy(t, v7ExamplesKit, e.kitDir)
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/tts":
			b, _ := io.ReadAll(r.Body)
			atomic.AddInt32(&e.tts, 1)
			st, _ := os.ReadFile(filepath.Join(e.dir, "spot.stato.json"))
			e.mu.Lock()
			e.ttsBodies = append(e.ttsBodies, string(b))
			e.auth = append(e.auth, r.Header.Get("Authorization"))
			e.stateAtTTS = append(e.stateAtTTS, string(st))
			e.mu.Unlock()
			switch e.mode {
			case "drop":
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
			case "400":
				http.Error(w, `{"error":{"message":"no"}}`, 400)
			default:
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"audio":%q,"duration":1}`, base64.StdEncoding.EncodeToString(v7WAV(1)))
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/interactions"):
			io.Copy(io.Discard, r.Body)
			atomic.AddInt32(&e.gem, 1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"completed","outputs":[{"type":"audio","mime_type":"audio/wav","data":%q}],"usage":{"total_input_tokens":20,"total_output_tokens":50}}`, base64.StdEncoding.EncodeToString(v7WAV(1)))
		case r.Method == http.MethodPost && r.URL.Path == "/videos":
			atomic.AddInt32(&e.videos, 1)
			io.Copy(io.Discard, r.Body)
			http.Error(w, "no", 500)
		default:
			t.Errorf("richiesta inattesa %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(e.srv.Close)
	for _, p := range []string{"xai", "openrouter", "gemini"} {
		old := baseURLs[p]
		baseURLs[p] = e.srv.URL
		t.Cleanup(func() { baseURLs[p] = old })
	}
	t.Setenv("XAI_API_KEY", "fake-xai")
	t.Setenv("OPENROUTER_API_KEY", "fake-or")
	t.Setenv("GEMINI_API_KEY", "fake-gem")
	t.Setenv("HOME", t.TempDir())
	t.Chdir(e.dir)
	e.board = filepath.Join(e.dir, "spot.json")
	rel, _ := filepath.Rel(e.dir, filepath.Join(e.kitDir, "kit.json"))
	board = strings.ReplaceAll(board, "KIT", filepath.ToSlash(rel))
	if err := os.WriteFile(e.board, []byte(board), 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Create(filepath.Join(e.dir, "a.png"))
	png.Encode(f, image.NewRGBA(image.Rect(0, 0, 16, 9)))
	f.Close()
	return e
}

func (e *v7Env) story(t *testing.T, args ...string) (string, string, int) {
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
	err := cmdStory(append(args, e.board))
	out, errOut := so(), se()
	code := 0
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return out, errOut, code
}

func (e *v7Env) state(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, "spot.stato.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m["scenes"].(map[string]any)
}

// v7ExamplesKit: percorso assoluto, preso prima che i test cambino cartella.
var v7ExamplesKit, _ = filepath.Abs("../../examples/kit")

const v7Text = "Ciao a tutti, questa e' la voce dello spot."

func v7Board(extra string) string {
	return `{"kit":"KIT","default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25}` + extra + `,
"scene":[{"id":"s1","tipo":"immagine","file":"a.png","durata":3,"testo":"` + v7Text + `"}]}`
}

func v7Chars() int { return len([]rune(v7Text)) }

// storyboard con kit e parlato: story check passa, mostra provenienza, stima
// e stato della voce, senza richieste.
func TestV7CheckKitEParlato(t *testing.T) {
	e := newV7Env(t, v7Board(`,"parlato":{"provider":"xai","voce":"eve","lingua":"it"}`))
	out, se, code := e.story(t, "check")
	if code != 0 {
		t.Fatalf("check code=%d\n%s", code, se)
	}
	var r struct {
		Kit struct {
			File      string
			Inherited []string
		}
		Scenes []struct {
			ID          string
			TextChars   int     `json:"text_chars"`
			VoiceStatus string  `json:"voice_status"`
			VoiceCost   float64 `json:"voice_cost_est_usd"`
			VoiceDur    float64 `json:"voice_duration_est_s"`
		}
		VoiceTotal float64 `json:"voice_cost_est_usd"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r.Kit.File == "" || len(r.Kit.Inherited) == 0 {
		t.Errorf("provenienza dal kit assente: %+v", r.Kit)
	}
	want := float64(v7Chars()) * 15 / 1e6
	if len(r.Scenes) != 1 || r.Scenes[0].TextChars != v7Chars() || r.Scenes[0].VoiceStatus != "da_generare" ||
		math.Abs(r.Scenes[0].VoiceCost-want) > 1e-6 || r.Scenes[0].VoiceDur <= 0 || math.Abs(r.VoiceTotal-want) > 1e-4 {
		t.Errorf("scena/voce: %+v totale=%v atteso costo %v", r.Scenes, r.VoiceTotal, want)
	}
	if e.tts != 0 || e.videos != 0 {
		t.Errorf("check ha fatto richieste: tts=%d video=%d", e.tts, e.videos)
	}
}

// --kit vuoto = codice 2; kit mancante o fuori regola = codice 2.
func TestV7KitRifiuti(t *testing.T) {
	e := newV7Env(t, v7Board(""))
	if _, _, code := e.story(t, "check", "--kit", ""); code != 2 {
		t.Errorf("--kit vuoto: code=%d", code)
	}
	if _, _, code := e.story(t, "check", "--kit", "non-c-e/kit.json"); code != 2 {
		t.Errorf("--kit mancante: code=%d", code)
	}
	os.WriteFile(filepath.Join(e.dir, "spot.json"), []byte(strings.Replace(v7Board(""), `"kit":"KIT"`, `"kit":"https://example.com/kit.json"`, 1)), 0o644)
	if _, _, code := e.story(t, "check"); code != 2 {
		t.Errorf("kit URL: code=%d", code)
	}
}

// run --voices: la riserva e' nello stato PRIMA della richiesta, la voce e'
// salvata in voce/, il costo e' registrato, il video non parte; una seconda
// esecuzione non rifa la voce.
func TestV7VoicesRiservaPrimaDelPOST(t *testing.T) {
	e := newV7Env(t, v7Board(`,"parlato":{"provider":"xai","voce":"eve","lingua":"it"}`))
	_, se, code := e.story(t, "run", "--voices")
	if code != 0 {
		t.Fatalf("code=%d\n%s", code, se)
	}
	if e.tts != 1 || e.videos != 0 {
		t.Fatalf("tts=%d video=%d", e.tts, e.videos)
	}
	if e.auth[0] != "Bearer fake-xai" {
		t.Errorf("chiave non inviata al provider giusto")
	}
	if !strings.Contains(e.stateAtTTS[0], `"incerto"`) || !strings.Contains(e.stateAtTTS[0], `"voce"`) {
		t.Errorf("riserva non scritta prima della richiesta: %s", e.stateAtTTS[0])
	}
	if strings.Contains(e.ttsBodies[0], "xxx") || !strings.Contains(e.ttsBodies[0], "Ciao a tutti") {
		t.Errorf("corpo: %s", e.ttsBodies[0])
	}
	ss := e.state(t)["s1"].(map[string]any)
	v, _ := ss["voice"].(map[string]any)
	f, _ := v["file"].(string)
	if f == "" || ss["voice_status"] != "fatto" {
		t.Fatalf("stato: %v", ss)
	}
	if fi, err := os.Stat(filepath.Join(e.dir, f)); err != nil || fi.Size() < 1000 || !strings.HasPrefix(f, "voce/") {
		t.Errorf("file voce %q: %v", f, err)
	}
	if ch, _ := ss["charges"].([]any); len(ch) != 1 {
		t.Errorf("addebiti: %v", ss["charges"])
	}
	// seconda esecuzione: niente richieste
	if _, se, code := e.story(t, "run", "--voices"); code != 0 || e.tts != 1 {
		t.Errorf("seconda esecuzione code=%d tts=%d\n%s", code, e.tts, se)
	}
	// il kit non e' stato scritto
	if _, err := os.Stat(filepath.Join(e.kitDir, "giv-log.jsonl")); err == nil {
		t.Errorf("scritto nella cartella del kit")
	}
	entries, _ := os.ReadDir(e.kitDir)
	for _, en := range entries {
		if strings.Contains(en.Name(), "stato") || en.Name() == "voce" {
			t.Errorf("scritto nel kit: %s", en.Name())
		}
	}
}

// tetto sotto la stima della voce: codice 2, nessuna richiesta; con tetto
// sufficiente la voce parte.
func TestV7TettoVoce(t *testing.T) {
	p := `,"parlato":{"provider":"xai","voce":"eve","lingua":"it"}`
	e := newV7Env(t, v7Board(p+`,"tetto_costo_usd":0.00001`))
	if _, se, code := e.story(t, "run", "--voices"); code != 2 || e.tts != 0 {
		t.Fatalf("code=%d tts=%d\n%s", code, e.tts, se)
	}
	if _, se, code := e.story(t, "check"); code != 2 || e.tts != 0 {
		t.Errorf("check col tetto superato: code=%d\n%s", code, se)
	}
	os.WriteFile(e.board, []byte(strings.Replace(v7Board(p+`,"tetto_costo_usd":1`), "KIT", mustRel(t, e), 1)), 0o644)
	if _, se, code := e.story(t, "run", "--voices"); code != 0 || e.tts != 1 {
		t.Fatalf("code=%d tts=%d\n%s", code, e.tts, se)
	}
}

func mustRel(t *testing.T, e *v7Env) string {
	r, err := filepath.Rel(e.dir, filepath.Join(e.kitDir, "kit.json"))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(r)
}

// voce non stimabile (modello senza tariffa) con un tetto: codice 2, nessuna richiesta.
func TestV7VoceNonStimabileConTetto(t *testing.T) {
	e := newV7Env(t, v7Board(`,"parlato":{"provider":"xai","modello":"modello-senza-tariffa","voce":"eve"},"tetto_costo_usd":1`))
	if _, se, code := e.story(t, "run", "--voices"); code != 2 || e.tts != 0 {
		t.Fatalf("code=%d tts=%d\n%s", code, e.tts, se)
	}
}

// esito incerto: codice 5, la voce resta riservata e contata, nessun rilancio
// da solo; si rifa' solo con --scene s1 --new-voice.
func TestV7VoceIncertaNonRilanciata(t *testing.T) {
	e := newV7Env(t, v7Board(`,"parlato":{"provider":"xai","voce":"eve","lingua":"it"}`))
	e.mode = "drop"
	if _, se, code := e.story(t, "run", "--voices"); code != 5 || e.tts != 1 {
		t.Fatalf("code=%d tts=%d\n%s", code, e.tts, se)
	}
	ss := e.state(t)["s1"].(map[string]any)
	if ss["voice_status"] != "incerto" {
		t.Fatalf("stato: %v", ss)
	}
	if ch, _ := ss["charges"].([]any); len(ch) != 1 {
		t.Errorf("la riserva deve restare contata: %v", ss["charges"])
	}
	e.mode = ""
	for _, args := range [][]string{{"run", "--voices"}, {"run"}, {"run", "--scene", "s1"}, {"run", "--new-voice"}} {
		if _, se, code := e.story(t, args...); code != 2 || e.tts != 1 {
			t.Errorf("%v: code=%d tts=%d\n%s", args, code, e.tts, se)
		}
	}
	// check lo segnala senza richieste
	if out, _, code := e.story(t, "check"); code != 0 || !strings.Contains(out, `"incerto"`) || e.tts != 1 {
		t.Errorf("check: code=%d tts=%d", code, e.tts)
	}
	if _, se, code := e.story(t, "run", "--scene", "s1", "--new-voice"); code != 0 || e.tts != 2 {
		t.Fatalf("rilancio esplicito: code=%d tts=%d\n%s", code, e.tts, se)
	}
	if ss := e.state(t)["s1"].(map[string]any); ss["voice_status"] != "fatto" {
		t.Errorf("stato dopo il rilancio: %v", ss)
	}
}

// richiesta rifiutata (400): riserva tolta, niente addebito, da rifare.
func TestV7VoceRifiutataToglieRiserva(t *testing.T) {
	e := newV7Env(t, v7Board(`,"parlato":{"provider":"xai","voce":"eve","lingua":"it"}`))
	e.mode = "400"
	if _, _, code := e.story(t, "run", "--voices"); code == 0 || e.tts != 1 {
		t.Fatalf("code=%d tts=%d", code, e.tts)
	}
	ss := e.state(t)["s1"].(map[string]any)
	if ch, _ := ss["charges"].([]any); len(ch) != 0 || ss["voice_status"] == "incerto" {
		t.Errorf("riserva rimasta: %v", ss)
	}
	e.mode = ""
	if _, se, code := e.story(t, "run", "--voices"); code != 0 || e.tts != 2 {
		t.Fatalf("code=%d tts=%d\n%s", code, e.tts, se)
	}
}

// --new-voice senza --scene = codice 2.
func TestV7NewVoiceSenzaScene(t *testing.T) {
	e := newV7Env(t, v7Board(`,"parlato":{"provider":"xai","voce":"eve","lingua":"it"}`))
	if _, _, code := e.story(t, "run", "--new-voice"); code != 2 || e.tts != 0 {
		t.Errorf("code=%d tts=%d", code, e.tts)
	}
}

// storyboard della 0.11.0 (senza kit, parlato, testo): nessuna richiesta di voce,
// il check non cambia e non porta campi nuovi.
func TestV7StoryboardSenzaCampiNuovi(t *testing.T) {
	board := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},
"scene":[{"id":"s1","tipo":"immagine","file":"a.png","durata":3}]}`
	e := newV7Env(t, board)
	out, se, code := e.story(t, "check")
	if code != 0 {
		t.Fatalf("code=%d\n%s", code, se)
	}
	for _, k := range []string{`"kit"`, `voice_`, `text_chars`} {
		if strings.Contains(out, k) {
			t.Errorf("campo nuovo %s in un check senza kit/parlato/testo", k)
		}
	}
	if _, se, code := e.story(t, "run"); code != 0 || e.tts != 0 || e.videos != 0 {
		t.Errorf("run code=%d tts=%d video=%d\n%s", code, e.tts, e.videos, se)
	}
}

// con il kit, cut mette cartello finale (3 s) e il resto in automatico:
// durata = scena + cartello (montaggio.senza e' di V-T8).
func TestV7CutKitAutomatico(t *testing.T) {
	cutNeedFF(t)
	e := newV7Env(t, v7Board(`,"parlato":{"provider":"xai","voce":"eve","lingua":"it"}`))
	if _, se, code := e.story(t, "run", "--voices"); code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	dur := func() float64 {
		out, se, code := e.story(t, "cut")
		if code != 0 {
			t.Fatalf("cut code=%d\n%s", code, se)
		}
		var r struct {
			Output string
		}
		json.Unmarshal([]byte(out), &r)
		f := r.Output
		if f == "" {
			t.Fatalf("nessun file nell'uscita: %s", out)
		}
		b, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", f).Output()
		if err != nil {
			t.Fatal(err)
		}
		d, _ := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		return d
	}
	if d := dur(); d < 5.5 || d > 6.8 {
		t.Errorf("durata con kit %v, attesa circa 6 (3 + cartello 3)", d)
	}
}

// cut con voce mancante: codice 2 che rimanda a giv story run --voices.
func TestV7CutSenzaVoceRifiuta(t *testing.T) {
	e := newV7Env(t, v7Board(`,"parlato":{"provider":"xai","voce":"eve","lingua":"it"}`))
	if _, se, code := e.story(t, "cut"); code != 2 || !strings.Contains(se, "--voices") {
		t.Errorf("code=%d\n%s", code, se)
	}
}

const v7Parlato = `,"parlato":{"provider":"xai","voce":"eve","lingua":"it"}`

// voce incerta, poi --new-voice rifiutata dal provider (4xx): resta incerta e
// contata (la riserva del tentativo si toglie); un run ordinario dopo resta a
// codice 2 senza richieste.
func TestV7NewVoiceRifiutataRestaIncerta(t *testing.T) {
	e := newV7Env(t, v7Board(v7Parlato))
	e.mode = "drop"
	if _, _, code := e.story(t, "run", "--voices"); code != 5 {
		t.Fatalf("code=%d", code)
	}
	e.mode = "400"
	if _, _, code := e.story(t, "run", "--scene", "s1", "--new-voice"); code == 0 || e.tts != 2 {
		t.Fatalf("code=%d tts=%d", code, e.tts)
	}
	ss := e.state(t)["s1"].(map[string]any)
	if ss["voice_status"] != "incerto" {
		t.Errorf("deve restare incerta: %v", ss)
	}
	if ch, _ := ss["charges"].([]any); len(ch) != 1 {
		t.Errorf("riserva del tentativo non tolta o addebito perso: %v", ss["charges"])
	}
	e.mode = ""
	if _, se, code := e.story(t, "run", "--voices"); code != 2 || e.tts != 2 {
		t.Errorf("run ordinario: code=%d tts=%d\n%s", code, e.tts, se)
	}
}

// un job pendente con indirizzo di attesa non valido ferma tutto a codice 2
// prima di ogni voce.
func TestV7JobPendenteNonValidoPrimaDelleVoci(t *testing.T) {
	board := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"}` + v7Parlato + `,
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},
"scene":[{"id":"s1","tipo":"immagine","file":"a.png","durata":3,"testo":"` + v7Text + `"},
{"id":"s2","tipo":"video","durata":5,"prompt":"un gatto"}]}`
	e := newV7Env(t, board)
	st := `{"version":1,"scenes":{"s2":{"status":"partito","job":{"provider":"openrouter","id":"job-9","polling_url":"https://evil.example.com/videos/job-9","model":"alibaba/wan-3.0","started":"2026-01-01T00:00:00Z"},"charges":[{"kind":"clip","job_id":"job-9","cost_est_usd":0.5,"at":"2026-01-01T00:00:00Z"}]}}}`
	os.WriteFile(filepath.Join(e.dir, "spot.stato.json"), []byte(st), 0o600)
	if _, se, code := e.story(t, "run", "--voices"); code != 2 || e.tts != 0 || e.videos != 0 {
		t.Fatalf("code=%d tts=%d video=%d\n%s", code, e.tts, e.videos, se)
	}
}

// il percorso assoluto di un file del kit scritto a mano nello storyboard e'
// rifiutato; i campi riempiti dal kit restano validi.
func TestV7AssolutoDelKitScrittoAMano(t *testing.T) {
	e := newV7Env(t, v7Board(""))
	abs := filepath.Join(e.kitDir, "logo.png")
	b := strings.Replace(v7Board(""), `"file":"a.png"`, `"file":"`+abs+`"`, 1)
	b = strings.Replace(b, "KIT", mustRel(t, e), 1)
	os.WriteFile(e.board, []byte(b), 0o644)
	if _, se, code := e.story(t, "check"); code != 2 || !strings.Contains(se, "assoluto") {
		t.Errorf("code=%d\n%s", code, se)
	}
	os.WriteFile(e.board, []byte(strings.Replace(v7Board(""), "KIT", mustRel(t, e), 1)), 0o644)
	if _, se, code := e.story(t, "check"); code != 0 {
		t.Errorf("storyboard valido col kit: code=%d\n%s", code, se)
	}
}

// una voce registrata nello stato che e' un link fuori cartella: check non la
// segue (avviso, nessun errore fatale).
func TestV7VoceNelloStatoConLinkFuori(t *testing.T) {
	e := newV7Env(t, v7Board(v7Parlato))
	out := filepath.Join(filepath.Dir(e.dir), "fuori.wav")
	os.WriteFile(out, v7WAV(1), 0o644)
	os.MkdirAll(filepath.Join(e.dir, "voce"), 0o755)
	if err := os.Symlink(out, filepath.Join(e.dir, "voce", "s1-v1.wav")); err != nil {
		t.Skip(err)
	}
	st := `{"version":1,"scenes":{"s1":{"status":"","voice":{"file":"voce/s1-v1.wav","done":"2026-01-01T00:00:00Z"},"voice_status":"fatto","voice_key":"x","charges":[{"kind":"voce","cost_est_usd":0.0006,"at":"2026-01-01T00:00:00Z"}]}}}`
	os.WriteFile(filepath.Join(e.dir, "spot.stato.json"), []byte(st), 0o600)
	o, se, code := e.story(t, "check")
	if code != 0 && code != 2 {
		t.Fatalf("code=%d", code)
	}
	if code == 0 && !strings.Contains(o+se, "voce") {
		t.Errorf("nessun avviso sulla voce fuori cartella:\n%s\n%s", o, se)
	}
	if e.tts != 0 {
		t.Errorf("richieste: %d", e.tts)
	}
}
