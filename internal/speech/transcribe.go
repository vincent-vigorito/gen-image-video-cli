package speech

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Trascrizione di un file audio (giv qc --voice): richiesta, risultato,
// limiti e prezzi comuni ai provider. Una trascrizione è una richiesta a
// pagamento come la voce: stima e tetto prima, una sola richiesta, mai
// ripetuta.

// Limiti della trascrizione.
const (
	// MaxTranscribeBytes: file audio caricato (xAI accetta fino a 500 MB; le
	// voci fuori campo di giv sono di pochi MB).
	MaxTranscribeBytes = 100 << 20
	// MaxTranscriptBytes: risposta della trascrizione.
	MaxTranscriptBytes = 16 << 20
	// MaxWords: parole lette da una risposta; oltre si scartano tutte.
	MaxWords = MaxTimings
	// MaxKeyterms e MaxKeytermChars: termini da riconoscere (xAI: 100 da 50).
	MaxKeyterms     = 100
	MaxKeytermChars = 50
	// maxWordChars: caratteri di una parola trascritta.
	maxWordChars = 100
)

// AudioFormats sono i formati che giv carica per la trascrizione
// (estensione -> tipo MIME del caricamento).
var AudioFormats = map[string]string{
	"wav": "audio/wav", "mp3": "audio/mpeg", "ogg": "audio/ogg", "flac": "audio/flac",
	"aac": "audio/aac", "m4a": "audio/mp4", "mp4": "audio/mp4",
}

// TranscribeRequest è una richiesta di trascrizione.
type TranscribeRequest struct {
	Provider string   // xai; "" = DefaultProvider
	Audio    []byte   // il file audio, già letto da giv
	Format   string   // estensione in AudioFormats (wav, mp3…)
	Keyterms []string // termini da riconoscere (nomi propri): vedi Keyterms
}

// Word è una parola trascritta coi suoi tempi in secondi.
type Word struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Transcript è il risultato di una trascrizione.
type Transcript struct {
	Text      string  // testo trascritto, ripulito
	Language  string  // lingua rilevata dal provider ("" = non riportata)
	DurationS float64 // durata riportata dal provider; 0 = non riportata
	Words     []Word  // parole coi tempi; nil = non riportate o scartate
	// costo calcolato da giv sulla durata riportata (il provider non lo dà)
	UsageUSD    float64
	UsageSource string
	Warnings    []string
}

// Transcriber trascrive un file audio: una sola richiesta a pagamento, mai
// ripetuta. Errori come per Synthesizer (httpx.Start), con un'eccezione
// (decisione 17): un caricamento che non arriva fino in fondo è un errore
// ordinario, perché il provider non ha ricevuto il file.
type Transcriber interface {
	Transcribe(ctx context.Context, req TranscribeRequest) (*Transcript, error)
}

// CheckTranscribe controlla una richiesta di trascrizione prima della stima
// e della spesa e la restituisce coi predefiniti; gli avvisi dicono quali
// termini non vanno al provider.
func CheckTranscribe(req TranscribeRequest) (TranscribeRequest, []string, error) {
	if req.Provider == "" {
		req.Provider = DefaultProvider
	}
	switch req.Provider {
	case XAI:
	case Gemini:
		return req, nil, errors.New("trascrizione con gemini non disponibile in questa versione: usa xai")
	default:
		return req, nil, fmt.Errorf("provider della trascrizione sconosciuto: %q (validi: xai)", req.Provider)
	}
	req.Format = strings.ToLower(req.Format)
	if _, ok := AudioFormats[req.Format]; !ok {
		return req, nil, fmt.Errorf("formato audio non ammesso: %q (validi: %s)", req.Format, strings.Join(audioFormatNames(), ", "))
	}
	if len(req.Audio) == 0 {
		return req, nil, errors.New("file audio vuoto")
	}
	if len(req.Audio) > MaxTranscribeBytes {
		return req, nil, fmt.Errorf("file audio oltre %d MiB", MaxTranscribeBytes>>20)
	}
	var warns []string
	var terms []string
	seen := map[string]bool{}
	for _, t := range req.Keyterms {
		t = strings.TrimSpace(t)
		key := strings.ToLower(t)
		switch {
		case t == "" || seen[key]:
			continue
		case !utf8.ValidString(t) || strings.IndexFunc(t, unicode.IsControl) >= 0:
			return req, nil, fmt.Errorf("termine non valido: %q", short(t))
		case utf8.RuneCountInString(t) > MaxKeytermChars:
			warns = append(warns, fmt.Sprintf("termine %q oltre %d caratteri: non inviato", short(t), MaxKeytermChars))
			continue
		case len(terms) >= MaxKeyterms:
			warns = append(warns, fmt.Sprintf("oltre %d termini: %q e i seguenti non inviati", MaxKeyterms, short(t)))
			req.Keyterms = terms
			return req, warns, nil
		}
		seen[key] = true
		terms = append(terms, t)
	}
	req.Keyterms = terms
	return req, warns, nil
}

