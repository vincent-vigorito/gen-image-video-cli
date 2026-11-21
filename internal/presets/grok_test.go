package presets

import "testing"

// Ogni preset ha la variante grok e la usa, senza avviso di frase generica.
func TestVarianteGrokPerOgniPreset(t *testing.T) {
	lib, err := load(builtinJSON, nil)
	if err != nil {
		t.Fatal(err) // include la validazione del file
	}
	if n := len(lib.List()); n != 18 {
		t.Fatalf("attesi 18 preset, trovati %d", n)
	}
	for _, p := range lib.List() {
		if p.Variants["grok"] == "" {
			t.Errorf("%s: variante grok assente", p.ID)
		}
		for _, model := range []string{"grok-imagine-video-1.5", "x-ai/grok-imagine-video-1.5"} {
			r, err := lib.Expand(Request{Prompt: "a product", Presets: []string{p.ID}, Model: model, Strict: true})
			if err != nil {
				t.Errorf("%s/%s: %v", p.ID, model, err)
				continue
			}
			if len(r.Warnings) != 0 {
				t.Errorf("%s/%s: avvisi %v", p.ID, model, r.Warnings)
			}
			if len(r.Presets) != 1 || r.Presets[0].Variant != "grok" || r.Presets[0].Phrase != p.Variants["grok"] {
				t.Errorf("%s/%s: variante usata %+v", p.ID, model, r.Presets)
			}
		}
	}
}
