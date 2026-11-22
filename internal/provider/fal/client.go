// Package fal è il provider fal.ai per i video: modelli dalle schede di giv
// (internal/caps, non un catalogo letto in rete), avvio sulla coda
// (queue.fal.run), attesa e risultato dagli indirizzi restituiti all'avvio,
// immagini locali caricate sullo storage di fal (rest.fal.ai) con scadenza di
// un'ora. La chiave va solo alle due origini dell'API; il PUT firmato e lo
// scaricamento del video partono senza chiave e solo verso indirizzi pubblici.
package fal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
)

// Origini dell'API di fal.
const (
	QueueURL = "https://queue.fal.run"
	RESTURL  = "https://rest.fal.ai"
)

const (
	name = "fal"
	// MaxVideoBytes è il tetto del video scaricato.
	MaxVideoBytes = 512 << 20
	maxJSON       = 4 << 20 // risposte JSON dell'API
)

// Config sono le origini e i tempi del client; i campi vuoti valgono come in
// produzione. I test puntano Queue e REST a un server finto e passano il suo
// Transport, che sostituisce tutte le connessioni (filtro degli indirizzi
// pubblici compreso).
type Config struct {
	Queue, REST  string
	Transport    http.RoundTripper
	PollInterval time.Duration // 0 = 10 s
}

// Client è il provider fal.
type Client struct {
	key         string
	queue, rest *url.URL
	api         *http.Client // verso queue e rest, con la chiave
	ext         *http.Client // PUT firmato e scaricamenti, senza chiave
	poll        time.Duration
}

// New prepara il client con la chiave FAL_KEY.
func New(key string, cfg Config) (*Client, error) {
	c := &Client{key: key, poll: cfg.PollInterval}
	if c.poll <= 0 {
		c.poll = 10 * time.Second
	}
	for _, o := range []struct {
		dst **url.URL
		raw string
		def string
	}{{&c.queue, cfg.Queue, QueueURL}, {&c.rest, cfg.REST, RESTURL}} {
		if o.raw == "" {
			o.raw = o.def
		}
		u, err := url.Parse(o.raw)
		if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("fal: origine %q non valida", o.raw)
		}
		*o.dst = &url.URL{Scheme: u.Scheme, Host: u.Host}
	}
	// nessun redirect sulle chiamate con la chiave: il 3xx diventa un errore
	c.api = &http.Client{Timeout: 120 * time.Second, Transport: cfg.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c.ext = externalClient(cfg.Transport)
	return c, nil
}

func (c *Client) Name() string { return name }

// Models elenca le schede video.
func (c *Client) Models(context.Context) ([]provider.ModelInfo, error) {
	var out []provider.ModelInfo
	for _, m := range caps.Fal() {
		out = append(out, provider.ModelInfo{Name: m.ID, Kind: "video", DisplayName: endpoints[m.ID]})
	}
	return out, nil
}

// VideoCaps sono le schede: nessuna chiamata.
func (c *Client) VideoCaps(context.Context) ([]caps.Model, error) { return caps.Fal(), nil }

func (c *Client) GenerateImage(context.Context, provider.ImageRequest) (*provider.Result, error) {
	return nil, errors.New("fal: in giv solo video, niente immagini")
}

// GenerateVideo carica le immagini locali, avvia il job sulla coda e lo
// attende fino al video scaricato.
func (c *Client) GenerateVideo(ctx context.Context, req provider.VideoRequest) (*provider.Result, error) {
	cd := cards[req.Model]
	if cd == nil {
		return nil, fmt.Errorf("fal: scheda %q sconosciuta (schede: %s)", req.Model, strings.Join(Cards(), ", "))
	}
	n := 0
	up := func(path string) (string, error) {
		n++
		return c.upload(ctx, path, n)
	}
	body, err := cd.body(req, up)
	if err != nil {
		return nil, err
	}
	job, err := c.start(ctx, cd.endpoint, body)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni %s)\n", job.ID, c.poll)
	if provider.OnJobStart != nil {
		provider.OnJobStart(name, job, req)
	}
	return c.wait(ctx, job, req.Timeout, false)
}