// Keyterms sono i termini da far riconoscere alla trascrizione: i nomi propri
// (names, già passati da CheckNames), nell'ordine dato, poi le parole scritte
// del dizionario, in ordine. Ripetizioni e il limite del provider li toglie
// CheckTranscribe, con un avviso.
func Keyterms(names []string, d Dictionary) []string {
	return append(slices.Clone(names), d.keys()...)
}

// MaxNames e MaxNameChars: nomi propri da far riconoscere alla trascrizione
// (giv qc --voice --nome, campo nomi del kit), entro i limiti dei termini di
// xAI.
const (
	MaxNames     = MaxKeyterms
	MaxNameChars = MaxKeytermChars
)

// CheckNames controlla i nomi propri da far riconoscere alla trascrizione e
// li restituisce senza spazi in testa e in coda: al più MaxNames, ognuno non
// vuoto, di al più MaxNameChars caratteri, UTF-8 valido, senza caratteri di
// controllo né invisibili. what è il nome del campo per i messaggi.
func CheckNames(what string, names []string) ([]string, error) {
	if len(names) > MaxNames {
		return nil, fmt.Errorf("%s: %d nomi, al più %d", what, len(names), MaxNames)
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		t := strings.TrimSpace(n)
		switch {
		case t == "":
			return nil, fmt.Errorf("%s vuoto", what)
		case !utf8.ValidString(t):
			return nil, fmt.Errorf("%s: testo non UTF-8", what)
		case strings.ContainsFunc(t, invisible):
			return nil, fmt.Errorf("%s %q: caratteri di controllo o invisibili non ammessi", what, short(strings.Map(visible, t)))
		case utf8.RuneCountInString(t) > MaxNameChars:
			return nil, fmt.Errorf("%s %q: %d caratteri, al più %d", what, short(t), utf8.RuneCountInString(t), MaxNameChars)
		}
		out = append(out, t)
	}
	return out, nil
}

// invisible: carattere di controllo o di formato (a capo, tabulazioni,
// direzione del testo, spazi di larghezza zero).
func invisible(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }

// visible toglie i caratteri invisibili per mostrare un testo in un messaggio.
func visible(r rune) rune {
	if invisible(r) {
		return '?'
	}
	return r
}

func audioFormatNames() []string {
	names := make([]string, 0, len(AudioFormats))
	for k := range AudioFormats {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// TranscribePrice è una tariffa della trascrizione, valida da From (zero = da
// sempre) fino alla successiva dello stesso provider.
type TranscribePrice struct {
	Provider   string
	From       time.Time
	USDPerHour float64 // dollari per ora di audio
}

// TranscribePrices è il listino della trascrizione (pagine prezzi, 22/11/2026):
// xAI 0,10 $ per ora di audio.
var TranscribePrices = []TranscribePrice{
	{Provider: XAI, USDPerHour: 0.10},
}

// EstimateTranscribe stima il costo della trascrizione di durationS secondi
// (misurati sul file) alla data at, arrotondati al minuto per eccesso: la
// granularità dell'addebito non è documentata, così la stima non sta sotto
// il vero (30 s: 0,001667 $ invece di 0,000833 $). nil
// col motivo se manca la tariffa o la durata: con un tetto la richiesta è
// non stimabile e non parte.
func EstimateTranscribe(provider string, durationS float64, at time.Time) (*Estimate, string) {
	usd, src, why := transcribeCost(provider, durationS, at, "listino")
	if why != "" {
		return nil, why
	}
	return &Estimate{USD: math.Round(usd*1e6) / 1e6, Exact: usd, Source: src}, ""
}

// TranscribeCost è il costo di una trascrizione di durationS secondi alla data
// at, al minuto per eccesso come la stima (giv lo calcola: il provider non
// riporta il costo). ok false senza
// tariffa o senza una durata valida.
func TranscribeCost(provider string, durationS float64, at time.Time) (usd float64, source string, ok bool) {
	usd, src, why := transcribeCost(provider, durationS, at, "calcolato da giv")
	return usd, src, why == ""
}

func transcribeCost(provider string, durationS float64, at time.Time, what string) (float64, string, string) {
	if math.IsNaN(durationS) || math.IsInf(durationS, 0) || durationS <= 0 {
		return 0, "", "durata dell'audio non misurata"
	}
	var p TranscribePrice
	found := false
	for _, q := range TranscribePrices {
		if q.Provider == provider && !q.From.After(at) && (!found || q.From.After(p.From)) {
			p, found = q, true
		}
	}
	if !found {
		return 0, "", fmt.Sprintf("nessuna tariffa della trascrizione per %s nel listino di giv", provider)
	}
	mins := math.Ceil(durationS / 60)
	usd := mins * p.USDPerHour / 60
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd < 0 {
		return 0, "", "tariffa della trascrizione non valida"
	}
	return usd, fmt.Sprintf("%s %s: %g $ per ora di audio × %.0f min (%.3f s arrotondati al minuto per eccesso)", what, provider, p.USDPerHour, mins, durationS), ""
}

// CleanWord ripulisce una parola trascritta: UTF-8 valido, senza caratteri di
// controllo, al più 100 caratteri.
func CleanWord(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	if r := []rune(s); len(r) > maxWordChars {
		s = string(r[:maxWordChars])
	}
	return strings.TrimSpace(s)
}
