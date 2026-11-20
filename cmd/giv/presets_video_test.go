package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gen-image-video-cli/internal/presets"
	"gen-image-video-cli/internal/provider"
	"gen-image-video-cli/internal/provider/openaicompat"
)

// bodyOpenRouter invia una richiesta video a un server finto e ne ritorna il corpo.
func bodyOpenRouter(t *testing.T, model, prompt, negative string) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/videos" {
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &got)
		}
		http.Error(w, "stop", http.StatusBadRequest) // niente polling
	}))
	defer srv.Close()
	c := openaicompat.New("openrouter", srv.URL, "k", openaicompat.Options{Router: true})
	c.GenerateVideo(context.Background(), provider.VideoRequest{Model: model, Prompt: prompt, Negative: negative})
	if got == nil {
		t.Fatal("nessun corpo ricevuto")
	}
	return got
}

func TestVideoPresetInviaPromptEspanso(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	const model = "kwaivgi/kling-v3.0-pro"
	sp, sn, err := applyPresets([]string{"orbit"}, "", "a bottle", "", model, presets.Inputs{})
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := presets.Load()
	res, err := lib.Expand(presets.Request{Prompt: "a bottle", Presets: []string{"orbit"}, Model: model})
	if err != nil || res.Presets[0].Variant != "kling" || res.Presets[0].Phrase == "" {
		t.Fatalf("variante kling attesa: %+v %v", res, err)
	}
	if sp != "a bottle. "+res.Presets[0].Phrase && !strings.Contains(sp, res.Presets[0].Phrase) {
		t.Fatalf("prompt espanso=%q", sp)
	}
	if sn == "" {
		t.Fatal("negative del preset atteso su kling")
	}
	b := bodyOpenRouter(t, model, sp, sn)
	if b["prompt"] != sp || !strings.HasPrefix(sp, "a bottle") || sp == "a bottle" {
		t.Errorf("prompt inviato=%v", b["prompt"])
	}
	// il negative parte nel corpo solo con F1-T1 (rai-797); qui si prova che arriva al VideoRequest
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestVideoSenzaPresetCorpoIdentico(t *testing.T) {
	sp, sn, err := applyPresets(nil, "", "a bottle", "blurry", "kwaivgi/kling-v3.0-pro", presets.Inputs{FirstFrame: true, Duration: 5})
	if err != nil || sp != "a bottle" || sn != "blurry" {
		t.Fatalf("senza preset atteso invariato: %q %q %v", sp, sn, err)
	}
	// --tutela no senza preset: ancora invariato
	sp, sn, err = applyPresets(nil, "no", "a bottle", "", "x/y", presets.Inputs{FirstFrame: true})
	if err != nil || sp != "a bottle" || sn != "" {
		t.Fatalf("tutela no: %q %q %v", sp, sn, err)
	}
	b := bodyOpenRouter(t, "kwaivgi/kling-v3.0-pro", sp, sn)
	if b["prompt"] != "a bottle" {
		t.Errorf("prompt=%v", b["prompt"])
	}
	if _, ok := b["provider"]; ok {
		t.Errorf("campi inattesi senza negative: %v", b)
	}
}

func TestVideoPresetRifiutiCodice2SenzaChiamate(t *testing.T) {
	for name, extra := range map[string][]string{
		"inesistente":   {"--preset", "nope"},
		"due camera":    {"--preset", "orbit", "--preset", "pan"},
		"incompatibili": {"--preset", "dolly-in", "--preset", "dolly-out"},
	} {
		args := append([]string{"video", "--provider", "openrouter", "--model", "kwaivgi/kling-v3.0-pro"}, extra...)
		args = append(args, "a bottle")
		_, se, code, hits := runGiv(t, nil, args...)
		if code != 2 || hits != 0 {
			t.Errorf("%s: code=%d hits=%d stderr=%s", name, code, hits, se)
		}
	}
}
