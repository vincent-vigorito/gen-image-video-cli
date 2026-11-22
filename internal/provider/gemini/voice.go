package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/speech"
)

// Voce di Gemini (giv voice --provider gemini): POST /v1beta/interactions con
// un modello TTS, una sola richiesta a pagamento, senza redirect né
// ritentativi, chiave solo all'origine dell'API; GET /v1beta/voices per
// l'elenco (gratuito). Gemini non ha un dizionario né i tempi: le pronunce si
// sostituiscono nel testo (speech.Request.Spoken) e i sottotitoli si
// distribuiscono sulla durata. Il costo viene dai token riportati in usage,
// altrimenti dalla durata misurata (speech.UsageCost). Ciò che le pagine
// ufficiali non descrivono (usage nella sintesi, audio senza intestazione) si
// accetta in modo prudente: audio solo WAV o PCM a 16 bit dichiarato.

// voiceTimeout è il tempo massimo della richiesta di voce.
const voiceTimeout = 10 * time.Minute

// voicesTimeout è il tempo massimo dell'elenco delle voci.
const voicesTimeout = 60 * time.Second

// maxVoices: voci tenute dall'elenco (una pagina dell'API).
const maxVoices = 1000

// VoiceBody è il corpo JSON della richiesta di voce di Gemini per req (già
// passata da speech.Check): anche il piano di --dry-run. Il testo è quello
// detto, con le pronunce sostituite; lo stile va come annotazione.
func VoiceBody(req speech.Request) map[string]any {
	text := map[string]any{"type": "text", "text": req.Spoken()}
	if req.Style != "" {
		text["annotations"] = []any{map[string]any{"type": "speech_metadata", "style": req.Style}}
	}
	return map[string]any{
		"model":             req.Model,
		"input":             []any{map[string]any{"type": "user_input", "content": []any{text}}},
		"response_format":   map[string]any{"type": "audio"},
		"generation_config": map[string]any{"speech_config": []any{map[string]any{"voice": req.Voice}}},
	}
}

// Synthesize genera la voce con Gemini. Errori classificati con httpx.Start:
// 4xx e connessione mai aperta = non partita; 3xx, 5xx, rete e risposta non
// valida = *httpx.StartError (può essere addebitata, non si rilancia).
func (c *Client) Synthesize(ctx context.Context, req speech.Request) (*speech.Result, error) {
	payload, err := json.Marshal(VoiceBody(req))
	if err != nil {
		return nil, errors.New("gemini: richiesta di voce non valida")
	}
	at := time.Now()
	data, err := c.voiceCall(ctx, httpx.NoRetry, http.MethodPost, c.base()+"/interactions", payload, voiceTimeout)
	if err != nil {
		return nil, httpx.Start(err)
	}
	res, err := parseVoice(data, c.APIKey)
	if err != nil {
		// risposta 2xx: la voce è stata con ogni probabilità addebitata
		return nil, httpx.Start(fmt.Errorf("gemini: %w", err))
	}
	res.Chars = utf8.RuneCountInString(req.Spoken())
	if usd, src, ok := speech.UsageCost(req, res, at); ok {
		res.UsageUSD, res.UsageSource = usd, src
	} else {
		res.Warnings = append(res.Warnings, "costo della voce non calcolabile dai consumi: vale la stima")
	}
	return res, nil
}

