package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gen-image-video-cli/internal/story"
)

// V-T9 (CLI-980): documentazione e versione 0.12.0.

var docCommands0120 = [][]string{{"voice"}, {"kit", "check"}, {"qc"}}

// ogni comando nuovo ha un esempio nel README e ogni flag del suo help è nel README.
func TestV9ComandiNuoviNelReadme(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	flagRe := regexp.MustCompile(`--([a-z][a-z0-9-]*)`)
	for _, c := range docCommands0120 {
		name := strings.Join(c, " ")
		so, se, code, _ := runGiv(t, nil, append(append([]string{}, c...), "--help")...)
		if code != 0 {
			t.Errorf("giv %s --help: exit %d %s", name, code, se)
			continue
		}
		if !strings.Contains(readme, "giv "+name) {
			t.Errorf("giv %s: non è nel README", name)
		}
		var missing []string
		seen := map[string]bool{}
		for _, m := range flagRe.FindAllStringSubmatch(so+se, -1) {
			if m[1] != "help" && !seen[m[1]] && !strings.Contains(readme, "--"+m[1]) {
				missing = append(missing, "--"+m[1])
			}
			seen[m[1]] = true
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("giv %s: flag nell'help ma non nel README: %v", name, missing)
		}
		if strings.Contains(strings.ToLower(so+se), "provvisori") {
			t.Errorf("giv %s: l'help parla ancora di voce provvisoria (decisione 21)", name)
		}
	}
}

// i campi nuovi dello storyboard della 0.12.0 esistono e sono nel README.
func TestV9CampiStoryboardNelReadme(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	tags := map[string]bool{}
	var walk func(rt reflect.Type, seen map[reflect.Type]bool)
	walk = func(rt reflect.Type, seen map[reflect.Type]bool) {
		for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			if tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
				tags[tag] = true
			}
			walk(rt.Field(i).Type, seen)
		}
	}
	walk(reflect.TypeOf(story.Storyboard{}), map[reflect.Type]bool{})
	for _, f := range []string{"kit", "parlato", "controllo", "senza", "sottotitoli"} {
		if !tags[f] {
			t.Errorf("campo %q non accettato dallo storyboard (story.Storyboard)", f)
			continue
		}
		if !strings.Contains(readme, "`"+f+"`") && !strings.Contains(readme, `"`+f+`"`) {
			t.Errorf("campo dello storyboard non documentato nel README: %s", f)
		}
	}
}

func TestV9ChangelogEVersione(t *testing.T) {
	b, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^## \[0\.12\.0\]`).Match(b) {
		t.Error("CHANGELOG senza la voce 0.12.0")
	}
	// la versione corrente è provata in docs_0121_test.go
}

// examples/kit passa giv kit check.
func TestV9EsempioKitCheck(t *testing.T) {
	kit, err := filepath.Abs("../../examples/kit/kit.json")
	if err != nil {
		t.Fatal(err)
	}
	_, se, code, _ := runGiv(t, nil, "kit", "check", kit)
	if code != 0 {
		t.Fatalf("kit check examples/kit: exit %d %s", code, se)
	}
}

// ogni storyboard d'esempio passa story check (catalogo finto, nessun avvio).
func TestV9EsempiStoryboardCheck(t *testing.T) {
	files, _ := filepath.Glob("../../examples/*/storyboard*.json")
	if len(files) == 0 {
		t.Fatal("nessuno storyboard d'esempio")
	}
	n := capsServer(t)
	for _, f := range files {
		out, se, code, starts := storyRun(t, f, n)
		if code != 0 || starts != 0 {
			t.Errorf("%s: code=%d avvii=%d\n%s\n%s", f, code, starts, se, out)
		}
	}
}
