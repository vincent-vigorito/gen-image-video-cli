package main

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gen-image-video-cli/internal/montage"
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
	// la versione corrente e' verificata da TestV9ChangelogEVersione
}

// lo storyboard di examples/spot passa story check (catalogo finto, nessun avvio).
func TestR8EsempioSpotCheck(t *testing.T) {
	n := capsServer(t)
	out, se, code, starts := storyRun(t, "../../examples/spot/storyboard.json", n)
	if code != 0 || starts != 0 {
		t.Fatalf("code=%d avvii=%d\n%s\n%s", code, starts, se, out)
	}
}

// cartello 9:16 con testo e sottotesto: due righe impilate, senza sovrapposizioni,
// dentro i margini; negli altri formati e con un solo testo resta centro+basso.
func TestR8CartelloVerticaleTestoESottotesto(t *testing.T) {
	m := montage.SafeMargins(720, 1280)
	c := montage.EndCard{Duration: 3, Text: "Swerpify", Subtext: "il gestionale che tiene tutto insieme"}
	ts := montage.EndCardTexts(c, 4, 1280, m)
	if len(ts) != 2 {
		t.Fatalf("9:16: %d testi, attesi 2", len(ts))
	}
	text, sub := ts[0], ts[1]
	if text.Pos != montage.Bottom || sub.Pos != montage.Top {
		t.Errorf("posizioni: %+v %+v", text, sub)
	}
	line := 1280 - text.MarginV // bordo basso del testo
	if line != 675 {
		t.Errorf("line = %d, atteso 675", line)
	}
	if sub.MarginV <= line {
		t.Errorf("sottotesto (MarginV %d) non sotto la riga del testo (%d)", sub.MarginV, line)
	}
	_, bot, _, _ := m.Pixels(720, 1280)
	if subBottom := sub.MarginV + sub.Style.Size; subBottom > 1280-bot {
		t.Errorf("sottotesto oltre il margine basso: %d > %d", subBottom, 1280-bot)
	}
	// logo sopra il testo
	c.Logo = &montage.Logo{Path: "/x/l.png", SrcW: 100, SrcH: 100, Width: 0.2, Opacity: 1}
	if lb, err := c.LogoBox(720, 1280, m); err != nil {
		t.Errorf("LogoBox: %v", err)
	} else {
		if lb.Y+lb.H > line-text.Style.Size {
			t.Errorf("logo (bottom %d) sovrapposto al testo (top %d)", lb.Y+lb.H, line-text.Style.Size)
		}
	}
	// invariati: 16:9, solo testo, solo sottotesto
	m169 := montage.SafeMargins(1280, 720)
	for _, cc := range []montage.EndCard{c, {Duration: 3, Text: "solo"}, {Duration: 3, Subtext: "solo"}} {
		hh, mm := 720, m169
		if cc.Text == "solo" || cc.Subtext == "solo" {
			hh, mm = 1280, m
		}
		for _, x := range montage.EndCardTexts(cc, 0, hh, mm) {
			if x.MarginV != 0 {
				t.Errorf("MarginV %d atteso 0 (%+v)", x.MarginV, x)
			}
		}
	}
}
