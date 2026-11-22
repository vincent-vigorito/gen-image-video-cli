package openaicompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/speech"
)

// Voce di xAI (giv voice): POST /v1/tts, una sola richiesta a pagamento, senza
// redirect né ritentativi, chiave solo all'origine dell'API; GET
// /v1/tts/voices per l'elenco (gratuito). I punti non verificati della
// risposta (forma dei tempi, canali del WAV, errori in testo semplice) si
// accettano in modo tollerante: i tempi che non tornano si scartano con un
// avviso, l'audio deve essere un WAV.

// voiceTimeout è il tempo massimo della richiesta di voce (xAI: fino a 15
// minuti per i testi lunghi).
const voiceTimeout = 10 * time.Minute

// GrokVoiceBody è il corpo JSON della richiesta di voce di xAI per req (già
// passata da speech.Check): anche il piano di --dry-run.
func GrokVoiceBody(req speech.Request) map[string]any {
	body := map[string]any{
		"text":            req.Text,
		"language":        req.Language,
		"voice_id":        req.Voice,
		"output_format":   map[string]any{"codec": "wav", "sample_rate": speech.SampleRate},
		"with_timestamps": true,
	}
	if req.Speed != 0 {
		body["speed"] = req.Speed
	}
	if len(req.Dict) > 0 {
		body["replace"] = map[string]string(req.Dict)
	}
	return body
}

// Synthesize genera la voce con xAI. Errori classificati con httpx.Start: 4xx
// e connessione mai aperta = non partita; 3xx, 5xx, rete e risposta non
// valida = *httpx.StartError (può essere addebitata, non si rilancia).
func (c *Client) Synthesize(ctx context.Context, req speech.Request) (*speech.Result, error) {
	if !c.opts.Grok {
		return nil, fmt.Errorf("%s: voce non disponibile", c.name)
	}
	payload, err := json.Marshal(GrokVoiceBody(req))
	if err != nil {
		return nil, fmt.Errorf("%s: richiesta di voce non valida", c.name)
	}
	data, ct, err := c.voicePost(ctx, c.baseURL+"/tts", payload)
	if err != nil {
		return nil, httpx.Start(err)
	}
	res, err := parseGrokVoice(data, ct)
	if err != nil {
		// risposta 2xx: la voce è stata con ogni probabilità addebitata
		return nil, httpx.Start(fmt.Errorf("%s: %w", c.name, err))
	}
	res.Chars = utf8.RuneCountInString(req.Text)
	return res, nil
}

// Voices elenca le voci di xAI (GET /v1/tts/voices, gratuito).
func (c *Client) Voices(ctx context.Context) ([]speech.Voice, error) {
	if !c.opts.Grok {
		return nil, fmt.Errorf("%s: voce non disponibile", c.name)
	}
	var resp struct {
		Voices []struct {
			ID       any `json:"voice_id"`
			Name     any `json:"name"`
			Language any `json:"language"`
		} `json:"voices"`
	}
	if err := c.apiCall(ctx, httpx.Retry, http.MethodGet, c.baseURL+"/tts/voices", "", nil, &resp); err != nil {
		return nil, err
	}
	const maxVoices = 1000
	out := []speech.Voice{}
	for _, v := range resp.Voices {
		id, _ := v.ID.(string)
		if !printableID(id) || len(id) > 128 {
			continue
		}
		name, _ := v.Name.(string)
		lang, _ := v.Language.(string)
		out = append(out, speech.Voice{ID: id, Name: shortText(redactText(name), 80), Language: shortText(redactText(lang), 35)})
		if len(out) >= maxVoices {
			break
		}
	}
	return out, nil
}