// start invia la richiesta alla coda, una volta sola: con un 5xx, un timeout o
// una risposta senza indirizzi validi il job potrebbe essere partito.
func (c *Client) start(ctx context.Context, endpoint string, body map[string]any) (provider.Job, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return provider.Job{}, err
	}
	var resp struct {
		RequestID string `json:"request_id"`
		StatusURL string `json:"status_url"`
	}
	if err := c.do(ctx, httpx.NoRetry, http.MethodPost, c.queue.String()+"/"+endpoint, payload, nil, &resp); err != nil {
		return provider.Job{}, httpx.Start(err)
	}
	if resp.RequestID == "" {
		return provider.Job{}, httpx.Start(errors.New("fal: nessun request_id nella risposta"))
	}
	job := provider.Job{ID: resp.RequestID, PollingURL: resp.StatusURL}
	if err := c.CheckJob(job); err != nil {
		return provider.Job{}, httpx.Start(err)
	}
	return job, nil
}

// FetchVideo riprende l'attesa di un job già partito e ne scarica il risultato,
// senza un nuovo avvio; il primo controllo dello stato è immediato.
func (c *Client) FetchVideo(ctx context.Context, job provider.Job, timeout time.Duration) (*provider.Result, error) {
	if err := c.CheckJob(job); err != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "attesa del job %s (polling ogni %s)\n", job.ID, c.poll)
	return c.wait(ctx, job, timeout, true)
}

// CheckJob rifiuta, senza richieste, un job che non può essere di fal:
// l'indirizzo di attesa (status_url, letto anche da file) deve stare
// sull'origine della coda, nella forma …/requests/<id>/status, senza query.
func (c *Client) CheckJob(job provider.Job) error {
	if !validID(job.ID) {
		return fmt.Errorf("fal: id del job %q non valido", job.ID)
	}
	if job.PollingURL == "" {
		return fmt.Errorf("fal: manca l'indirizzo di attesa del job %s", job.ID)
	}
	u, err := url.Parse(job.PollingURL)
	if err != nil || !c.sameOrigin(u, c.queue) || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("fal: l'indirizzo di attesa del job %s non è sulla coda di fal: rifiutato", job.ID)
	}
	segs := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return fmt.Errorf("fal: l'indirizzo di attesa non è quello del job %s: rifiutato", job.ID)
		}
	}
	n := len(segs)
	if n < 4 || segs[n-1] != "status" || segs[n-2] != url.PathEscape(job.ID) || segs[n-3] != "requests" {
		return fmt.Errorf("fal: l'indirizzo di attesa non è quello del job %s: rifiutato", job.ID)
	}
	return nil
}

// validID: da 1 a 256 caratteri ASCII stampabili, senza spazi né '/'.
func validID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r >= 0x7f || r == '/' {
			return false
		}
	}
	return true
}

// wait attende un job già partito fino al tetto e ne scarica il risultato;
// con immediate il primo controllo è subito (recupero).
func (c *Client) wait(ctx context.Context, job provider.Job, timeout time.Duration, immediate bool) (res *provider.Result, err error) {
	// da qui ogni errore porta l'id del job
	defer func() { err = httpx.Job(job.ID, err) }()
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	// il tetto copre attesa, controlli, ritentativi e scaricamento
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if err != nil && ctx.Err() == nil && errors.Is(tctx.Err(), context.DeadlineExceeded) {
			res, err = nil, fmt.Errorf("fal: timeout dopo %s", timeout)
		}
	}()
	for first := true; ; first = false {
		if !first || !immediate {
			select {
			case <-tctx.Done():
				return nil, tctx.Err()
			case <-time.After(c.poll):
			}
		}
		if res, err = c.poll1(tctx, job); err != nil || res != nil {
			return res, err
		}
		fmt.Fprint(os.Stderr, ".")
	}
}

// poll1 controlla una volta lo stato: risultato scaricato, errore, oppure nil
// e nil se il job è in coda o in corso. Su fal non c'è uno stato di
// fallimento: un job finito male è COMPLETED con error.
func (c *Client) poll1(ctx context.Context, job provider.Job) (*provider.Result, error) {
	var st struct {
		Status    string          `json:"status"`
		Error     json.RawMessage `json:"error"`
		ErrorType string          `json:"error_type"`
	}
	if err := c.do(ctx, httpx.RetryPoll, http.MethodGet, job.PollingURL, nil, nil, &st); err != nil {
		return nil, err
	}
	switch st.Status {
	case "IN_QUEUE", "IN_PROGRESS":
		return nil, nil
	case "COMPLETED":
	default:
		return nil, fmt.Errorf("fal: stato del job sconosciuto: %q", clip(st.Status, 40))
	}
	fmt.Fprintln(os.Stderr)
	if msg := jsonText(st.Error); msg != "" || st.ErrorType != "" {
		return nil, provider.JobFailed(fmt.Errorf("fal: job finito con errore: %s", clip(strings.TrimSpace(st.ErrorType+" "+msg), 400)))
	}
	return c.result(ctx, job)
}

