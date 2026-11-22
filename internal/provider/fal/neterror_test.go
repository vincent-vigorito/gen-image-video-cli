package fal

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout https://x.example/f?sig=SEGRETO" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

// CLI-922: netError non lascia passare indirizzi firmati e conserva la catena.
func TestNetError(t *testing.T) {
	wrap := func(e error) error { return &url.Error{Op: "Get", URL: "https://x.example/f?sig=SEGRETO", Err: e} }

	got := netError(wrap(errors.New(`parse "https://h/\x7f?sig=SEGRETO": invalid`)))
	if !strings.HasPrefix(got.Error(), "risposta di rete non valida") || strings.Contains(got.Error(), "SEGRETO") {
		t.Errorf("risposta malformata: %q", got)
	}
	got = netError(wrap(timeoutErr{}))
	if got.Error() != "tempo scaduto" {
		t.Errorf("timeout: %q", got)
	}
	var te timeoutErr
	if !errors.As(got, &te) {
		t.Errorf("errors.As perso")
	}
	for name, e := range map[string]error{"EOF": io.EOF, "annullato": context.Canceled, "scaduto": context.DeadlineExceeded} {
		if g := netError(wrap(e)); !errors.Is(g, e) || strings.Contains(g.Error(), "risposta di rete non valida") {
			t.Errorf("%s: %q", name, g)
		}
	}
	plain := errors.New("altro")
	if netError(plain) != plain {
		t.Errorf("errore non di rete cambiato")
	}
}
