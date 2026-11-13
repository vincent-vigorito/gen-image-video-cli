package openaicompat

import "testing"

func TestSizeForAspect(t *testing.T) {
	cases := map[string]string{
		"1:1": "1024x1024", "9:16": "1024x1536", "3:4": "1024x1536",
		"16:9": "1536x1024", "4:3": "1536x1024",
	}
	for in, want := range cases {
		if got := sizeForAspect(in); got != want {
			t.Errorf("sizeForAspect(%q)=%q want %q", in, got, want)
		}
	}
}
