package presets

import (
	"strings"
	"testing"
)

func TestTuttiPresetPerOgniFamiglia(t *testing.T) {
	lib, err := load(builtinJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	models := map[string]string{
		"kling": "kwaivgi/kling-v3.0-pro", "veo": "google/veo-3.1", "seedance": "bytedance/seedance-1-pro",
		"grok": "grok-imagine-video", "omni": "openai/omni-video", "wan": "alibaba/wan-2.5",
	}
	if len(lib.List()) < 15 {
		t.Fatalf("libreria troppo piccola: %d", len(lib.List()))
	}
	for _, p := range lib.List() {
		for fam, model := range models {
			if got := Family(model); got != fam {
				t.Fatalf("Family(%s)=%q, atteso %q", model, got, fam)
			}
			r, err := lib.Expand(Request{Prompt: "a product", Presets: []string{p.ID}, Model: model})
			if err != nil {
				t.Errorf("%s/%s: %v", p.ID, fam, err)
				continue
			}
			if !strings.HasPrefix(r.Prompt, "a product") || len(r.Prompt) <= len("a product") || len(r.Presets) != 1 {
				t.Errorf("%s/%s: prompt finale %q", p.ID, fam, r.Prompt)
			}
		}
	}
}

func TestRifiutiExpand(t *testing.T) {
	lib, err := load(builtinJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, ids := range map[string][]string{
		"inesistente":   {"nope"},
		"incompatibili": {"dolly-in", "dolly-out"},
		"due camera":    {"orbit", "pan"},
	} {
		if _, err := lib.Expand(Request{Prompt: "x", Presets: ids}); err == nil {
			t.Errorf("%s: atteso errore", name)
		}
	}
}
