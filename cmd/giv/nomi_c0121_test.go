package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// giv 0.12.1 filone C (CLI-998): nomi propri come parole chiave, confronto
// delle voci nel controllo dopo story cut, costo di giv voice xAI.
// Solo server finti, nessuna spesa.

// sttKeyterms: handler /stt che registra i campi "keyterm" di ogni richiesta.
type sttSpy struct {
	mu    sync.Mutex
	terms [][]string
}

func (s *sttSpy) handler(text string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		var got []string
		mr := multipart.NewReader(r.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(p)
			if p.FormName() == "keyterm" {
				got = append(got, string(b))
			}
		}
		s.mu.Lock()
		s.terms = append(s.terms, got)
		s.mu.Unlock()
		sttResp(text)(w, r)
	}
}

func TestC0121QCVoiceNomiEDizionarioComeKeyterm(t *testing.T) {
	spy := &sttSpy{}
	hits, dir := voiceEnv(t, spy.handler("Ciao Swerpify"))
	aud := qcAudio(t, dir)
	dict := filepath.Join(dir, "p.json")
	os.WriteFile(dict, []byte(`{"Swerpify":"suèrpifai"}`), 0o600)
	_, se, code := qcVoiceRun(t, aud, "--script", "Ciao Swerpify", "--pronuncia", dict,
		"--nome", "Anja Rossi", "--nome", "ARGO")
	if code != 0 || *hits != 1 {
		t.Fatalf("code=%d hits=%d %s", code, *hits, se)
	}
	want := []string{"Anja Rossi", "ARGO", "Swerpify"}
	if len(spy.terms) != 1 || !reflect.DeepEqual(spy.terms[0], want) {
		t.Errorf("keyterm=%v, attesi %v", spy.terms, want)
	}
}

func TestC0121QCVoiceNomiRifiutiCodice2SenzaRichieste(t *testing.T) {
	hits, dir := voiceEnv(t, sttResp("x"))
	aud := qcAudio(t, dir)
	var many []string
	for i := 0; i < 101; i++ {
		many = append(many, "--nome", fmt.Sprintf("N%d", i))
	}
	cases := map[string][]string{
		"vuoto":      {"--nome", ""},
		"troppo":     {"--nome", strings.Repeat("a", 51)},
		"controllo":  {"--nome", "Anja\nRossi"},
		"invisibile": {"--nome", "An​ja"},
		"oltre 100":  many,
	}
	for name, extra := range cases {
		args := append([]string{aud, "--script", "x"}, extra...)
		if _, se, code := qcVoiceRun(t, args...); code != 2 {
			t.Errorf("%s: code=%d %s", name, code, se)
		}
	}
	if *hits != 0 {
		t.Errorf("richieste=%d", *hits)
	}
}

// kit: nomi validi passano; kit 0.12.0 senza il campo passa; campo mal formato = 2.
func TestC0121KitNomi(t *testing.T) {
	check := func(kit string) (string, int) {
		d := v3Dir(t, kit)
		so, se, code := v3Run(t, t.TempDir(), "kit", "check", filepath.Join(d, "kit.json"))
		return so + se, code
	}
	if o, code := check(v3Kit); code != 0 {
		t.Fatalf("kit 0.12.0 senza nomi: %d %s", code, o)
	}
	if o, code := check(strings.Replace(v3Kit, `"pronuncia"`, `"nomi": ["Anja", "ARGO"], "pronuncia"`, 1)); code != 0 {
		t.Fatalf("kit con nomi: %d %s", code, o)
	}
	bad := map[string]string{
		"non array":   `"nomi": "Anja"`,
		"non stringa": `"nomi": [1]`,
		"vuoto":       `"nomi": [""]`,
		"lungo":       `"nomi": ["` + strings.Repeat("a", 51) + `"]`,
		"controllo":   `"nomi": ["A\nB"]`,
		"ripetuto":    `"nomi": ["Anja", "anja"]`,
		"oltre 100":   `"nomi": [` + manyNames(101) + `]`,
	}
	for name, field := range bad {
		if o, code := check(strings.Replace(v3Kit, `"pronuncia"`, field+`, "pronuncia"`, 1)); code != 2 {
			t.Errorf("%s: code=%d %s", name, code, o)
		}
	}
}

func manyNames(n int) string {
	var s []string
	for i := 0; i < n; i++ {
		s = append(s, fmt.Sprintf(`"nome%d"`, i))
	}
	return strings.Join(s, ",")
}