// voicePost è la POST della voce: solo sull'origine dell'API, nessun
// redirect (un 3xx torna come errore HTTP), nessun ritentativo, risposta
// entro speech.MaxResponseBytes; restituisce corpo e Content-Type. I
// messaggi d'errore sono filtrati, anche quelli in testo semplice.
func (c *Client) voicePost(ctx context.Context, rawURL string, body []byte) ([]byte, string, error) {
	if !c.apiHost(rawURL) {
		return nil, "", fmt.Errorf("%s: %s non è sull'origine dell'API: richiesta non fatta", c.name, redactURL(rawURL))
	}
	cl := c.noRedirectClient()
	cl.Timeout = max(cl.Timeout, voiceTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("%s: richiesta non valida", c.name)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, "", hideURL(err)
	}
	defer resp.Body.Close()
	data, err := readAtMost(resp.Body, speech.MaxResponseBytes, "risposta")
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", c.name, hideURL(err))
	}
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := "redirect non seguito"
		if resp.StatusCode < 300 || resp.StatusCode > 399 {
			msg = messageOr(voiceErrorText(data, ct, c.apiKey), "nessun messaggio leggibile nella risposta")
		}
		return nil, "", &httpx.Error{Prefix: c.name, Status: resp.StatusCode, Msg: msg,
			RetryAfter: httpx.ParseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	return data, ct, nil
}

// voiceErrorText è il messaggio di una risposta d'errore della voce: quello
// del JSON (error.code, error.message o message) oppure, per una risposta in
// testo semplice (xAI può rispondere così), il testo ripulito e corto; mai
// HTML né altri corpi.
func voiceErrorText(data []byte, ct, apiKey string) string {
	if msg := providerMessage(data); msg != "" {
		return redactText(msg, apiKey)
	}
	mt, _, _ := mime.ParseMediaType(ct)
	if ct != "" && mt != "text/plain" {
		return ""
	}
	s := string(data)
	if !utf8.ValidString(s) || strings.ContainsAny(s, "<>{}") {
		return ""
	}
	return shortText(redactText(s, apiKey), 300)
}

