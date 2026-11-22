package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"gen-image-video-cli/internal/provider"
)

// inputHTTP scarica gli input da URL: connessione diretta (niente proxy) e solo
// verso indirizzi pubblici, controllati sull'indirizzo effettivo di ogni
// connessione, redirect compresi.
var inputHTTP = &http.Client{
	Timeout: 120 * time.Second,
	Transport: &http.Transport{
		DialContext:            (&net.Dialer{Timeout: 30 * time.Second, Control: publicOnly}).DialContext,
		TLSHandshakeTimeout:    30 * time.Second,
		ResponseHeaderTimeout:  60 * time.Second,
		MaxResponseHeaderBytes: 1 << 20,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errRedirects
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return errRedirectScheme
		}
		return nil
	},
}

var (
	errRedirects      = errors.New("troppi redirect")
	errRedirectScheme = errors.New("redirect verso uno schema diverso da http(s)")
	errTooBig         = errors.New("oltre il limite")
)

// reti non pubbliche oltre a quelle riconosciute da netip (privati, loopback,
// link-local, multicast)
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

type notPublicError struct{ addr netip.Addr }

func (e *notPublicError) Error() string {
	return fmt.Sprintf("indirizzo %s non pubblico: connessione rifiutata", e.addr)
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
		return &notPublicError{ip}
	}
	return nil
}

func isURL(p string) bool {
	return strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://")
}

