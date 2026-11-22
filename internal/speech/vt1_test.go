package speech

import (
	"math"
	"testing"
	"time"
)

func TestV1CostoGeminiTokenDurataE2027(t *testing.T) {
	req, _, err := Check(Request{Provider: "gemini", Text: "Ciao"})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC)
	after := time.Date(2027, 1, 1, 1, 0, 0, 0, time.UTC)
	tok := &Result{Usage: &TokenUsage{Input: 1000000, Output: 1000000}}
	a, _, ok := UsageCost(req, tok, before)
	b, _, ok2 := UsageCost(req, tok, after)
	if !ok || !ok2 || a <= 0 || math.Abs(b/a-2) > 1e-9 {
		t.Fatalf("token: prima=%v dopo=%v", a, b)
	}
	dur := &Result{DurationS: 4}
	c, src, ok := UsageCost(req, dur, before)
	if !ok || c <= 0 || src == "" {
		t.Fatalf("durata: %v %q %v", c, src, ok)
	}
	if d, _, _ := UsageCost(req, dur, after); math.Abs(d/c-2) > 1e-9 {
		t.Errorf("durata dopo 2027: %v vs %v", d, c)
	}
	if _, _, ok := UsageCost(req, &Result{}, before); ok {
		t.Error("senza token né durata: atteso non calcolabile")
	}
}
