package fal

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// externalClient è il client del PUT firmato e degli scaricamenti: nessuna
// chiave, connessione diretta (niente proxy) solo verso indirizzi pubblici,
// controllati sull'indirizzo effettivo di ogni connessione, e redirect solo
// verso https.
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
			if len(via) >= 5 {
				return errors.New("troppi redirect")
			}
			if req.URL.Scheme != "https" {
				return errors.New("redirect verso uno schema diverso da https")
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

// redact lascia di un URL solo schema e host: gli URL firmati portano token
// nel percorso o nella query.
func redact(raw string) string {
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
