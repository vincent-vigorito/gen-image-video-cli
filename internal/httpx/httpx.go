package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

type Error struct {
	Prefix     string // nome provider
	Status     int
	Msg        string
	RetryAfter time.Duration // header Retry-After della risposta (0 = assente)
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Prefix, e.Status, e.Msg)
}

// RetryFunc esegue fn con una politica di ripetizione (Retry, RetryPoll, NoRetry).
type RetryFunc func(ctx context.Context, fn func() error) error

// attesa massima fra due tentativi, anche con un Retry-After più lungo
const maxWait = 2 * time.Minute

// Retry esegue fn ritentando fino a 2 volte (backoff 2s, 4s) sui soli 5xx; con
// un contesto di WithoutRetry una volta sola.
func Retry(ctx context.Context, fn func() error) error {
	if ctx.Value(noRetryKey{}) != nil {
		return fn()
	}
	return retry(ctx, fn, 2, func(err error) bool { return status(err) >= 500 })
}

type noRetryKey struct{}

// WithoutRetry: le richieste fatte con il contesto restituito non si ripetono
// sui 5xx (Retry diventa NoRetry). Per le generazioni a pagamento in cui un
// secondo invio potrebbe far pagare due volte (fotogrammi di giv story run).
func WithoutRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, noRetryKey{}, true)
}

// RetryPoll è per le GET di attesa dei job video: ritenta fino a 5 volte (backoff
// 2s…32s o Retry-After) su 5xx, 429 ed errori di rete, così un disturbo passeggero
// non fa perdere un job già avviato.
func RetryPoll(ctx context.Context, fn func() error) error {
	return retry(ctx, fn, 5, func(err error) bool {
		s := status(err)
		return s >= 500 || s == http.StatusTooManyRequests || (s == 0 && IsNetwork(err))
	})
}

// NoRetry esegue fn una volta sola: per le richieste che avviano un job a pagamento.
func NoRetry(_ context.Context, fn func() error) error { return fn() }

func retry(ctx context.Context, fn func() error, maxRetries int, retriable func(error) bool) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || attempt >= maxRetries || ctx.Err() != nil || !retriable(err) {
			return err
		}
		wait := time.Duration(2<<attempt) * time.Second
		var he *Error
		if errors.As(err, &he) && he.RetryAfter > wait {
			wait = he.RetryAfter
		}
		wait = min(wait, maxWait)
		fmt.Fprintf(os.Stderr, "avviso: %v — ritento tra %s\n", err, wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// status è lo status HTTP dell'errore, 0 se non viene da una risposta HTTP.
func status(err error) int {
	var he *Error
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// IsNetwork dice se err è un errore di rete o un timeout (nessuna risposta HTTP).
func IsNetwork(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF)
}

// ParseRetryAfter legge l'header Retry-After (secondi o data HTTP); 0 se assente o non valido.
func ParseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if s, err := strconv.Atoi(h); err == nil {
		if s <= 0 {
			return 0
		}
		return time.Duration(min(s, 3600)) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// JobError è un errore arrivato dopo l'avvio di un job video: porta l'id del job,
// che può essere già addebitato e va ritrovato presso il provider.
type JobError struct {
	ID  string
	Err error
}

func (e *JobError) Error() string { return fmt.Sprintf("%v (job %s)", e.Err, e.ID) }
func (e *JobError) Unwrap() error { return e.Err }

// Job aggiunge l'id del job all'errore; nil resta nil.
func Job(id string, err error) error {
	if err == nil {
		return nil
	}
	return &JobError{ID: id, Err: err}
}

// StartError: la richiesta che avvia un job video non ha avuto una risposta che
// dica se il job è partito (5xx, timeout, rete, risposta senza id). Non si ripete:
// un secondo invio potrebbe far pagare il video due volte.
type StartError struct {
	Err error
}

func (e *StartError) Error() string {
	return fmt.Sprintf("%v — il job potrebbe essere partito lo stesso: richiesta non ripetuta", e.Err)
}
func (e *StartError) Unwrap() error { return e.Err }

// Start classifica l'errore della richiesta che avvia un job: un 4xx o una
// connessione mai aperta dicono che il job non è partito e restano come sono,
// ogni altro errore diventa StartError.
func Start(err error) error {
	if err == nil {
		return nil
	}
	if s := status(err); s >= 400 && s < 500 {
		return err
	}
	// connessione mai aperta (DNS, connessione rifiutata): la richiesta non è partita
	var oe *net.OpError
	if errors.As(err, &oe) && (oe.Op == "dial" || oe.Op == "proxyconnect") {
		return err
	}
	return &StartError{Err: err}
}