// shortText accorcia s a n caratteri.
func shortText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// parseGrokVoice legge la risposta della voce: JSON con l'audio in base64 e i
// tempi (with_timestamps), oppure i byte del WAV.
func parseGrokVoice(data []byte, ct string) (*speech.Result, error) {
	mt, _, _ := mime.ParseMediaType(ct)
	res := &speech.Result{}
	audio := data
	var declared *float64
	var ts []speech.CharTime
	var tsErr error
	isJSON := mt == "application/json" || (mt == "" && len(bytes.TrimSpace(data)) > 0 && bytes.TrimSpace(data)[0] == '{')
	if isJSON {
		var r struct {
			Audio           string          `json:"audio"`
			ContentType     string          `json:"content_type"`
			Duration        *float64        `json:"duration"`
			AudioTimestamps json.RawMessage `json:"audio_timestamps"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, errors.New("risposta della voce non valida (JSON)")
		}
		dec, err := decodeBase64(r.Audio)
		if err != nil || len(dec) == 0 {
			return nil, errors.New("risposta della voce senza audio leggibile")
		}
		audio, declared = dec, r.Duration
		ts, tsErr = grokTimings(r.AudioTimestamps)
	} else {
		tsErr = errors.New("xai non ha restituito i tempi")
	}
	w, err := speech.ParseWAV(audio)
	if err != nil {
		return nil, fmt.Errorf("audio della voce non valido: %v", err)
	}
	res.Audio, res.WAV, res.DurationS = audio, w, w.DurationS
	if declared != nil && !math.IsNaN(*declared) && !math.IsInf(*declared, 0) && math.Abs(*declared-w.DurationS) > 0.5 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("durata dichiarata da xai %.2f s, misurata sul WAV %.2f s: vale quella misurata", *declared, w.DurationS))
	}
	if tsErr == nil {
		tsErr = speech.CheckTimings(ts, w.DurationS)
	}
	if tsErr != nil {
		res.Warnings = append(res.Warnings, fmt.Sprintf("tempi per carattere non usabili (%v): i sottotitoli si distribuiranno sulla durata", tsErr))
	} else {
		res.Timings = ts
	}
	return res, nil
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if d, err := base64.StdEncoding.DecodeString(s); err == nil {
		return d, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}

// grokTimings legge audio_timestamps: graph_chars (elenco di stringhe, o una
// stringa) e graph_times in una delle due forme descritte da xAI (oggetti
// {start, end} o coppie [inizio, fine]), oppure soli inizi (la fine è
// l'inizio seguente). Numero di caratteri e di tempi uguale; al più
// speech.MaxTimings elementi, contati durante la lettura (oltre, i tempi si
// scartano senza tenerli in memoria).
func grokTimings(raw json.RawMessage) ([]speech.CharTime, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, errors.New("xai non ha restituito i tempi")
	}
	var at struct {
		Chars json.RawMessage `json:"graph_chars"`
		Times json.RawMessage `json:"graph_times"`
	}
	if json.Unmarshal(raw, &at) != nil {
		return nil, errors.New("forma dei tempi non riconosciuta")
	}
	var chars []string
	err := eachElem(at.Chars, func(el json.RawMessage) error {
		var c string
		if json.Unmarshal(el, &c) != nil {
			return errors.New("caratteri dei tempi non riconosciuti")
		}
		chars = append(chars, c)
		return nil
	})
	if errors.Is(err, errNotList) {
		var s string
		if json.Unmarshal(at.Chars, &s) != nil {
			return nil, errors.New("caratteri dei tempi non riconosciuti")
		}
		if utf8.RuneCountInString(s) > speech.MaxTimings {
			return nil, fmt.Errorf("oltre %d caratteri nei tempi", speech.MaxTimings)
		}
		chars = nil
		for _, r := range s {
			chars = append(chars, string(r))
		}
	} else if err != nil {
		return nil, err
	}
	var starts, ends []float64
	form := byte(0)
	err = eachElem(at.Times, func(el json.RawMessage) error {
		f := el[0]
		if f == '-' || (f >= '0' && f <= '9') {
			f = '0'
		}
		if form == 0 {
			form = f
		}
		if f != form {
			return errors.New("forma dei tempi non riconosciuta")
		}
		switch f {
		case '0':
			var v float64
			if json.Unmarshal(el, &v) != nil {
				return errors.New("tempo non valido")
			}
			starts = append(starts, v)
		case '[':
			var p []float64
			if json.Unmarshal(el, &p) != nil || len(p) != 2 {
				return errors.New("coppia di tempi non valida")
			}
			starts, ends = append(starts, p[0]), append(ends, p[1])
		case '{':
			var o struct {
				Start *float64 `json:"start"`
				End   *float64 `json:"end"`
			}
			if json.Unmarshal(el, &o) != nil || o.Start == nil || o.End == nil {
				return errors.New("tempo senza inizio o fine")
			}
			starts, ends = append(starts, *o.Start), append(ends, *o.End)
		default:
			return errors.New("forma dei tempi non riconosciuta")
		}
		return nil
	})
	if errors.Is(err, errNotList) {
		return nil, errors.New("forma dei tempi non riconosciuta")
	}
	if err != nil {
		return nil, err
	}
	if form == '0' { // soli inizi: la fine è l'inizio seguente
		for i := range starts {
			if i+1 < len(starts) {
				ends = append(ends, starts[i+1])
			} else {
				ends = append(ends, starts[i])
			}
		}
	}
	if len(chars) == 0 || len(chars) != len(starts) {
		return nil, fmt.Errorf("%d caratteri e %d tempi", len(chars), len(starts))
	}
	out := make([]speech.CharTime, len(chars))
	for i := range chars {
		if utf8.RuneCountInString(chars[i]) > 8 || !utf8.ValidString(chars[i]) {
			return nil, fmt.Errorf("carattere %d dei tempi non valido", i+1)
		}
		out[i] = speech.CharTime{Char: chars[i], Start: starts[i], End: ends[i]}
	}
	return out, nil
}

// errNotList: il valore JSON non è un elenco.
var errNotList = errors.New("non è un elenco")

// eachElem chiama fn per ogni elemento dell'elenco JSON raw, leggendo un
// elemento per volta: oltre speech.MaxTimings elementi si ferma con un errore.
func eachElem(raw json.RawMessage, fn func(json.RawMessage) error) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if d, ok := tok.(json.Delim); err != nil || !ok || d != '[' {
		return errNotList
	}
	for n := 1; dec.More(); n++ {
		if n > speech.MaxTimings {
			return fmt.Errorf("oltre %d tempi", speech.MaxTimings)
		}
		var el json.RawMessage
		if err := dec.Decode(&el); err != nil || len(el) == 0 {
			return errors.New("forma dei tempi non riconosciuta")
		}
		if err := fn(el); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return errors.New("forma dei tempi non riconosciuta")
	}
	return nil
}
