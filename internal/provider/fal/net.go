package fal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// externalClient è il client del PUT firmato e degli scaricamenti: nessuna
// chiave, connessione diretta (niente proxy) solo verso indirizzi pubblici,
// controllati sull'indirizzo effettivo di ogni connessione. Redirect solo per
// le GET, al massimo 5, ognuno rivalidato come gli indirizzi di fal e senza
// Referer (porterebbe percorso e query firmati della richiesta precedente);
// il PUT non segue redirect: un 301/302/303 lo trasformerebbe in una GET e un
// 200 finale passerebbe per un caricamento riuscito.
func externalClient(rt http.RoundTripper) *http.Client {
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
			if via[0].Method != http.MethodGet {
				return redirectError{fmt.Errorf("redirect rifiutato per %s: il caricamento va solo all'indirizzo firmato", via[0].Method)}
			}
			if len(via) >= 5 {
				return redirectError{errors.New("troppi redirect")}
			}
			if err := checkExternal(req.URL.String()); err != nil {
				return redirectError{fmt.Errorf("redirect: %w", err)}
			}
			return nil
		},
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

// checkExternal controlla un indirizzo restituito da fal (upload_url,
// file_url, video.url): https, con un host e senza credenziali.
func checkExternal(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("indirizzo %s non valido: serve https senza credenziali", redact(raw))
	}
	return nil
}

// falStorageDomains sono i domini dello storage di fal (file_url documentati
// su v3b.fal.media). L'host esatto dell'upload_url firmato non è documentato:
// si conferma nella prova a pagamento.
var falStorageDomains = []string{"fal.media", "fal.ai"}

// falStorage controlla l'upload_url restituito dall'initiate: oltre a
// checkExternal, solo un host dello storage di fal (o un suo sottodominio),
// perché riceve i byte dei file locali.
func falStorage(raw string) error {
	if err := checkExternal(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	h := strings.ToLower(u.Hostname())
	for _, d := range falStorageDomains {
		if h == d || strings.HasSuffix(h, "."+d) {
			return nil
		}
	}
	return fmt.Errorf("indirizzo %s fuori dallo storage di fal: file non inviato", redact(raw))
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

// redirectError è un rifiuto di CheckRedirect: testo nostro, con gli
// indirizzi già ridotti.
type redirectError struct{ error }

func (e redirectError) Unwrap() error { return e.error }

// netError toglie da un errore di net/http l'URL completo (che può essere
// firmato). La causa resta solo se è di un tipo che non riporta testi della
// risposta (net/http mette nella causa un Location non leggibile o una riga di
// risposta malformata); altrimenti un testo nostro, con la catena per
// errors.Is ed errors.As.
func netError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	e := ue.Err
	var re redirectError
	var oe *net.OpError
	var de *net.DNSError
	var ne net.Error
	switch {
	case errors.As(e, &re), errors.As(e, &oe), errors.As(e, &de),
		errors.Is(e, context.Canceled), errors.Is(e, context.DeadlineExceeded),
		errors.Is(e, io.EOF), errors.Is(e, io.ErrUnexpectedEOF):
		return e
	case errors.As(e, &ne) && ne.Timeout():
		return opaqueError{e, "tempo scaduto"}
	}
	return opaqueError{e, fmt.Sprintf("risposta di rete non valida (%T, dettaglio omesso: può contenere indirizzi firmati)", e)}
}

// opaqueError mostra solo msg e tiene la causa per errors.Is ed errors.As.
type opaqueError struct {
	err error
	msg string
}

func (e opaqueError) Error() string { return e.msg }
func (e opaqueError) Unwrap() error { return e.err }