// result legge il risultato del job (status_url senza /status) e scarica il video.
func (c *Client) result(ctx context.Context, job provider.Job) (*provider.Result, error) {
	var out struct {
		Video struct {
			URL string `json:"url"`
		} `json:"video"`
	}
	err := c.do(ctx, httpx.RetryPoll, http.MethodGet, strings.TrimSuffix(job.PollingURL, "/status"), nil, nil, &out)
	var he *httpx.Error
	if errors.As(err, &he) && (he.Status == http.StatusUnprocessableEntity || he.Status == 499) {
		// input rifiutato dal modello o job annullato: niente da scaricare
		return nil, provider.JobFailed(err)
	}
	if err != nil {
		return nil, err
	}
	if out.Video.URL == "" {
		return nil, errors.New("fal: job completato ma nessun video nella risposta")
	}
	data, err := c.download(ctx, out.Video.URL)
	if err != nil {
		return nil, err
	}
	return &provider.Result{Media: []provider.Media{{Mime: "video/mp4", Data: data}}, JobID: job.ID}, nil
}

// download scarica il video del risultato: senza chiave, solo https verso
// indirizzi pubblici, entro MaxVideoBytes.
func (c *Client) download(ctx context.Context, raw string) ([]byte, error) {
	if err := checkExternal(raw); err != nil {
		return nil, fmt.Errorf("fal: video del risultato: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("fal: video del risultato %s: indirizzo non valido", redact(raw))
	}
	resp, err := c.ext.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fal: scaricamento da %s: %w", redact(raw), netError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fal: scaricamento da %s: HTTP %d", redact(raw), resp.StatusCode)
	}
	if resp.ContentLength > MaxVideoBytes {
		return nil, fmt.Errorf("fal: video di %d byte, oltre il limite di %d MiB", resp.ContentLength, MaxVideoBytes>>20)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxVideoBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fal: scaricamento da %s: %w", redact(raw), netError(err))
	}
	if len(data) > MaxVideoBytes {
		return nil, fmt.Errorf("fal: video oltre il limite di %d MiB", MaxVideoBytes>>20)
	}
	return data, nil
}

// do è una chiamata con la chiave: solo verso le origini della coda e dello
// storage, con risposta JSON entro maxJSON.
func (c *Client) do(ctx context.Context, retry httpx.RetryFunc, method, raw string, body []byte, header http.Header, out any) error {
	u, err := url.Parse(raw)
	if err != nil || !(c.sameOrigin(u, c.queue) || c.sameOrigin(u, c.rest)) {
		return fmt.Errorf("fal: %s non è un'origine dell'API di fal: chiave non inviata", redact(raw))
	}
	return retry(ctx, func() error {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, raw, rdr)
		if err != nil {
			return err
		}
		for k, v := range header {
			req.Header[k] = v
		}
		req.Header.Set("Authorization", "Key "+c.key)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.api.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
		if err != nil {
			return err
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return &httpx.Error{Prefix: name, Status: resp.StatusCode, Msg: apiError(data),
				RetryAfter: httpx.ParseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		if len(data) > maxJSON {
			return fmt.Errorf("fal: risposta oltre %d MiB", maxJSON>>20)
		}
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("fal: risposta non valida: %w", err)
			}
		}
		return nil
	})
}

// sameOrigin: schema, host e porta (esplicita o di default) uguali, senza credenziali.
func (c *Client) sameOrigin(u, origin *url.URL) bool {
	return u != nil && u.User == nil && u.Host != "" && u.Scheme == origin.Scheme &&
		strings.EqualFold(u.Hostname(), origin.Hostname()) && port(u) == port(origin)
}

func port(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// apiError è il messaggio di un errore di fal: detail come testo, o come
// elenco di {msg, type} per gli errori del modello; mai l'input rimandato.
func apiError(data []byte) string {
	var e struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(data, &e) == nil && len(e.Detail) > 0 {
		if s := jsonText(e.Detail); s != "" {
			return clip(s, 400)
		}
		var list []struct {
			Msg  string `json:"msg"`
			Type string `json:"type"`
		}
		if json.Unmarshal(e.Detail, &list) == nil && len(list) > 0 {
			parts := make([]string, len(list))
			for i, d := range list {
				parts[i] = strings.TrimSpace(d.Type + ": " + d.Msg)
			}
			return clip(strings.Join(parts, "; "), 400)
		}
	}
	return clip(strings.TrimSpace(string(data)), 400)
}

// jsonText è il testo di un valore JSON: la stringa se lo è, il JSON grezzo
// altrimenti; "" per assente, null o stringa vuota.
func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
