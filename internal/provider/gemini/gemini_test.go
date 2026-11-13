package gemini

import (
	"encoding/json"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name    string
		methods []string
		want    string
	}{
		{"models/veo-3.1-generate-preview", nil, "video"},
		{"models/gemini-3.1-flash-image", nil, "image"},
		{"models/x", []string{"predictLongRunning"}, "video"},
		{"models/x", []string{"predict"}, "image"},
		{"models/gemini-pro", []string{"generateContent"}, "other"},
	}
	for _, c := range cases {
		if got := classify(c.name, c.methods); got != c.want {
			t.Errorf("classify(%q,%v)=%q want %q", c.name, c.methods, got, c.want)
		}
	}
}

func TestVideoURIs(t *testing.T) {
	raw := json.RawMessage(`{"generateVideoResponse":{"generatedSamples":[{"video":{"uri":"u1"}}],"generatedVideos":[{"video":{"uri":"u2"}}]}}`)
	got, err := videoURIs(raw)
	if err != nil || len(got) != 2 || got[0] != "u1" || got[1] != "u2" {
		t.Fatalf("got %v err %v", got, err)
	}
	if _, err := videoURIs(json.RawMessage(`{"generateVideoResponse":{}}`)); err == nil {
		t.Fatal("atteso errore senza video")
	}
}