// story run, controllo.voce: nomi del kit + pronunce del kit come keyterm.
func TestC0121StoryRunControlloVoceKeyterm(t *testing.T) {
	e := newV7Env(t, strings.Replace(v8VoceBoard(false, `{"voce":true}`), `{"default"`, `{"kit":"KIT","default"`, 1))
	kj := filepath.Join(e.kitDir, "kit.json")
	b, _ := os.ReadFile(kj)
	var k map[string]any
	json.Unmarshal(b, &k)
	k["nomi"] = []string{"Anja Rossi", "ARGO"}
	k["pronuncia"] = map[string]string{"Swerpify": "suèrpifai"}
	b, _ = json.Marshal(k)
	os.WriteFile(kj, b, 0o644)

	spy := &sttSpy{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stt" {
			spy.handler(v7Text)(w, r)
			return
		}
		e.srv.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	old := baseURLs["xai"]
	baseURLs["xai"] = srv.URL
	t.Cleanup(func() { baseURLs["xai"] = old })

	if _, se, code := e.story(t, "run"); code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	want := []string{"Anja Rossi", "ARGO", "Swerpify"}
	if len(spy.terms) != 1 || !reflect.DeepEqual(spy.terms[0], want) {
		t.Errorf("keyterm=%v, attesi %v", spy.terms, want)
	}
}

// controllo.json dopo story cut: la voce già controllata porta il confronto.
func TestC0121CutControlloConfrontoVoci(t *testing.T) {
	cutNeedFF(t)
	e := newV7Env(t, v8VoceBoard(false, `{"tecnico":true,"voce":true}`))
	e.sttMode = "altro"
	if _, se, code := e.story(t, "run"); code != 0 {
		t.Fatalf("run code=%d\n%s", code, se)
	}
	stt := e.stt
	os.Remove(filepath.Join(e.dir, "spot.controllo.json")) // deve riscriverlo il cut
	v8Cut(t, e)
	if e.stt != stt || e.tts != 1 {
		t.Errorf("il cut ha rifatto richieste: stt %d->%d tts=%d", stt, e.stt, e.tts)
	}
	read := func() map[string]any {
		b, err := os.ReadFile(filepath.Join(e.dir, "spot.controllo.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	voices, _ := read()["voices"].([]any)
	if len(voices) != 1 {
		t.Fatalf("voices: %v", voices)
	}
	cmp, _ := voices[0].(map[string]any)["comparison"].(map[string]any)
	if cmp == nil {
		t.Fatalf("manca comparison nella riga voce dopo cut: %v", voices[0])
	}
	if r, _ := cmp["ratio"].(float64); r >= 1 {
		t.Errorf("ratio=%v", cmp["ratio"])
	}
	if w, _ := cmp["words"].(float64); w == 0 {
		t.Errorf("words=%v", cmp["words"])
	}
	n := 0
	for _, f := range []string{"different", "missing", "extra"} {
		l, _ := cmp[f].([]any)
		n += len(l)
	}
	if _, ok := cmp["matched"]; !ok || n == 0 {
		t.Errorf("matched/different assenti: %v", cmp)
	}

	// stato 0.12.0: voice_check senza comparison accettato, cut riesce, riga senza confronto
	sp := filepath.Join(e.dir, "spot.stato.json")
	var st map[string]any
	sb, _ := os.ReadFile(sp)
	json.Unmarshal(sb, &st)
	vc := st["scenes"].(map[string]any)["s1"].(map[string]any)["voice_check"].(map[string]any)
	delete(vc, "comparison")
	sb, _ = json.Marshal(st)
	os.WriteFile(sp, sb, 0o600)
	os.Remove(filepath.Join(e.dir, "spot.controllo.json"))
	v8Cut(t, e)
	voices, _ = read()["voices"].([]any)
	if len(voices) != 1 {
		t.Fatalf("voices dopo stato 0.12.0: %v", voices)
	}
	if _, has := voices[0].(map[string]any)["comparison"]; has {
		t.Errorf("comparison inventata da uno stato senza: %v", voices[0])
	}
	if e.stt != stt {
		t.Errorf("stt rilanciata: %d", e.stt)
	}
}

// giv voice xai: costo calcolato dai caratteri, marcato; campi esistenti invariati.
func TestC0121VoiceXAICostoCalcolato(t *testing.T) {
	wav := fakeWAV(1)
	_, dir := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"audio": base64.StdEncoding.EncodeToString(wav), "content_type": "audio/wav", "duration": 1.0})
	})
	out, se, code := voiceRun(t, "--out", dir, "--name", "p", "Ciao")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	c, ok := m["cost_usage_usd"].(float64)
	if !ok || c < 5.9e-5 || c > 6.1e-5 {
		t.Errorf("cost_usage_usd=%v", m["cost_usage_usd"])
	}
	if s, _ := m["cost_usage_source"].(string); !strings.HasPrefix(s, "calcolato da giv:") {
		t.Errorf("cost_usage_source=%q", m["cost_usage_source"])
	}
	if e, _ := m["cost_est_usd"].(float64); e < 5.9e-5 || e > 6.1e-5 || m["command"] != "voice" || m["provider"] != "xai" {
		t.Errorf("campi esistenti cambiati: %v", m)
	}
}
