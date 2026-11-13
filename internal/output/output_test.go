package output

import "testing"

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Un Gatto  Rosso!": "un-gatto-rosso",
		"!!!":              "media",
		"":                 "media",
	}
	for in, want := range cases {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q)=%q want %q", in, got, want)
		}
	}
	if got := Slug("abcdefghij abcdefghij abcdefghij abcdefghij abcdefghij"); len(got) > 41 {
		t.Errorf("slug troppo lungo: %q", got)
	}
}
