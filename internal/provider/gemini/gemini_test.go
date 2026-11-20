package gemini

import "testing"

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
