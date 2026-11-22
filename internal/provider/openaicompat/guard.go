package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
)

// Richieste protette, comuni a tutti i dialetti (decisione 15):
//   - apiCall: chiamate all'API (avvio a pagamento, caricamento, attesa) solo
//     sull'origine dell'API, senza redirect (un 3xx è un errore, all'avvio un
//     esito incerto), risposta entro maxAPIBody, messaggio d'errore filtrato
//     prima del ritentativo;
//   - startedJob: id e indirizzo di attesa di un job appena avviato,
//     controllati prima di registrarlo o attenderlo;
//   - fetchResult: l'unico scaricamento dei risultati (avvio, attesa e
//     recupero con giv job fetch): sull'origine dell'API con la chiave e senza
//     redirect, altrove solo https senza credenziali né chiave, connessioni
//     dirette solo verso indirizzi pubblici, redirect rivalidati e senza Referer;
//   - redactText, jobErrorText, hideURL, hideInMessages: messaggi senza corpo
//     grezzo, data URI, URL completi né valori nascosti.

const (
	maxAPIBody    = 64 << 20 // risposta JSON dell'API, anche con le immagini in base64 di /images
	maxResultBody = 1 << 30  // risultato scaricato (video o immagine)
)

// resultTransport sostituisce le connessioni degli scaricamenti fuori
// dall'origine dell'API (test); nil = dirette, senza proxy, solo verso
// indirizzi pubblici.
var resultTransport http.RoundTripper

// apiCall fa una richiesta all'API del provider con la politica di ripetizione
// retry (httpx.NoRetry per avvii e caricamenti, httpx.RetryPoll per l'attesa):
// solo sull'origine dell'API, senza seguire redirect, risposta entro
// maxAPIBody decodificata in out. Gli errori arrivano già filtrati al
// ritentativo, che li stampa.
func (c *Client) apiCall(ctx context.Context, retry httpx.RetryFunc, method, rawURL, contentType string, body []byte, out any) error {
	if !c.apiHost(rawURL) {
		return fmt.Errorf("%s: %s non è sull'origine dell'API: richiesta non fatta", c.name, redactURL(rawURL))
	}
	hide := hiddenIn(ctx)
	return retry(ctx, func() error {
		var rdr io.Reader
		if body != nil {
			rdr = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
		if err != nil {
			return fmt.Errorf("%s: richiesta non valida", c.name)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := c.noRedirectClient().Do(req)
		if err != nil {
			return hideURL(err)
		}
		defer resp.Body.Close()
		data, err := readAtMost(resp.Body, maxAPIBody, "risposta")
		if err != nil {
			return fmt.Errorf("%s: %w", c.name, hideURL(err))
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			msg := "redirect non seguito"
			if resp.StatusCode < 300 || resp.StatusCode > 399 {
				msg = messageOr(redactText(providerMessage(data), hide...), "nessun messaggio leggibile nella risposta")
			}
			return &httpx.Error{Prefix: c.name, Status: resp.StatusCode, Msg: msg,
				RetryAfter: httpx.ParseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		if out != nil {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("%s: risposta non valida: %w", c.name, err)
			}
		}
		return nil
	})
}

// noRedirectClient è il client dell'API che non segue nessun redirect: il
// 3xx torna come risposta.
func (c *Client) noRedirectClient() *http.Client {
	cl := *c.http
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cl
}

// startedJob controlla il job restituito da un avvio prima di registrarlo o
// attenderlo: id stampabile, indirizzo di attesa sull'origine dell'API e nel
// percorso del job (CheckJob). Chi avvia avvolge l'errore in httpx.Start.
func (c *Client) startedJob(job provider.Job) error {
	if !printableID(job.ID) {
		return fmt.Errorf("%s: id del job non valido nella risposta", c.name)
	}
	return c.CheckJob(job)
}

// fetchResult scarica un risultato indicato dal provider. Sull'origine
// dell'API con la chiave e senza seguire redirect: un redirect si segue con
// externalGet, senza chiave. Fuori origine solo con externalGet.
func (c *Client) fetchResult(ctx context.Context, raw string) ([]byte, error) {
	if !c.apiHost(raw) {
		return c.externalGet(ctx, raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: indirizzo del risultato non valido", c.name)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.noRedirectClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: download: %w", c.name, hideURL(err))
	}
	defer resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode >= 300 && resp.StatusCode < 400 && loc != "" {
		u, err := resp.Request.URL.Parse(loc)
		if err != nil {
			return nil, fmt.Errorf("%s: redirect del risultato non valido", c.name)
		}
		return c.externalGet(ctx, u.String())
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: download HTTP %d", c.name, resp.StatusCode)
	}
	data, err := readAtMost(resp.Body, maxResultBody, "risultato")
	if err != nil {
		return nil, fmt.Errorf("%s: download: %w", c.name, hideURL(err))
	}
	return data, nil
}

// externalGet scarica fuori dall'origine dell'API: https senza credenziali,
// nessuna chiave, connessione diretta solo verso indirizzi pubblici
// (controllati sull'indirizzo effettivo), redirect rivalidati e senza Referer.
func (c *Client) externalGet(ctx context.Context, raw string) ([]byte, error) {
	if err := checkExternal(raw); err != nil {
		return nil, fmt.Errorf("%s: risultato: %w", c.name, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: indirizzo del risultato non valido", c.name)
	}
	resp, err := externalClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: download: %w", c.name, hideURL(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: download HTTP %d", c.name, resp.StatusCode)
	}
	data, err := readAtMost(resp.Body, maxResultBody, "risultato")
	if err != nil {
		return nil, fmt.Errorf("%s: download: %w", c.name, hideURL(err))
	}
	return data, nil
}

func externalClient() *http.Client {
	rt := resultTransport
	if rt == nil {
		rt = &http.Transport{
			DialContext:            (&net.Dialer{Timeout: 30 * time.Second, Control: publicOnly}).DialContext,
			TLSHandshakeTimeout:    30 * time.Second,
			ResponseHeaderTimeout:  60 * time.Second,
			MaxResponseHeaderBytes: 1 << 20,
		}
	}
	return &http.Client{
		Timeout:   10 * time.Minute,
		Transport: rt,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Referer")
			if len(via) >= 5 {
				return errors.New("troppi redirect")
			}
			if err := checkExternal(req.URL.String()); err != nil {
				return fmt.Errorf("redirect: %w", err)
			}
			return nil
		},
	}
}

// readAtMost legge al massimo limit byte: oltre è un errore.
func readAtMost(r io.Reader, limit int64, what string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s oltre il limite di %d MiB: scartata", what, limit>>20)
	}
	return data, nil
}

