package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// F2-T1: giv story check. OpenRouter finto (capsServer): solo il catalogo, ogni
// altra richiesta e' un avvio e fa fallire il test.

func storyCheckRun(t *testing.T, board string) (out, errOut string, code int, starts int32) {
	t.Helper()
	n := capsServer(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "ref"), 0o755); err != nil {
		t.Fatal(err)
	}
	var png1 bytes.Buffer
	if err := png.Encode(&png1, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ref", "x.png"), png1.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "sb.json")
	if err := os.WriteFile(p, []byte(board), 0o644); err != nil {
		t.Fatal(err)
	}
	return storyRun(t, p, n)
}

func storyRun(t *testing.T, path string, n *int32) (out, errOut string, code int, starts int32) {
	t.Helper()
	grab := func(f **os.File) func() string {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := readAllFile(r); done <- string(b) }()
		return func() string { w.Close(); *f = old; return <-done }
	}
	so, se := grab(&os.Stdout), grab(&os.Stderr)
	err := cmdStory([]string{"check", path})
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return out, errOut, code, atomic.LoadInt32(n)
}

func readAllFile(r *os.File) ([]byte, error) {
	var b []byte
	buf := make([]byte, 4096)
	for {
		k, err := r.Read(buf)
		b = append(b, buf[:k]...)
		if err != nil {
			return b, nil
		}
	}
}

func sb(tetto string, scene ...string) string {
	return fmt.Sprintf(`{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"}%s,"scene":[%s]}`,
		tetto, strings.Join(scene, ","))
}

func sc(id, extra string) string {
	return fmt.Sprintf(`{"id":"%s","tipo":"video","durata":5,"prompt":"un gatto"%s}`, id, extra)
}

func TestStoryCheckValido4Scene(t *testing.T) {
	board := sb("", sc("s1", ""), sc("s2", ""), sc("s3", `,"continua_da":"s1"`),
		sc("s4", `,"immagine":"ref/x.png"`))
	out, se, code, starts := storyCheckRun(t, board)
	if code != 0 || starts != 0 {
		t.Fatalf("code=%d starts=%d\n%s", code, starts, se)
	}
	var r struct {
		Scene []struct {
			CostEstUSD *float64 `json:"cost_est_usd"`
		} `json:"scenes"`
		Total float64 `json:"cost_total_usd"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || len(r.Scene) != 4 {
		t.Fatalf("uscita: %v\n%s", err, out)
	}
	sum := 0.0
	for _, s := range r.Scene {
		if s.CostEstUSD == nil {
			t.Fatalf("scena senza stima: %s", out)
		}
		sum += *s.CostEstUSD
	}
	if sum <= 0 || math.Abs(sum-r.Total) > 1e-6 {
		t.Errorf("totale %v != somma stime %v", r.Total, sum)
	}
}

func TestStoryCheckRifiuti(t *testing.T) {
	cases := []struct{ name, board, want string }{
		{"json malformato", `{"scene": [`, ""},
		{"campo sconosciuto", sb(`,"boh":1`, sc("s1", "")), "sconosciuto"},
		{"id doppio", sb("", sc("a", ""), sc("a", "")), ""},
		{"continua_da inesistente", sb("", sc("s1", `,"continua_da":"zz"`)), ""},
		{"continua_da successiva", sb("", sc("s1", `,"continua_da":"s2"`), sc("s2", "")), ""},
		{"file mancante", sb("", sc("s1", `,"immagine":"ref/manca.png"`)), ""},
		{"durata non ammessa", sb("", `{"id":"s1","tipo":"video","durata":1,"prompt":"x"}`), ""},
		{"tipo avatar", sb("", `{"id":"s1","tipo":"avatar","prompt":"x"}`), "avatar"},
		{"totale oltre tetto", sb(`,"tetto_costo_usd":0.1`, sc("s1", ""), sc("s2", "")), "tetto"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, se, code, starts := storyCheckRun(t, c.board)
			if code != 2 || starts != 0 || out != "" {
				t.Fatalf("code=%d starts=%d out=%q\n%s", code, starts, out, se)
			}
			if c.want != "" && !strings.Contains(se, c.want) {
				t.Errorf("manca %q in: %s", c.want, se)
			}
		})
	}
}

func TestStoryCheckNonStimabile(t *testing.T) {
	// seedance: listino a token, nessuna stima
	scena := func(extra string) string {
		return `{"id":"s1","tipo":"video","durata":5,"prompt":"x","modello":"bytedance/seedance-2.0"` + extra + `}`
	}
	t.Run("dichiarato", func(t *testing.T) {
		out, se, code, _ := storyCheckRun(t, sb(`,"tetto_costo_usd":5`, scena(`,"costo_dichiarato_usd":0.7`)))
		if code != 0 {
			t.Fatalf("code=%d %s", code, se)
		}
		var r struct {
			Declared float64  `json:"cost_declared_usd"`
			Total    float64  `json:"cost_total_usd"`
			NotEst   []string `json:"not_estimated"`
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(err)
		}
		if r.Declared != 0.7 || r.Total != 0.7 || len(r.NotEst) != 0 {
			t.Errorf("%+v", r)
		}
	})
	t.Run("senza dichiarato con tetto", func(t *testing.T) {
		out, se, code, starts := storyCheckRun(t, sb(`,"tetto_costo_usd":5`, scena("")))
		if code != 2 || starts != 0 || out != "" || !strings.Contains(se, "s1") {
			t.Fatalf("code=%d out=%q\n%s", code, out, se)
		}
	})
	t.Run("senza tetto: elencata a parte", func(t *testing.T) {
		out, _, code, _ := storyCheckRun(t, sb("", scena("")))
		if code != 0 || !strings.Contains(out, `"not_estimated"`) {
			t.Fatalf("code=%d\n%s", code, out)
		}
	})
}

func TestStoryCheckNegativeIgnorato(t *testing.T) {
	// wan-3.0 non ha negative_prompt fra i parametri
	neg := `,"negative":"sfocato"`
	out, se, code, _ := storyCheckRun(t, sb("", sc("s1", neg)))
	if code != 0 || !strings.Contains(out+se, "negative") {
		t.Fatalf("senza strict: atteso avviso, code=%d\n%s\n%s", code, out, se)
	}
	strict := strings.Replace(sb("", sc("s1", neg)), `{"default"`, `{"strict":true,"default"`, 1)
	out, se, code, starts := storyCheckRun(t, strict)
	if code != 2 || starts != 0 || out != "" {
		t.Fatalf("strict: atteso rifiuto, code=%d\n%s", code, se)
	}
}

func TestStoryCheckModelloAssenteDalCatalogo(t *testing.T) {
	board := strings.Replace(sb("", sc("s1", "")), "alibaba/wan-3.0", "kwaivgi/kling-v9-sparito", 1)
	out, se, code, starts := storyCheckRun(t, board)
	if code != 2 || starts != 0 || out != "" || !strings.Contains(se, "catalogo") {
		t.Fatalf("code=%d starts=%d\n%s", code, starts, se)
	}
}
