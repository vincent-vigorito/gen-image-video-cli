package openaicompat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"syscall"
	"time"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
)

// Operazioni sulle clip esistenti via OpenRouter (giv upscale): POST /videos
// con la clip sorgente in input_references, attesa e scaricamento. Un URL https
// va al provider così com'è; un file locale va nel corpo come data URI, che
// OpenRouter non documenta: se lo rifiuta, il messaggio chiede un URL https.
//
// Rispetto a giv video le richieste sono più strette: nessun redirect sulle
// chiamate all'API (l'avvio rediretto è un esito incerto, mai ripetuto),
// risposte entro un tetto, messaggi d'errore senza data URI né URL completi, un
// solo video scaricato; fuori dall'origine dell'API solo https senza
// credenziali né Referer, connessioni dirette solo verso indirizzi pubblici.

const (
	clipMaxJSON  = 4 << 20 // risposta JSON di avvio e attesa
	clipMaxMedia = 1 << 30 // video scaricato
)

// clipTransport sostituisce le connessioni degli scaricamenti fuori
// dall'origine dell'API (test); nil = dirette, senza proxy, solo verso
// indirizzi pubblici.
var clipTransport http.RoundTripper

// dataURIHint accompagna un rifiuto quando la clip è andata come data URI.
const dataURIHint = "la clip locale è andata a openrouter come data URI, che OpenRouter non documenta: " +
	"se il rifiuto dipende da questo, pubblica il video e passa --video con un URL https"

// creativity di flux-video-upscale per --mode
var routerCreativity = map[string]int{"precise": 0, "creative": 1}

// GenerateClip avvia un'operazione su una clip (avvio mai ripetuto), la
// segnala al registro dei job, la attende e ne scarica il risultato.
func (c *Client) GenerateClip(ctx context.Context, req provider.ClipRequest) (*provider.Result, error) {
	switch {
	case c.opts.Router:
		return c.routerClip(ctx, req)
	}
	return nil, fmt.Errorf("giv %s non disponibile con %s", req.Op, c.name)
}

// routerClip fa l'upscale con black-forest-labs/flux-video-upscale: fattore e
// creativity sempre nel corpo (il default è quello della tabella, su cui è
// fatta la stima), prompt solo se indicato.
func (c *Client) routerClip(ctx context.Context, req provider.ClipRequest) (*provider.Result, error) {
	op := caps.FindOp(caps.OpenRouterOps(), req.Op, req.Model)
	if req.Op != "upscale" || op == nil {
		return nil, fmt.Errorf("%s: giv %s con %s non disponibile", c.name, req.Op, req.Model)
	}
	creativity, ok := routerCreativity[op.Mode(req)]
	if !ok {
		return nil, fmt.Errorf("%s: --mode %s non ammesso da %s", c.name, op.Mode(req), req.Model)
	}
	src, local, err := c.routerVideoURL(req.Video)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model":            req.Model,
		"input_references": []map[string]any{{"type": "video_url", "video_url": map[string]string{"url": src}}},
		"upscale_factor":   op.Factor(req),
		"creativity":       creativity,
	}
	if req.Prompt != "" {
		body["prompt"] = req.Prompt
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ID         string `json:"id"`
		PollingURL string `json:"polling_url"`
	}
	// l'avvio non si ripete né segue redirect: con un 3xx, un 5xx o un timeout potrebbe essere partito
	if err := c.clipCall(ctx, http.MethodPost, c.baseURL+"/videos", payload, &resp); err != nil {
		err = httpx.Start(err)
		if local && bodyRejected(err) {
			err = fmt.Errorf("%w; %s", err, dataURIHint)
		}
		return nil, err
	}
	// un job senza id valido o senza un indirizzo di attesa recuperabile non si
	// registra né si attende: esito incerto, nessun nuovo avvio
	job := provider.Job{ID: resp.ID, PollingURL: resp.PollingURL}
	if !printableID(job.ID) {
		return nil, httpx.Start(fmt.Errorf("%s: id del job non valido nella risposta", c.name))
	}
	if err := c.CheckJob(job); err != nil {
		return nil, httpx.Start(err)
	}
	fmt.Fprintf(os.Stderr, "job avviato: %s (polling ogni %s)\n", job.ID, pollInterval)
	provider.ClipJobStarted(c.name, job, req)
	res, err := c.clipWait(ctx, job, req.Timeout)
	if err != nil && local && errors.Is(err, provider.ErrJobFailed) {
		err = fmt.Errorf("%w; %s", err, dataURIHint)
	}
	return res, err
}

