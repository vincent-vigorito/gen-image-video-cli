package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// CLI-928 (O-T9): help <-> README per comando e storyboard d'esempio della
// documentazione, senza chiamate vere.

// comandi con il loro help (argomenti per ottenerlo) e il testo con cui compaiono nel README
var docCommands = []struct {
	name    string
	help    []string
	more    [][]string // altri help del comando (sottocomandi) da unire
	noFlags bool       // il comando può non avere flag
}{
	{"image", []string{"image", "--help"}, nil, false},
	{"video", []string{"video", "--help"}, nil, false},
	{"presets", []string{"presets", "--help"}, [][]string{{"presets", "show", "--help"}}, false},
	{"job fetch", []string{"job", "fetch", "--help"}, nil, false},
	{"story check", []string{"story", "check", "--help"}, nil, true},
	{"story run", []string{"story", "run", "--help"}, nil, false},
	{"story cut", []string{"story", "cut", "--help"}, nil, true},
	{"extend", []string{"extend", "--help"}, nil, false},
	{"edit", []string{"edit", "--help"}, nil, false},
	{"upscale", []string{"upscale", "--help"}, nil, false},
	{"avatar", []string{"avatar", "--help"}, nil, false},
}

// flag elencati da un help: "-nome" (pacchetto flag) o "--nome" (testo di giv story)
var helpFlagRe = regexp.MustCompile(`(?m)(?:^\s+|[\s\[|(])--?([a-z][a-z0-9-]*)`)

func helpFlags(t *testing.T, args []string) map[string]bool {
	t.Helper()
	so, se, code, _ := runGiv(t, nil, args...)
	if code != 0 {
		t.Fatalf("giv %v: exit %d %s", args, code, se)
	}
	text := so + se
	out := map[string]bool{}
	if strings.HasPrefix(strings.TrimSpace(text), "Usage of") {
		// pacchetto flag: solo le righe di definizione "  -nome tipo"
		for _, m := range regexp.MustCompile(`(?m)^\s{2}-([a-z][a-z0-9-]*)`).FindAllStringSubmatch(text, -1) {
			out[m[1]] = true
		}
	} else {
		for _, m := range regexp.MustCompile(`--([a-z][a-z0-9-]*)`).FindAllStringSubmatch(text, -1) {
			out[m[1]] = true
		}
	}
	delete(out, "help")
	return out
}

// 1) ogni flag dell'help di ogni comando è nel README (e nella skill), e ogni flag
// scritto nel README sulle righe di un comando è nell'help di quel comando.
func TestHelpPerComandoCoincideConReadme(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	flagRe := regexp.MustCompile("--([a-z][a-z0-9-]*)")
	for _, c := range docCommands {
		h := helpFlags(t, c.help)
		for _, m := range c.more {
			for f := range helpFlags(t, m) {
				h[f] = true
			}
		}
		if len(h) == 0 && !c.noFlags {
			t.Errorf("giv %s: help senza flag", c.name)
		}
		// righe del README che mostrano il comando: "giv <comando> ..." (qualsiasi riga)
		var onLines = map[string]bool{}
		lines := strings.Split(string(readme), "\n")
		seen := false
		for _, l := range lines {
			// righe d'uso: iniziano con il comando (anche dentro un elenco o fra apici)
			tl := strings.TrimLeft(strings.TrimSpace(l), "-*` ")
			if tl == "giv "+c.name || strings.HasPrefix(tl, "giv "+c.name+" ") {
				seen = true
				for _, m := range flagRe.FindAllStringSubmatch(l, -1) {
					onLines[m[1]] = true
				}
			}
		}
		if !seen {
			t.Errorf("giv %s: nessun esempio nel README", c.name)
		}
		var missing []string
		for f := range h {
			if !strings.Contains(string(readme), "--"+f) {
				missing = append(missing, "--"+f)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("giv %s: flag nell'help ma non nel README: %v", c.name, missing)
		}
		delete(onLines, "help")
		var extra []string
		for f := range onLines {
			if !h[f] {
				extra = append(extra, "--"+f)
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			t.Errorf("giv %s: flag negli esempi del README ma non nell'help: %v", c.name, extra)
		}
	}
}

// 2) gli storyboard d'esempio (blocchi json del README e della skill con "scene",
// e examples/*/storyboard.json) passano giv story check, con catalogo finto e nessun avvio.
func TestStoryboardDocumentazionePassaCheck(t *testing.T) {
	jsonBlock := regexp.MustCompile("(?s)```json\n(.*?)```")
	type board struct{ name, body, root string }
	var boards []board
	for _, f := range []string{"../../README.md", "../../skills/gen-image-video/SKILL.md"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, m := range jsonBlock.FindAllStringSubmatch(string(b), -1) {
			if strings.Contains(m[1], `"scene"`) {
				boards = append(boards, board{filepath.Base(f) + "#" + string(rune('0'+i)), m[1], "../../examples/spot"})
			}
		}
	}
	files, _ := filepath.Glob("../../examples/*/storyboard.json")
	for _, f := range files {
		b, _ := os.ReadFile(f)
		boards = append(boards, board{f, string(b), filepath.Dir(f)})
	}
	if len(boards) < 2 {
		t.Fatalf("storyboard d'esempio trovati: %d", len(boards))
	}
	for _, b := range boards {
		dir := t.TempDir()
		copyTree(t, b.root, dir) // i file segnaposto accanto allo storyboard
		p := filepath.Join(dir, "sb.json")
		if err := os.WriteFile(p, []byte(b.body), 0o644); err != nil {
			t.Fatal(err)
		}
		n := capsServer(t)
		out, se, code, starts := storyRun(t, p, n)
		if code != 0 || starts != 0 {
			t.Errorf("%s: story check code=%d avvii=%d\n%s\n%s", b.name, code, starts, se, out)
		}
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	filepath.Walk(from, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Base(p) == "storyboard.json" {
			return nil
		}
		rel, _ := filepath.Rel(from, p)
		b, _ := os.ReadFile(p)
		os.MkdirAll(filepath.Dir(filepath.Join(to, rel)), 0o755)
		os.WriteFile(filepath.Join(to, rel), b, 0o644)
		return nil
	})
}

// 3) versione: 0.9.1 fino al cambio, 0.11.0 sulla punta finale.
func TestVersioneRilascio(t *testing.T) {
	so, _, code, _ := runGiv(t, nil, "version")
	want := os.Getenv("GIV_ATTESA_VERSIONE")
	if want == "" {
		want = "0.11.0"
	}
	if code != 0 || strings.TrimSpace(so) != "giv "+want {
		t.Fatalf("code=%d out=%q, atteso giv %s", code, so, want)
	}
}
