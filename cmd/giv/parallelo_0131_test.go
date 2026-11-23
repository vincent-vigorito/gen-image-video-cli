package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Q-R (CLI-1030, giv 0.13.1): default.parallelo scritto a 0 è un errore di
// validazione; assente vale 1; 1 e 4 validi; 5 rifiutato.

func parBoard(par string) string {
	extra := ""
	if par != "" {
		extra = `,"parallelo":` + par
	}
	return fmt.Sprintf(`{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"%s},"scene":[%s]}`,
		extra, ps("s1", ""))
}

func parCheck(t *testing.T, e *parEnv) (string, int) {
	t.Helper()
	so, se := grabFile(&os.Stdout), grabFile(&os.Stderr)
	err := cmdStory([]string{"check", e.board})
	sout, serr := so(), se()
	code := 0
	if err != nil {
		code, _ = exitCode(err)
		serr += err.Error()
	}
	return sout + serr, code
}

func TestParalleloZeroEsplicitoRifiutato(t *testing.T) {
	e := newParEnv(t, parBoard("0"), false)
	out, code := parCheck(t, e)
	if code != 2 || !strings.Contains(out, "parallelo") || !strings.Contains(out, "da 1 a 4") {
		t.Errorf("story check: code=%d\n%s", code, out)
	}
	_, se, code := e.run(t)
	if code != 2 || !strings.Contains(se, "parallelo") || !strings.Contains(se, "da 1 a 4") {
		t.Errorf("story run: code=%d\n%s", code, se)
	}
	if e.reqs != 0 {
		t.Errorf("richieste ricevute: %d", e.reqs)
	}
	if _, err := os.Stat(e.statePath()); err == nil {
		t.Error("stato scritto")
	}
}

func TestParalleloAssenteUnoQuattroValidiCinqueNo(t *testing.T) {
	for _, c := range []struct {
		par  string
		code int
	}{{"", 0}, {"1", 0}, {"4", 0}, {"5", 2}} {
		e := newParEnv(t, parBoard(c.par), false)
		out, code := parCheck(t, e)
		if code != c.code {
			t.Errorf("parallelo %q: code=%d atteso %d\n%s", c.par, code, c.code, out)
		}
	}
	// 5 anche in run: codice 2, nessuna richiesta
	e := newParEnv(t, parBoard("5"), false)
	if _, _, code := e.run(t); code != 2 || e.reqs != 0 {
		t.Errorf("run parallelo 5: code=%d richieste=%d", code, e.reqs)
	}
}
