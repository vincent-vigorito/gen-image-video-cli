package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"gen-image-video-cli/internal/httpx"
)

func TestExitCode(t *testing.T) {
	he := func(s int) error { return &httpx.Error{Prefix: "openrouter", Status: s} }
	cases := []struct {
		name string
		err  error
		code int
		hint string // sottostringa attesa
	}{
		{"uso", usageError("x"), exitUsage, ""},
		{"401", he(401), exitAuth, "chiave"},
		{"402", he(402), exitAuth, "credito"},
		{"429", he(429), exitRate, ""},
		{"404", he(404), exitNotFound, "giv models"},
		{"500", he(500), exitAPI, ""},
		{"400", he(400), exitAPI, ""},
		{"job 404 non e' modello", httpx.Job("j1", he(404)), exitAPI, "j1"},
		{"job rete", httpx.Job("j2", errors.New("boom")), exitAPI, "j2"},
		{"avvio", &httpx.StartError{Err: he(502)}, exitAPI, "controlla"},
		{"rete", fmt.Errorf("x: %w", &net.OpError{Op: "read", Err: errors.New("t")}), exitAPI, ""},
		{"generico", errors.New("x"), exitError, ""},
	}
	for _, c := range cases {
		code, hint := exitCode(c.err)
		if code != c.code || !strings.Contains(hint, c.hint) {
			t.Errorf("%s: code=%d hint=%q, atteso %d/%q", c.name, code, hint, c.code, c.hint)
		}
	}
}
