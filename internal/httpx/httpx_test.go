package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	if got := ParseRetryAfter("7"); got != 7*time.Second {
		t.Errorf("secondi: %v", got)
	}
	if got := ParseRetryAfter("99999"); got != time.Hour {
		t.Errorf("tetto: %v", got)
	}
	for _, in := range []string{"", "0", "-3", "boh"} {
		if got := ParseRetryAfter(in); got != 0 {
			t.Errorf("%q: %v", in, got)
		}
	}
}

func TestRetryNon5xxNonRitenta(t *testing.T) {
	n := 0
	err := Retry(context.Background(), func() error { n++; return &Error{Status: 429} })
	if n != 1 || err == nil {
		t.Errorf("Retry su 429: n=%d err=%v", n, err)
	}
	n = 0
	NoRetry(context.Background(), func() error { n++; return &Error{Status: 500} })
	if n != 1 {
		t.Errorf("NoRetry: n=%d", n)
	}
}

func TestRetryPollRitentaSu429(t *testing.T) {
	n := 0
	err := RetryPoll(context.Background(), func() error {
		n++
		if n == 1 {
			return &Error{Status: 429, RetryAfter: time.Second}
		}
		return nil
	})
	if err != nil || n != 2 {
		t.Errorf("n=%d err=%v", n, err)
	}
	// 4xx diverso da 429: nessuna ripetizione
	n = 0
	RetryPoll(context.Background(), func() error { n++; return &Error{Status: 404} })
	if n != 1 {
		t.Errorf("404 ritentato: n=%d", n)
	}
}

func TestStart(t *testing.T) {
	if Start(nil) != nil {
		t.Error("nil")
	}
	e4 := &Error{Status: 400}
	if got := Start(e4); got != error(e4) {
		t.Errorf("4xx deve restare com'è: %v", got)
	}
	var se *StartError
	if !errors.As(Start(&Error{Status: 502}), &se) {
		t.Error("5xx deve diventare StartError")
	}
	if !errors.As(Start(fmt.Errorf("x: %w", &net.OpError{Op: "read", Err: errors.New("timeout")})), &se) {
		t.Error("errore di rete in lettura deve diventare StartError")
	}
	dial := &net.OpError{Op: "dial", Err: errors.New("refused")}
	if errors.As(Start(dial), &se) {
		t.Error("dial: il job non è partito, non è StartError")
	}
}

func TestJob(t *testing.T) {
	if Job("x", nil) != nil {
		t.Error("nil")
	}
	var je *JobError
	err := Job("abc", &Error{Prefix: "p", Status: 500, Msg: "m"})
	if !errors.As(err, &je) || je.ID != "abc" {
		t.Errorf("%v", err)
	}
	var he *Error
	if !errors.As(err, &he) {
		t.Error("Unwrap")
	}
}
