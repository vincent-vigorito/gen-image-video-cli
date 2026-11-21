package provider

import (
	"context"
	"errors"
	"time"
)

type ModelInfo struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"` // image | video | other
	DisplayName string         `json:"display_name,omitempty"`
	Pricing     map[string]any `json:"pricing,omitempty"` // raw del provider (per ora: openrouter)
}

type ImageRequest struct {
	Prompt string
	Model  string
	N      int
	Aspect string
	Seed   int      // 0 = non impostato
	Inputs []string // path di immagini di riferimento (editing/reference)
}

type VideoRequest struct {
	Prompt     string
	Model      string
	Aspect     string
	Resolution string
	Negative   string
	Image      string // frame iniziale opzionale
	Duration   int    // secondi, 0 = default del modello

	// campi opzionali: a zero la richiesta resta quella di prima
	LastFrame string         // ultimo fotogramma (path locale)
	Refs      []string       // immagini di riferimento (path locali)
	Audio     *bool          // audio nativo, nil = default del modello
	Seed      int            // 0 = non impostato
	Params    map[string]any // parametri passanti del modello
	Timeout   time.Duration  // tetto di attesa del job, 0 = 15 minuti
}

type Media struct {
	Mime string
	Data []byte
}

type Result struct {
	Media   []Media
	CostUSD float64 // 0 = costo non riportato dal provider
	JobID   string  // id del job asincrono; "" se il provider non ne ha
}

type Provider interface {
	Name() string
	Models(ctx context.Context) ([]ModelInfo, error)
	GenerateImage(ctx context.Context, req ImageRequest) (*Result, error)
	GenerateVideo(ctx context.Context, req VideoRequest) (*Result, error)
}

// Job è un job video già partito: quanto serve per riprenderne l'attesa e
// scaricarne il risultato senza un nuovo avvio.
type Job struct {
	ID         string
	PollingURL string // indirizzo di attesa (OpenRouter lo dà all'avvio, xai lo ricava dall'id)
}

// JobFetcher è un provider che sa recuperare un job video già partito.
type JobFetcher interface {
	// CheckJob rifiuta, senza richieste, un job che non può essere di questo
	// provider (indirizzo di attesa su un altro host, id vuoto, provider senza job).
	CheckJob(job Job) error
	// FetchVideo attende il job (fino a timeout, 0 = 15 minuti) e ne scarica il
	// risultato; ogni errore porta l'id del job.
	FetchVideo(ctx context.Context, job Job, timeout time.Duration) (*Result, error)
}

// ErrJobFailed: il provider dice che il job è finito male (stato terminale
// failed, cancelled, error, expired letto dall'attesa): non c'è più niente da
// scaricare. Gli altri errori (rete, download, risposta incompleta) lasciano il
// job recuperabile.
var ErrJobFailed = errors.New("job finito male sul provider")

// JobFailed segna err come stato terminale di fallimento del job (errors.Is con
// ErrJobFailed), lasciando il messaggio com'è.
func JobFailed(err error) error { return jobFailed{err} }

type jobFailed struct{ error }

func (e jobFailed) Unwrap() error      { return e.error }
func (jobFailed) Is(target error) bool { return target == ErrJobFailed }

// OnJobStart, se impostata, riceve ogni job video appena avviato, prima
// dell'attesa: il CLI la usa per salvarne lo stato (giv job fetch).
var OnJobStart func(prov string, job Job, req VideoRequest)