// routerVideoURL è la clip sorgente nella forma di input_references: un URL
// https assoluto senza credenziali così com'è, un file locale come data URI
// (regole di provider.OpenVideo, letti al massimo i byte controllati). local
// dice se è un file locale.
func (c *Client) routerVideoURL(p string) (src string, local bool, err error) {
	if strings.HasPrefix(p, "https://") || strings.HasPrefix(p, "http://") || p == "" {
		if err := checkExternal(p); err != nil {
			return "", false, fmt.Errorf("%s: --video: %w", c.name, err)
		}
		return p, false, nil
	}
	f, mime, size, err := provider.OpenVideo(p)
	if err != nil {
		return "", true, fmt.Errorf("--video %s: %w", p, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, size))
	if err != nil {
		return "", true, fmt.Errorf("--video %s: %w", p, err)
	}
	fmt.Fprintf(os.Stderr, "avviso: clip locale inviata a %s nel corpo della richiesta (data URI, %.1f MiB)\n",
		c.name, float64(len(data))/(1<<20))
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), true, nil
}

// clipCall è una chiamata all'API (avvio o attesa) senza redirect né
// ripetizioni: risposta entro clipMaxJSON, messaggio d'errore ripulito.
func (c *Client) clipCall(ctx context.Context, method, rawURL string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.noRedirect().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := readAtMost(resp.Body, clipMaxJSON, "risposta")
	if err != nil {
		return fmt.Errorf("%s: %w", c.name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &httpx.Error{Prefix: c.name, Status: resp.StatusCode, Msg: redactText(apiErrorMessage(data)),
			RetryAfter: httpx.ParseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

// noRedirect è il client dell'API che non segue nessun redirect: il 3xx torna
// come risposta (e quindi come errore HTTP).
func (c *Client) noRedirect() *http.Client {
	cl := *c.http
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cl
}

// clipWait attende un job di clip già partito fino al tetto e ne scarica il
// risultato; ogni errore porta l'id del job.
func (c *Client) clipWait(ctx context.Context, job provider.Job, timeout time.Duration) (res *provider.Result, err error) {
	defer func() { err = httpx.Job(job.ID, err) }()
	timeout = jobTimeout(timeout)
	// il tetto copre attesa, controlli, ritentativi e scaricamento
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if err != nil && ctx.Err() == nil && errors.Is(tctx.Err(), context.DeadlineExceeded) {
			res, err = nil, fmt.Errorf("%s: timeout dopo %s", c.name, timeout)
		}
	}()
	for {
		select {
		case <-tctx.Done():
			return nil, tctx.Err()
		case <-time.After(pollInterval):
		}
		if res, err = c.clipPoll(tctx, job); err != nil || res != nil {
			return res, err
		}
		fmt.Fprint(os.Stderr, ".")
	}
}

// clipPoll controlla una volta lo stato di un job di clip: risultato
// scaricato, errore, oppure nil e nil se il job è ancora in corso.
func (c *Client) clipPoll(ctx context.Context, job provider.Job) (*provider.Result, error) {
	var st struct {
		Status       string          `json:"status"`
		UnsignedURLs []string        `json:"unsigned_urls"`
		Error        json.RawMessage `json:"error"`
		Usage        struct {
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	err := httpx.RetryPoll(ctx, func() error { return c.clipCall(ctx, http.MethodGet, job.PollingURL, nil, &st) })
	if err != nil {
		return nil, err
	}
	switch st.Status {
	case "completed":
		fmt.Fprintln(os.Stderr)
		if len(st.UnsignedURLs) == 0 {
			return nil, fmt.Errorf("%s: job completato ma nessun video nella risposta", c.name)
		}
		// un'operazione dà un solo video: gli altri indirizzi non si scaricano
		data, err := c.clipDownload(ctx, st.UnsignedURLs[0])
		if err != nil {
			return nil, err
		}
		return &provider.Result{Media: []provider.Media{{Mime: "video/mp4", Data: data}}, CostUSD: st.Usage.Cost, JobID: job.ID}, nil
	case "failed", "cancelled", "error", "expired":
		fmt.Fprintln(os.Stderr)
		return nil, provider.JobFailed(fmt.Errorf("%s: job %s: %s", c.name, st.Status, redactText(string(st.Error))))
	}
	return nil, nil
}

// clipDownload scarica il video di un job di clip. Sull'origine dell'API con
// la chiave e senza seguire redirect: un redirect si segue col client esterno,
// senza chiave. Fuori origine solo col client esterno.
func (c *Client) clipDownload(ctx context.Context, raw string) ([]byte, error) {
	if !c.apiHost(raw) {
		return c.clipExternalGet(ctx, raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.noRedirect().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode >= 300 && resp.StatusCode < 400 && loc != "" {
		u, err := resp.Request.URL.Parse(loc)
		if err != nil {
			return nil, fmt.Errorf("%s: redirect del video non valido", c.name)
		}
		return c.clipExternalGet(ctx, u.String())
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: download HTTP %d", c.name, resp.StatusCode)
	}
	return readAtMost(resp.Body, clipMaxMedia, "video")
}

// clipExternalGet scarica fuori dall'origine dell'API: https senza
// credenziali, nessuna chiave, connessione diretta solo verso indirizzi
// pubblici (controllati sull'indirizzo effettivo), redirect rivalidati e senza
// Referer.
func (c *Client) clipExternalGet(ctx context.Context, raw string) ([]byte, error) {
	if err := checkExternal(raw); err != nil {
		return nil, fmt.Errorf("%s: video del job: %w", c.name, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: video del job: indirizzo non valido", c.name)
	}
	resp, err := clipExternalClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: download: %w", c.name, netError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: download HTTP %d", c.name, resp.StatusCode)
	}
	return readAtMost(resp.Body, clipMaxMedia, "video")
}

func clipExternalClient() *http.Client {
	rt := clipTransport
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
		return nil, fmt.Errorf("%s oltre %d MiB: rifiutata", what, limit>>20)
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
	if err != nil || u.Host == "" {
		return "<url>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// netError toglie da un errore di net/http l'URL completo (che può essere
// firmato), lasciando la causa.
func netError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

var (
	dataURIText = regexp.MustCompile(`data:[^\s"'<>]*`)
	urlText     = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)
)

// redactText ripulisce un messaggio del provider prima di mostrarlo: i data
// URI (la clip inviata) e gli URL (che possono essere firmati) ridotti a schema
// e host, al massimo 500 caratteri.
func redactText(s string) string {
	s = strings.ReplaceAll(s, `\/`, "/")
	s = dataURIText.ReplaceAllString(s, "data:…")
	s = urlText.ReplaceAllStringFunc(s, redactURL)
	if r := []rune(s); len(r) > 500 {
		s = string(r[:500]) + "…"
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

// bodyRejected dice se l'avvio è stato rifiutato per il contenuto della
// richiesta (corpo non valido, troppo grande o tipo non accettato).
func bodyRejected(err error) bool {
	var he *httpx.Error
	if !errors.As(err, &he) {
		return false
	}
	switch he.Status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return true
	}
	return false
}
