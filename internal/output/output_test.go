package output

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gen-image-video-cli/internal/provider"
)

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

// CLI-881 R1: file o symlink già presente sul nome: mai seguito né sovrascritto.
func TestSaveAllNonSovrascriveNeSegueLink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "bersaglio")
	os.WriteFile(target, []byte("ORIGINALE"), 0o644)
	now := time.Now()
	for d := -2; d <= 3; d++ { // copre il secondo del timestamp anche al cambio
		os.Symlink(target, filepath.Join(dir, "x-"+now.Add(time.Duration(d)*time.Second).Format("20060102-150405")+".png"))
	}
	files, err := SaveAll(dir, "x", []provider.Media{{Mime: "image/png", Data: []byte("NUOVO")}})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "ORIGINALE" {
		t.Errorf("bersaglio modificato: %q", b)
	}
	if !strings.Contains(files[0].Path, "-2.png") {
		t.Errorf("atteso nome con -2: %s", files[0].Path)
	}
	if b, _ := os.ReadFile(files[0].Path); string(b) != "NUOVO" {
		t.Errorf("contenuto %q", b)
	}
}