// reti non pubbliche oltre a quelle riconosciute da netip (privati, loopback,
// link-local, multicast), come nel provider fal
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
}

// publicOnly è il controllo del dialer, sull'indirizzo già risolto.
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	ok := ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
	for _, p := range nonPublic {
		ok = ok && !p.Contains(ip)
	}
	if !ok {
		return fmt.Errorf("indirizzo %s non pubblico: connessione rifiutata", ip)
	}
	return nil
}

// checkExternal controlla un indirizzo fuori dall'origine dell'API: https
// assoluto, con un host e senza credenziali.
func checkExternal(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("indirizzo %s non valido: serve https con un host e senza credenziali", redactURL(raw))
	}
	return nil
}

// redactURL lascia di un URL solo schema e host: gli URL firmati portano token
// nel percorso o nella query.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return "<url>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// hideURL riduce a schema e host l'URL degli errori di net/http nella catena
// di err (sul posto: tipo e classificazione dell'errore restano).
func hideURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = redactURL(ue.URL)
	}
	return err
}

type hideKey struct{}

// hideInMessages: con il contesto restituito i valori indicati (un file_id
// caricato, un indirizzo della clip sorgente) non compaiono nei messaggi
// d'errore di apiCall e jobErrorText.
func hideInMessages(ctx context.Context, vals ...string) context.Context {
	return context.WithValue(ctx, hideKey{}, append(slices.Clip(hiddenIn(ctx)), vals...))
}

func hiddenIn(ctx context.Context) []string {
	v, _ := ctx.Value(hideKey{}).([]string)
	return v
}

var (
	dataURIText = regexp.MustCompile(`data:[^\s"'<>]*`)
	urlText     = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)
)

// redactText ripulisce un testo del provider prima di mostrarlo: i valori
// nascosti tolti, i data URI (la clip inviata) e gli URL (che possono essere
// firmati) ridotti a schema e host, i caratteri di controllo tolti, al massimo
// 500 caratteri.
func redactText(s string, hide ...string) string {
	s = strings.ReplaceAll(s, `\/`, "/")
	for _, h := range hide {
		if h != "" {
			s = strings.ReplaceAll(s, h, "<…>")
		}
	}
	s = dataURIText.ReplaceAllString(s, "data:…")
	s = urlText.ReplaceAllStringFunc(s, redactURL)
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 500 {
		s = string(r[:500]) + "…"
	}
	return strings.TrimSpace(s)
}

// providerMessage è il solo messaggio d'errore di una risposta dell'API
// (error.code ed error.message, oppure message), mai il corpo: vuoto se non c'è.
func providerMessage(data []byte) string {
	var e struct {
		Error   json.RawMessage `json:"error"`
		Message any             `json:"message"`
	}
	if json.Unmarshal(data, &e) != nil {
		return ""
	}
	if msg := errorDetail(e.Error); msg != "" {
		return msg
	}
	s, _ := e.Message.(string)
	return s
}

// jobErrorText è il motivo di un job finito male, dal campo error dello
// stato: solo codice e messaggio, ripuliti.
func jobErrorText(ctx context.Context, raw json.RawMessage) string {
	return messageOr(redactText(errorDetail(raw), hiddenIn(ctx)...), "nessun dettaglio dal provider")
}

// errorDetail legge un campo error: una stringa, oppure un oggetto con code e
// message (gli altri campi, che possono riportare l'input, si scartano).
func errorDetail(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var e struct {
		Code    any `json:"code"`
		Message any `json:"message"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	msg, _ := e.Message.(string)
	switch code := e.Code.(type) {
	case string:
		if code != "" {
			msg = code + ": " + msg
		}
	case float64:
		msg = fmt.Sprintf("%g: %s", code, msg)
	}
	return strings.TrimSpace(msg)
}

func messageOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// printableID dice se l'id di un job è usabile in registro e messaggi: da 1 a
// 256 caratteri ASCII stampabili, senza spazi (come per giv job fetch).
func printableID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r >= 0x7f {
			return false
		}
	}
	return true
}