// Voices elenca le voci di Gemini (GET /v1beta/voices, gratuito): la prima
// pagina, al più maxVoices; nomi ripuliti, solo quelli usabili con --voice.
func (c *Client) Voices(ctx context.Context) ([]speech.Voice, error) {
	data, err := c.voiceCall(stay(ctx), httpx.Retry, http.MethodGet, c.base()+"/voices?page_size=1000", nil, voicesTimeout)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Voices []struct {
			ID          any `json:"id"`
			Name        any `json:"name"`
			DisplayName any `json:"display_name"`
			Language    any `json:"language_code"`
		} `json:"voices"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return nil, errors.New("gemini: elenco delle voci non valido")
	}
	out := []speech.Voice{}
	for _, v := range resp.Voices {
		id, _ := v.ID.(string)
		if id == "" {
			id, _ = v.Name.(string)
		}
		id = strings.TrimPrefix(id, "voices/")
		if !speech.ValidVoice(id) {
			continue
		}
		name, _ := v.DisplayName.(string)
		lang, _ := v.Language.(string)
		out = append(out, speech.Voice{ID: id, Name: safeText(name, 80), Language: safeText(lang, 35)})
		if len(out) >= maxVoices {
			break
		}
	}
	return out, nil
}

// voiceCall è la richiesta della voce: solo sull'origine dell'API (lì va la
// chiave), nessun redirect sul POST (checkRedirect: un 3xx torna come errore
// HTTP, esito incerto), ritentativi solo come dice retry, risposta entro
// speech.MaxResponseBytes, errori filtrati (videoAPIError, chiave tolta).
func (c *Client) voiceCall(ctx context.Context, retry httpx.RetryFunc, method, rawURL string, body []byte, timeout time.Duration) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !c.apiOrigin(u) {
		return nil, errors.New("gemini: indirizzo fuori dall'origine dell'API: richiesta non fatta")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out []byte
	err = retry(ctx, func() error {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
		if err != nil {
			return errors.New("gemini: richiesta non valida")
		}
		req.Header.Set("x-goog-api-key", c.APIKey)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.videoHTTP().Do(req)
		if err != nil {
			return netError(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, speech.MaxResponseBytes+1))
		if err != nil {
			return netError(err)
		}
		if len(data) > speech.MaxResponseBytes {
			return fmt.Errorf("gemini: risposta oltre %d MiB", speech.MaxResponseBytes>>20)
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			msg := "redirect non seguito"
			if resp.StatusCode < 300 || resp.StatusCode > 399 {
				msg = videoAPIError(hideKey(data, c.APIKey)) // la chiave via prima di accorciare
			}
			return &httpx.Error{Prefix: "gemini", Status: resp.StatusCode, Msg: msg,
				RetryAfter: httpx.ParseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		out = data
		return nil
	})
	return out, err
}

// hideKey toglie la chiave da un corpo di risposta prima di leggerne i
// messaggi (che poi si accorciano: una chiave tagliata a metà resterebbe).
func hideKey(data []byte, key string) []byte {
	if key == "" {
		return data
	}
	return bytes.ReplaceAll(data, []byte(key), []byte("***"))
}

// parseVoice legge la risposta della voce: l'ultimo audio prodotto dal
// modello (steps, o outputs nello schema senza steps), in base64; WAV, oppure
// PCM a 16 bit dichiarato (audio/L16, audio/pcm) avvolto in un WAV. I
// messaggi d'errore sono senza la chiave apiKey.
func parseVoice(data []byte, apiKey string) (*speech.Result, error) {
	var it interaction
	if json.Unmarshal(data, &it) != nil {
		return nil, errors.New("risposta della voce non valida (JSON)")
	}
	switch it.Status {
	case "failed", "cancelled":
		msg := errorText(hideKey(it.Error, apiKey))
		if msg == "" {
			msg = "nessun dettaglio"
		}
		return nil, fmt.Errorf("voce %s: %s", it.Status, msg)
	case "in_progress":
		return nil, errors.New("voce ancora in corso nella risposta: nessun audio")
	}
	a := it.audio()
	if a == nil || a.Data == "" {
		return nil, errors.New("risposta della voce senza audio")
	}
	raw, err := decodeBase64(a.Data)
	if err != nil || len(raw) == 0 {
		return nil, errors.New("risposta della voce senza audio leggibile")
	}
	res := &speech.Result{}
	if !bytes.HasPrefix(raw, []byte("RIFF")) {
		rate, ch, ok := pcmFormat(a.MimeType)
		if !ok {
			return nil, fmt.Errorf("audio della voce non è un WAV (tipo %q)", safeText(a.MimeType, 40))
		}
		raw = speech.PCM16WAV(raw, rate, ch)
		res.Warnings = append(res.Warnings, fmt.Sprintf("gemini ha restituito PCM senza intestazione: WAV ricostruito a %d Hz, %d canali", rate, ch))
	}
	w, err := speech.ParseWAV(raw)
	if err != nil {
		return nil, fmt.Errorf("audio della voce non valido: %v", err)
	}
	res.Audio, res.WAV, res.DurationS = raw, w, w.DurationS
	if it.Status == "incomplete" {
		res.Warnings = append(res.Warnings, "gemini: risposta incompleta, l'audio può essere troncato")
	}
	res.Usage = it.Usage.tokens()
	return res, nil
}

// audio è l'ultimo audio prodotto dal modello (non negli ingressi dell'utente).
func (it *interaction) audio() *content {
	var a *content
	for i := range it.Steps {
		if it.Steps[i].Type == "user_input" {
			continue
		}
		for j := range it.Steps[i].Content {
			if it.Steps[i].Content[j].Type == "audio" {
				a = &it.Steps[i].Content[j]
			}
		}
	}
	if a != nil {
		return a
	}
	for j := range it.Outputs {
		if it.Outputs[j].Type == "audio" {
			a = &it.Outputs[j]
		}
	}
	return a
}

// tokens sono i token riportati per il costo: nil se mancano i totali di
// ingresso o d'uscita o se un valore è negativo.
func (u *usage) tokens() *speech.TokenUsage {
	if u == nil || u.TotalInput == nil || u.TotalOutput == nil || *u.TotalInput < 0 || *u.TotalOutput < 0 {
		return nil
	}
	t := &speech.TokenUsage{Input: *u.TotalInput, Output: *u.TotalOutput}
	if u.TotalThought != nil {
		if *u.TotalThought < 0 {
			return nil
		}
		t.Thought = *u.TotalThought
	}
	return t
}

// pcmFormat legge frequenza e canali di un tipo audio/L16 o audio/pcm (a 16
// bit); senza parametri 24 kHz mono, come la voce di Gemini.
func pcmFormat(ct string) (rate, channels int, ok bool) {
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || (mt != "audio/l16" && mt != "audio/pcm") {
		return 0, 0, false
	}
	rate, channels = speech.SampleRate, 1
	if v, found := params["rate"]; found {
		n, err := strconv.Atoi(v)
		if err != nil || n < 8000 || n > 192000 {
			return 0, 0, false
		}
		rate = n
	}
	if v, found := params["channels"]; found {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 2 {
			return 0, 0, false
		}
		channels = n
	}
	return rate, channels, true
}

func decodeBase64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if d, err := base64.StdEncoding.DecodeString(s); err == nil {
		return d, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}
