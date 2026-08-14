package httpx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

type Error struct {
	Prefix string // nome provider
	Status int
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Prefix, e.Status, e.Msg)
}

// Retry esegue fn ritentando fino a 2 volte (backoff 2s, 4s) sui soli 5xx.
func Retry(ctx context.Context, fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		var he *Error
		if err == nil || attempt >= 2 || !errors.As(err, &he) || he.Status < 500 {
			return err
		}
		wait := time.Duration(2*(attempt+1)) * time.Second
		fmt.Fprintf(os.Stderr, "avviso: %v — ritento tra %s\n", err, wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