// redactURL lascia negli errori solo schema e host: percorso e query degli URL
// firmati possono contenere token.
func redactURL(p string) string {
	u, err := url.Parse(p)
	if err != nil || u.Host == "" {
		return "<url>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

// shownInput è l'input come compare nei messaggi.
func shownInput(p string) string {
	if isURL(p) {
		return redactURL(p)
	}
	return p
}

// tempInputs sono le copie degli input scaricati: cleanup le toglie a fine
// comando, anche in caso di errore.
type tempInputs []string

func (t *tempInputs) cleanup() {
	for _, f := range *t {
		os.Remove(f)
	}
	*t = nil
}

// downloadError riduce un errore di download a un messaggio a contenuto
// controllato: gli errori di net/http possono riportare l'URL o il Location
// interi.
func downloadError(shown string, err error) error {
	var np *notPublicError
	if errors.As(err, &np) {
		return usageError("download input %s: %v", shown, np)
	}
	msg := "connessione non riuscita"
	var dns *net.DNSError
	var ne net.Error
	switch {
	case errors.Is(err, errRedirects):
		msg = errRedirects.Error()
	case errors.Is(err, errRedirectScheme):
		msg = errRedirectScheme.Error()
	case errors.As(err, &dns):
		msg = "host non risolto"
	case errors.As(err, &ne) && ne.Timeout():
		msg = "timeout"
	}
	return fmt.Errorf("download input %s: %s", shown, msg)
}

// resolve accetta un path locale o un URL http(s); gli URL vengono scaricati in
// un file temporaneo privato (0600, nome casuale), entro MaxInputBytes.
func (t *tempInputs) resolve(p string) (string, error) {
	if !isURL(p) {
		return p, nil
	}
	local, _, err := t.download(p, provider.MaxInputBytes)
	if errors.Is(err, errTooBig) {
		return "", usageError("download input %s: oltre il limite di %d MiB", redactURL(p), provider.MaxInputBytes>>20)
	}
	return local, err
}

// download scarica l'URL p in un file temporaneo privato (0600, nome casuale)
// leggendo al massimo limit byte (errTooBig oltre); restituisce file e byte.
func (t *tempInputs) download(p string, limit int64) (string, int64, error) {
	shown := redactURL(p)
	resp, err := inputHTTP.Get(p)
	if err != nil {
		return "", 0, downloadError(shown, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("download input %s: HTTP %d", shown, resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return "", 0, errTooBig
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", 0, downloadError(shown, err)
	}
	if int64(len(data)) > limit {
		return "", 0, errTooBig
	}
	ext := extForMime(http.DetectContentType(data))
	if ext == ".bin" {
		ext = extForMime(resp.Header.Get("Content-Type"))
	}
	f, err := os.CreateTemp("", "giv-input-*"+ext)
	if err != nil {
		return "", 0, err
	}
	*t = append(*t, f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", 0, err
	}
	if err := f.Close(); err != nil {
		return "", 0, err
	}
	fmt.Fprintf(os.Stderr, "input scaricato: %s (%d byte)\n", f.Name(), len(data))
	return f.Name(), int64(len(data)), nil
}

func extForMime(ct string) string {
	switch {
	case strings.Contains(ct, "png"):
		return ".png"
	case strings.Contains(ct, "jpeg"):
		return ".jpg"
	case strings.Contains(ct, "webp"):
		return ".webp"
	case strings.Contains(ct, "gif"):
		return ".gif"
	default:
		return ".bin"
	}
}

// videoInput è un'immagine in ingresso a un video col flag da cui arriva.
type videoInput struct {
	flag string
	path *string
}

func videoInputs(req *provider.VideoRequest) []videoInput {
	list := []videoInput{{"--image", &req.Image}, {"--last-frame", &req.LastFrame}}
	for i := range req.Refs {
		list = append(list, videoInput{"--ref", &req.Refs[i]})
	}
	for i := range req.Keyframes {
		list = append(list, videoInput{"--keyframe", &req.Keyframes[i]})
	}
	return list
}

// checkVideoInputs controlla le immagini di un video (codice 2): numero di
// --ref, tipo riconosciuto dai byte, dimensione per file e totale. Gli URL si
// controllano dopo il download (resolveVideo).
func checkVideoInputs(req *provider.VideoRequest) error {
	_, err := localInputsSize(req)
	return err
}

// localInputsSize controlla le immagini locali di un video e ne restituisce la
// dimensione totale.
func localInputsSize(req *provider.VideoRequest) (int64, error) {
	if len(req.Refs) > provider.MaxRefs {
		return 0, usageError("--ref: al massimo %d immagini di riferimento, non %d", provider.MaxRefs, len(req.Refs))
	}
	if len(req.Keyframes) > provider.MaxKeyframes {
		return 0, usageError("--keyframe: al massimo %d fotogrammi intermedi, non %d", provider.MaxKeyframes, len(req.Keyframes))
	}
	var total int64
	for _, in := range videoInputs(req) {
		if *in.path == "" || isURL(*in.path) {
			continue
		}
		n, err := provider.CheckImage(*in.path)
		if err != nil {
			return 0, usageError("%s %s: %v", in.flag, *in.path, err)
		}
		total += n
	}
	if total > provider.MaxInputTotal {
		return 0, totalTooBig()
	}
	return total, nil
}

func totalTooBig() error {
	return usageError("immagini in ingresso oltre il limite di %d MiB in tutto", provider.MaxInputTotal>>20)
}

// resolveVideo scarica gli input da URL del video e li ricontrolla: ogni
// download si ferma al minimo fra il limite per immagine e i byte che restano
// del limite totale, file locali compresi.
func (t *tempInputs) resolveVideo(req *provider.VideoRequest) error {
	used, err := localInputsSize(req)
	if err != nil {
		return err
	}
	for _, in := range videoInputs(req) {
		if *in.path == "" || !isURL(*in.path) {
			continue
		}
		limit := min(int64(provider.MaxInputBytes), int64(provider.MaxInputTotal)-used)
		if limit <= 0 {
			return totalTooBig()
		}
		local, n, err := t.download(*in.path, limit)
		if errors.Is(err, errTooBig) {
			if limit < provider.MaxInputBytes {
				return totalTooBig()
			}
			return usageError("%s %s: oltre il limite di %d MiB per immagine", in.flag, redactURL(*in.path), provider.MaxInputBytes>>20)
		}
		if err != nil {
			return err
		}
		if _, err := provider.CheckImage(local); err != nil {
			return usageError("%s %s: %v", in.flag, redactURL(*in.path), err)
		}
		used += n
		*in.path = local
	}
	return checkVideoInputs(req)
}
