// Package speech è la base della voce di giv (giv voice e, nello storyboard,
// la voce delle scene): richiesta e risultato comuni ai provider, controlli
// prima di spendere, prezzi e stima, dizionario delle pronunce, tempi per
// carattere e lettura del WAV. Nessuna chiamata di rete: i provider stanno in
// internal/provider e implementano Synthesizer.
package speech

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Provider della voce.
const (
	XAI    = "xai"
	Gemini = "gemini"
)

// Limiti di giv, controllati prima di ogni richiesta.
const (
	// MaxTextChars: caratteri per richiesta. xAI ne accetta 60.000; giv si
	// ferma prima (circa un quarto d'ora di parlato) perché la risposta, un
	// WAV in base64, deve stare entro MaxResponseBytes.
	MaxTextChars = 15000
	// MaxResponseBytes: risposta del provider (audio e tempi).
	MaxResponseBytes = 160 << 20
	// MaxTextFileBytes: file del testo (--text-file).
	MaxTextFileBytes = 256 << 10
	// MinSpeed e MaxSpeed: velocità ammesse (xAI).
	MinSpeed, MaxSpeed = 0.7, 1.5
	// SampleRate: frequenza del WAV chiesto al provider.
	SampleRate = 24000
	// MaxGeminiTextChars: caratteri detti (dopo le pronunce) per richiesta a
	// gemini, che dà al più MaxGeminiOutputTokens di audio (circa 11 minuti):
	// anche a un ritmo lento il testo deve starci, o la voce esce troncata.
	MaxGeminiTextChars = 6000
	// MaxGeminiInputTokens e MaxGeminiOutputTokens: token di gemini per
	// richiesta in ingresso e in uscita (limiti del modello): danno il
	// massimo fatturabile, su cui vale il tetto.
	MaxGeminiInputTokens  = 8192
	MaxGeminiOutputTokens = 16384
	// GeminiTokensPerSecond: token audio di gemini per secondo di parlato.
	GeminiTokensPerSecond = 25
	// MaxStyleChars: caratteri dello stile a parole.
	MaxStyleChars = 1000
	// MaxTimings: tempi per carattere letti da una risposta; oltre si scartano
	// tutti (i sottotitoli ripiegano sulla durata).
	MaxTimings = 4 * MaxTextChars
)

// Predefiniti della voce: xai eve in italiano, scelta dopo l'ascolto
// (decisione 21); per gemini, seconda via, Kore.
const (
	DefaultProvider    = XAI
	DefaultLanguage    = "it"
	DefaultXAIVoice    = "eve"
	DefaultGeminiModel = "gemini-3.8-flash-tts"
	DefaultGeminiVoice = "Kore"
)

// Request è una richiesta di voce, uguale per tutti i provider.
type Request struct {
	Provider string     // xai, gemini; "" = DefaultProvider
	Model    string     // modello (gemini); xai non ne ha
	Voice    string     // "" = la voce predefinita del provider
	Language string     // BCP-47 o auto; "" = DefaultLanguage
	Speed    float64    // 0 = velocità del provider (xai 1.0)
	Style    string     // stile a parole (gemini); xai lo ignora con un avviso
	Text     string     // testo da dire, come scritto (i sottotitoli usano questo)
	Dict     Dictionary // pronunce: parola scritta -> come dirla
}

// Result è la voce generata.
type Result struct {
	Audio     []byte     // WAV
	WAV       WAVInfo    // misure lette dal file
	DurationS float64    // durata misurata sul WAV
	Timings   []CharTime // tempi per carattere; nil = il provider non li ha dati
	Chars     int        // caratteri fatturati (xai: quelli del testo inviato)
	CostUSD   float64    // costo riportato dal provider; 0 = non riportato
	Usage     *TokenUsage
	// costo dai consumi (gemini: token riportati, altrimenti durata
	// misurata), accanto alla stima da listino: vedi UsageCost
	UsageUSD    float64
	UsageSource string
	Warnings    []string // risposta tollerata (tempi scartati, durate diverse…)
}

// TokenUsage sono i token riportati dal provider (gemini); nil nel Result =
// non riportati o incoerenti.
type TokenUsage struct {
	Input   int64 `json:"input"`
	Output  int64 `json:"output"`
	Thought int64 `json:"thought,omitempty"`
}

// Voice è una voce dell'elenco del provider.
type Voice struct {
	ID       string `json:"voice_id"`
	Name     string `json:"name,omitempty"`
	Language string `json:"language,omitempty"`
}

