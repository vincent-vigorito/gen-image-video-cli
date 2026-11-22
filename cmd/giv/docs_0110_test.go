package main

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gen-image-video-cli/internal/story"
)

// R-T8 (CLI-953): documentazione e versione 0.11.0.

var docCommands0110 = [][]string{
	{"post", "trim"}, {"post", "speed"}, {"post", "freeze"}, {"post", "reframe"}, {"post", "overlay"}, {"post", "mix"},
	{"sheet"}, {"frame"}, {"gif"}, {"export"},
}

// ogni comando nuovo ha un esempio nel README e ogni flag del suo help è nel README.
func TestR8ComandiNuoviNelReadme(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	flagRe := regexp.MustCompile(`--([a-z][a-z0-9-]*)`)
	for _, c := range docCommands0110 {
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
		if len(seen) == 0 {
			t.Errorf("giv %s: help senza flag", name)
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("giv %s: flag nell'help ma non nel README: %v", name, missing)
		}
	}
}

// ogni campo JSON del montaggio (e dei suoi oggetti) e il campo fuoco della scena sono nel README.
func TestR8CampiStoryboardNelReadme(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	var walk func(rt reflect.Type, seen map[reflect.Type]bool)
	var tags []string
	walk = func(rt reflect.Type, seen map[reflect.Type]bool) {
		for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || seen[rt] {
			return
		}
		seen[rt] = true
		for i := 0; i < rt.NumField(); i++ {
			tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
			if tag != "" && tag != "-" {
				tags = append(tags, tag)
			}
			walk(rt.Field(i).Type, seen)
		}
	}
	walk(reflect.TypeOf(story.Montaggio{}), map[reflect.Type]bool{})
	if f, ok := reflect.TypeOf(story.Scene{}).FieldByName("Fuoco"); ok {
		tags = append(tags, strings.Split(f.Tag.Get("json"), ",")[0])
	} else {
		t.Error("story.Scene senza campo Fuoco")
	}
	for _, f := range tags {
		if !strings.Contains(readme, "`"+f+"`") && !strings.Contains(readme, `"`+f+`"`) {
			t.Errorf("campo dello storyboard non documentato nel README: %s", f)
		}
	}
}

func TestR8ChangelogEVersione(t *testing.T) {
	b, err := os.ReadFile("../../CHANGELOG.md")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^## \[0\.11\.0\]`).Match(b) {
		t.Error("CHANGELOG senza la voce 0.11.0")
	}
	so, _, code, _ := runGiv(t, nil, "--version")
	if code != 0 || strings.TrimSpace(so) != "giv 0.11.0" {
		t.Errorf("giv --version: code=%d out=%q", code, so)
	}
}

// lo storyboard di examples/spot passa story check (catalogo finto, nessun avvio).
func TestR8EsempioSpotCheck(t *testing.T) {
	n := capsServer(t)
	out, se, code, starts := storyRun(t, "../../examples/spot/storyboard.json", n)
	if code != 0 || starts != 0 {
		t.Fatalf("code=%d avvii=%d\n%s\n%s", code, starts, se, out)
	}
}
