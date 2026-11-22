package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/speech"
)

// Trascrizione di xAI (giv qc --voice): POST /v1/stt multipart col file come
// ultimo campo, una sola richiesta a pagamento, senza redirect né
// ritentativi, chiave solo all'origine dell'API. La lingua non si invia (il
// codice it della trascrizione non è verificato): si controlla quella
// rilevata. Un caricamento che non arriva fino in fondo è un errore ordinario
// (il file non è arrivato, decisione 17); ogni altro esito non riuscito dopo
// l'invio completo è incerto (*httpx.StartError).

// sttTimeout è il tempo massimo della trascrizione.
const sttTimeout = 10 * time.Minute

// GrokSTTFields sono i campi del caricamento prima del file (anche nel piano
// di --dry-run): un keyterm per termine.
func GrokSTTFields(req speech.TranscribeRequest) map[string]any {
	terms := req.Keyterms
	if terms == nil {
		terms = []string{}
	}
	return map[string]any{"keyterm": terms}
}

// grokSTTBody è il corpo multipart della trascrizione: i keyterm e poi, come
// ultimo campo, il file con un nome generico (nessun percorso locale va al
// provider).
func grokSTTBody(req speech.TranscribeRequest) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, t := range req.Keyterms {
		if err := w.WriteField("keyterm", t); err != nil {
			return nil, "", err
		}
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="audio.%s"`, req.Format))
	h.Set("Content-Type", speech.AudioFormats[req.Format])
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(req.Audio); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

// Transcribe trascrive l'audio con xAI (req già passata da
// speech.CheckTranscribe).
func (c *Client) Transcribe(ctx context.Context, req speech.TranscribeRequest) (*speech.Transcript, error) {
	if !c.opts.Grok {
		return nil, fmt.Errorf("%s: trascrizione non disponibile", c.name)
	}
	if _, ok := speech.AudioFormats[req.Format]; !ok || len(req.Audio) == 0 {
		return nil, fmt.Errorf("%s: richiesta di trascrizione non valida", c.name)
	}
	body, ct, err := grokSTTBody(req)
	if err != nil {
		return nil, fmt.Errorf("%s: richiesta di trascrizione non valida", c.name)
	}
	at := time.Now()
	data, err := c.sttPost(ctx, c.baseURL+"/stt", body, ct)
	if err != nil {
		return nil, err
	}
	tr, err := parseGrokTranscript(data)
	if err != nil {
		// risposta 2xx: la trascrizione è stata con ogni probabilità addebitata
		return nil, httpx.Start(fmt.Errorf("%s: %w", c.name, err))
	}
	if usd, src, ok := speech.TranscribeCost(speech.XAI, tr.DurationS, at); ok {
		tr.UsageUSD, tr.UsageSource = usd, src
	}
	return tr, nil
}

// errUpload: il caricamento non è arrivato fino in fondo.
var errUpload = errors.New("caricamento dell'audio non riuscito, nessuna trascrizione partita")

// sentReader conta i byte del corpo letti dal client HTTP.
type sentReader struct {
	r    io.Reader
	sent atomic.Int64
}

func (s *sentReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	s.sent.Add(int64(n))
	return n, err
}

// sttPost è la POST della trascrizione: solo sull'origine dell'API, nessun
// redirect, nessun ritentativo (il corpo non si può rileggere), risposta
// entro speech.MaxTranscriptBytes, messaggi filtrati. Errori già
// classificati: corpo non inviato tutto = errore ordinario (errUpload); 4xx o
// connessione mai aperta come sono; il resto *httpx.StartError.
func (c *Client) sttPost(ctx context.Context, rawURL string, body []byte, ct string) ([]byte, error) {
	if !c.apiHost(rawURL) {
		return nil, fmt.Errorf("%s: %s non è sull'origine dell'API: richiesta non fatta", c.name, redactURL(rawURL))
	}
	cl := c.noRedirectClient()
	cl.Timeout = max(cl.Timeout, sttTimeout)
	sr := &sentReader{r: bytes.NewReader(body)}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, sr)
	if err != nil {
		return nil, fmt.Errorf("%s: richiesta non valida", c.name)
	}
	req.ContentLength = int64(len(body)) // GetBody nil: il client non lo reinvia
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", ct)
	resp, err := cl.Do(req)
	if err != nil {
		err = hideURL(err)
		if sr.sent.Load() < int64(len(body)) {
			if s := httpx.Start(err); !isStartError(s) {
				return nil, s // connessione mai aperta
			}
			return nil, fmt.Errorf("%s: %w: %w", c.name, errUpload, err)
		}
		return nil, httpx.Start(err)
	}
	defer resp.Body.Close()
	data, err := readAtMost(resp.Body, speech.MaxTranscriptBytes, "risposta")
	if err != nil {
		return nil, httpx.Start(fmt.Errorf("%s: %w", c.name, hideURL(err)))
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := "redirect non seguito"
		if resp.StatusCode < 300 || resp.StatusCode > 399 {
			msg = messageOr(voiceErrorText(data, resp.Header.Get("Content-Type"), c.apiKey), "nessun messaggio leggibile nella risposta")
		}
		return nil, httpx.Start(&httpx.Error{Prefix: c.name, Status: resp.StatusCode, Msg: msg,
			RetryAfter: httpx.ParseRetryAfter(resp.Header.Get("Retry-After"))})
	}
	return data, nil
}

func isStartError(err error) bool {
	var se *httpx.StartError
	return errors.As(err, &se)
}

// parseGrokTranscript legge la risposta della trascrizione: text, language,
// duration e words [{text, start, end}]. Testo e parole ripuliti; parole
// oltre speech.MaxWords o con tempi non validi scartate con un avviso (il
// confronto usa il testo).
func parseGrokTranscript(data []byte) (*speech.Transcript, error) {
	var r struct {
		Text     *string         `json:"text"`
		Language any             `json:"language"`
		Duration any             `json:"duration"`
		Words    json.RawMessage `json:"words"`
	}
	if json.Unmarshal(data, &r) != nil {
		return nil, errors.New("risposta della trascrizione non valida (JSON)")
	}
	if r.Text == nil {
		return nil, errors.New("risposta della trascrizione senza testo")
	}
	tr := &speech.Transcript{Text: cleanTranscript(*r.Text)}
	if lang, ok := r.Language.(string); ok {
		tr.Language = shortText(redactText(speech.CleanWord(lang)), 35)
	}
	if d, ok := r.Duration.(float64); ok && !math.IsNaN(d) && !math.IsInf(d, 0) && d >= 0 {
		tr.DurationS = d
	}
	words, err := grokWords(r.Words)
	if err != nil {
		tr.Warnings = append(tr.Warnings, fmt.Sprintf("parole coi tempi non usabili (%v): il confronto usa il testo", err))
	} else {
		tr.Words = words
	}
	return tr, nil
}

// cleanTranscript ripulisce il testo trascritto: UTF-8 valido, senza
// caratteri di controllo (gli a capo diventano spazi).
func cleanTranscript(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\r' {
			return ' '
		}
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// grokWords legge words un elemento per volta, al più speech.MaxWords.
func grokWords(raw json.RawMessage) ([]speech.Word, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("nessuna parola")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if d, ok := tok.(json.Delim); err != nil || !ok || d != '[' {
		return nil, errors.New("forma delle parole non riconosciuta")
	}
	out := []speech.Word{}
	for dec.More() {
		if len(out) >= speech.MaxWords {
			return nil, fmt.Errorf("oltre %d parole", speech.MaxWords)
		}
		var w struct {
			Text  *string  `json:"text"`
			Start *float64 `json:"start"`
			End   *float64 `json:"end"`
		}
		if err := dec.Decode(&w); err != nil {
			return nil, errors.New("forma delle parole non riconosciuta")
		}
		if w.Text == nil || w.Start == nil || w.End == nil || !finiteTime(*w.Start) || !finiteTime(*w.End) || *w.End < *w.Start {
			return nil, fmt.Errorf("parola %d senza testo o tempi validi", len(out)+1)
		}
		if !utf8.ValidString(*w.Text) {
			return nil, fmt.Errorf("parola %d non valida", len(out)+1)
		}
		out = append(out, speech.Word{Text: speech.CleanWord(*w.Text), Start: *w.Start, End: *w.End})
	}
	if _, err := dec.Token(); err != nil {
		return nil, errors.New("forma delle parole non riconosciuta")
	}
	return out, nil
}

func finiteTime(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 }
