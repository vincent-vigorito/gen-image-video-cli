package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// CLI-1015 (giv 0.13.0): integrazione in story run, contro un OpenRouter
// finto (parEnv, story_parallel_test.go).

func listFiles(dir string) []string {
	var files []string
	filepath.Walk(dir, func(p string, i os.FileInfo, err error) error {
		if err == nil && !i.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, stamp.ReplaceAllString(rel, "-T"))
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// storyboard 0.12.x senza novità: richieste e file uguali con e senza
// --parallel 1; nessun file nuovo (bozze, approvazioni, giudizi).
func TestIntegrazioneStoryboard012xInvariato(t *testing.T) {
	e := newParEnv(t, sb("", ps("s1", ""), ps("s2", "")), false)
	out, se, code := e.run(t)
	if code != 0 || e.posts != 2 || out.Started != 2 {
		t.Fatalf("code=%d posts=%d\n%s", code, e.posts, se)
	}
	for _, f := range listFiles(e.dir) {
		if strings.Contains(f, "bozza") || strings.Contains(f, "giudizi") {
			t.Errorf("file nuovo in uno storyboard senza novità: %s", f)
		}
	}
	// seconda esecuzione: niente da fare, nessuna richiesta in più
	if _, se, code := e.run(t); code != 0 || e.posts != 2 {
		t.Fatalf("seconda: code=%d posts=%d\n%s", code, e.posts, se)
	}
}

// --keyframes con --bozza, e --bozza senza default.bozza: codice 2 senza richieste.
func TestIntegrazioneOpzioniRifiutate(t *testing.T) {
	board := sb("", ps("s1", ""))
	for name, args := range map[string][]string{
		"keyframes e bozza":        {"--keyframes", "--bozza"},
		"bozza senza profilo":      {"--bozza"},
		"bozza senza profilo (p2)": {"--bozza", "--parallel", "2"},
	} {
		e := newParEnv(t, board, false)
		_, se, code := e.run(t, args...)
		if code != 2 || e.reqs != 0 {
			t.Errorf("%s: code=%d richieste=%d\n%s", name, code, e.reqs, se)
		}
	}
}