// Synthesizer genera una voce: una sola richiesta a pagamento, mai ripetuta.
// Gli errori sono classificati con httpx.Start: un 4xx o una connessione mai
// aperta dicono che la richiesta non è partita; ogni altro esito (3xx, 5xx,
// rete, risposta non valida) è un *httpx.StartError: la voce può essere
// stata addebitata e non si rilancia da sola.
type Synthesizer interface {
	Synthesize(ctx context.Context, req Request) (*Result, error)
}

// VoiceLister elenca le voci del provider (gratuito).
type VoiceLister interface {
	Voices(ctx context.Context) ([]Voice, error)
}

var (
	voiceRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	languageRe = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8}){0,3}$`)
	modelRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// ValidVoice dice se id può essere il nome di una voce (lettere, cifre, - e
// _, al massimo 64): i nomi dell'elenco del provider passano da qui.
func ValidVoice(id string) bool { return voiceRe.MatchString(id) }

// Spoken è il testo che va al provider: con gemini, che non ha un dizionario,
// le pronunce sostituite nel testo; con xai il testo com'è (il dizionario va
// a parte, nel campo replace).
func (r Request) Spoken() string {
	if r.Provider == Gemini {
		return r.Dict.Apply(r.Text)
	}
	return r.Text
}

// Check controlla una richiesta prima di stimarla e di spendere, e la
// restituisce coi predefiniti: provider, modello, voce, lingua, testo senza
// spazi in testa e in coda. Ogni errore è un uso sbagliato (in giv codice 2);
// gli avvisi dicono cosa il provider non userà.
func Check(req Request) (Request, []string, error) {
	var warns []string
	if req.Provider == "" {
		req.Provider = DefaultProvider
	}
	if req.Provider != XAI && req.Provider != Gemini {
		return req, nil, fmt.Errorf("provider della voce sconosciuto: %q (validi: xai, gemini)", req.Provider)
	}
	if !utf8.ValidString(req.Text) {
		return req, nil, errors.New("testo non valido: serve UTF-8")
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		return req, nil, errors.New("manca il testo da dire")
	}
	if n := utf8.RuneCountInString(req.Text); n > MaxTextChars {
		return req, nil, fmt.Errorf("testo di %d caratteri: al massimo %d per richiesta", n, MaxTextChars)
	}
	switch req.Provider {
	case XAI:
		if req.Model != "" {
			return req, nil, fmt.Errorf("%s: la voce non ha modelli da scegliere (modello %q)", req.Provider, req.Model)
		}
		if req.Voice == "" {
			req.Voice = DefaultXAIVoice
		}
	case Gemini:
		if req.Model == "" {
			req.Model = DefaultGeminiModel
		}
		if !modelRe.MatchString(req.Model) {
			return req, nil, fmt.Errorf("modello della voce non valido: %q", req.Model)
		}
		if req.Voice == "" {
			req.Voice = DefaultGeminiVoice
		}
	}
	if !voiceRe.MatchString(req.Voice) {
		return req, nil, fmt.Errorf("voce non valida: %q (lettere, cifre, - e _, al massimo 64)", req.Voice)
	}
	if req.Language == "" {
		req.Language = DefaultLanguage
	}
	if !strings.EqualFold(req.Language, "auto") && !languageRe.MatchString(req.Language) {
		return req, nil, fmt.Errorf("lingua non valida: %q (codice BCP-47 come it, en, pt-BR, oppure auto)", req.Language)
	}
	if req.Speed != 0 && (math.IsNaN(req.Speed) || math.IsInf(req.Speed, 0) || req.Speed < MinSpeed || req.Speed > MaxSpeed) {
		return req, nil, fmt.Errorf("velocità %g fuori dai limiti: da %.1f a %.1f", req.Speed, MinSpeed, MaxSpeed)
	}
	if !utf8.ValidString(req.Style) {
		return req, nil, errors.New("stile non valido: serve UTF-8")
	}
	req.Style = strings.TrimSpace(req.Style)
	if n := utf8.RuneCountInString(req.Style); n > MaxStyleChars {
		return req, nil, fmt.Errorf("stile di %d caratteri: al massimo %d", n, MaxStyleChars)
	}
	if err := req.Dict.Check(); err != nil {
		return req, nil, err
	}
	switch req.Provider {
	case XAI:
		if req.Style != "" {
			warns = append(warns, "xai non ha uno stile a parole: stile ignorato (usa la velocità e i tag nel testo, come [pause])")
		}
	case Gemini:
		if req.Speed != 0 {
			req.Speed = 0
			warns = append(warns, "gemini non ha una velocità numerica: velocità ignorata (indicala a parole nello stile)")
		}
		if n := utf8.RuneCountInString(req.Spoken()); n > MaxGeminiTextChars {
			return req, nil, fmt.Errorf("gemini: testo di %d caratteri (con le pronunce): al massimo %d per richiesta", n, MaxGeminiTextChars)
		}
	}
	return req, warns, nil
}
