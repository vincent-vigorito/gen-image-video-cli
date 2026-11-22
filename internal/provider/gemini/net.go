package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// Rete delle richieste video (come per fal): la chiave solo all'origine
// dell'API, nessun redirect sui POST, scaricamenti verso altri host solo https
// e verso indirizzi pubblici, testi d'errore senza corpo grezzo né URL completi.

// stayKey segna le GET che non seguono redirect fuori dall'origine dell'API:
// le attese, la cui risposta deve venire dal provider.
type stayKey struct{}

func stay(ctx context.Context) context.Context { return context.WithValue(ctx, stayKey{}, true) }

// checkRedirect vale per tutte le richieste del client (anche le immagini):
// una richiesta diversa da GET non segue redirect, nemmeno sull'origine
// dell'API (un 307/308 reinvierebbe il corpo: per un avvio a pagamento il 3xx
// è un esito incerto); le GET li seguono sull'origine con la chiave, fuori
// solo verso https, senza chiave, senza credenziali nell'URL e mai per le attese.
// Il Referer si toglie sempre (porterebbe percorso e query della richiesta prima).
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if via[0].Method != http.MethodGet {
		return http.ErrUseLastResponse // il 3xx diventa un errore HTTP
	}
	if len(via) >= 10 {
		return errors.New("gemini: troppi redirect")
	}
	req.Header.Del("Referer")
	if c.apiOrigin(req.URL) {
		return nil
	}
	req.Header.Del("x-goog-api-key")
	if req.Context().Value(stayKey{}) != nil {
		return http.ErrUseLastResponse
	}
	return checkExternal(req.URL)
}

// downloadHTTP è il client degli scaricamenti dei video: redirect come in
// checkRedirect; le connessioni verso host diversi da quello dell'API solo a
// indirizzi pubblici (controllati sull'indirizzo effettivo) e senza proxy. Un
// Transport nel client HTTP (i test) sostituisce tutte le connessioni, filtro
// compreso.
func (c *Client) downloadHTTP() *http.Client {
	var rt http.RoundTripper
	if c.HTTP != nil {
		rt = c.HTTP.Transport
	}
	if rt == nil {
		rt = c.publicTransport()
	}
	return &http.Client{Transport: rt, CheckRedirect: c.checkRedirect}
}

func (c *Client) publicTransport() *http.Transport {
	api := ""
	if b, err := url.Parse(c.base()); err == nil && b.Hostname() != "" {
		api = net.JoinHostPort(b.Hostname(), port(b))
	}
	plain := &net.Dialer{Timeout: 30 * time.Second}
	public := &net.Dialer{Timeout: 30 * time.Second, Control: publicOnly}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if api != "" && strings.EqualFold(addr, api) {
				return plain.DialContext(ctx, network, addr)
			}
			return public.DialContext(ctx, network, addr)
		},
		TLSHandshakeTimeout:    30 * time.Second,
		ResponseHeaderTimeout:  60 * time.Second,
		MaxResponseHeaderBytes: 1 << 20,
	}
}

// reti non pubbliche oltre a quelle riconosciute da netip (privati, loopback,
// link-local, multicast): come per gli ingressi scaricati da giv
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

func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// publicOnly è il controllo del dialer: vale per l'indirizzo già risolto.
func publicOnly(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if !publicAddr(ip) {
		return fmt.Errorf("indirizzo %s non pubblico: connessione rifiutata", ip)
	}
	return nil
}

// checkExternal controlla la destinazione di un redirect fuori dall'API: https,
// con un host e senza credenziali.
func checkExternal(u *url.URL) error {
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("gemini: redirect verso %s rifiutato: serve https senza credenziali", redact(u.String()))
	}
	return nil
}

// redact lascia di un URL solo schema e host: gli URL firmati portano token
// nel percorso o nella query.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<url>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// netError riduce l'URL di un errore di net/http a schema e host (resta un
// *url.Error, quindi un errore di rete per i ritentativi delle attese).
func netError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return &url.Error{Op: ue.Op, URL: redact(ue.URL), Err: ue.Err}
	}
	return err
}

// videoAPIError è il messaggio di un errore HTTP delle richieste video: solo
// status e message dell'oggetto error, ripuliti; mai il corpo grezzo.
func videoAPIError(data []byte) string {
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil {
		if s := errorText(e.Error); s != "" {
			return s
		}
	}
	return "risposta di errore senza messaggio leggibile"
}

// errorText è il testo di un errore di Gemini dai soli campi ammessi: una
// stringa o un {code, status, message}; per ogni altra forma "" (assente) o un
// testo generico, mai il JSON grezzo.
func errorText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return safeText(s, 300)
	}
	var o struct {
		Code    json.Number `json:"code"`
		Status  string      `json:"status"`
		Message string      `json:"message"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return "errore in una forma non riconosciuta"
	}
	var parts []string
	for _, p := range []string{safeText(o.Status, 60), safeText(o.Code.String(), 20), safeText(o.Message, 300)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return "errore senza messaggio"
	}
	return strings.Join(parts, ": ")
}

// urlRe trova gli indirizzi nei testi di Gemini, ridotti da safeText a schema e host.
var urlRe = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"'<>]+`)

// safeText prepara un testo del provider per stderr e per lo stato: URL ridotti
// a schema e host, caratteri di controllo tolti, al massimo n byte.
func safeText(s string, n int) string {
	s = urlRe.ReplaceAllStringFunc(s, redact)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)
	return clip(strings.Join(strings.Fields(s), " "), n)
}

// clip taglia s a n byte senza spezzare un carattere.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
