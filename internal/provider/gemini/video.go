package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/provider"
)

// Video con Gemini diretto: API interactions con gemini-omni (giv video, giv
// extend e giv edit con --from). La richiesta di generazione è sincrona e a
// pagamento: mai ripetuta. La risposta può essere già completa o un job in
// corso da attendere; il video arriva in base64 oppure come file da scaricare
// dall'API dei file.

// pollInterval è l'attesa fra due controlli di un job o di un file; i test del
// package la accorciano.
var pollInterval = 10 * time.Second

// maxVideoBytes è il tetto delle risposte (video in base64) e degli scaricamenti.
const maxVideoBytes = 512 << 20

// apiRevision fissa lo schema delle interactions con steps: compare negli
// esempi ufficiali di attesa e file, non in quelli di Omni (non verificato se
// serva).
const apiRevision = "2026-05-20"

func (c *Client) base() string {
	if c.Base != "" {
		return strings.TrimSuffix(c.Base, "/")
	}
	return baseURL
}

// stayKey segna le richieste che non seguono redirect fuori dall'origine
// dell'API: l'avvio a pagamento e le attese, la cui risposta deve venire dal provider.
type stayKey struct{}

func stay(ctx context.Context) context.Context { return context.WithValue(ctx, stayKey{}, true) }

// checkRedirect: sull'origine dell'API i salti si seguono con la chiave; fuori
// origine la chiave si toglie, si seguono solo verso https e mai per avvio e attese.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("gemini: troppi redirect")
	}
	if c.apiOrigin(req.URL) {
		return nil
	}
	req.Header.Del("x-goog-api-key")
	if req.Context().Value(stayKey{}) != nil {
		return http.ErrUseLastResponse // il 3xx diventa un errore HTTP
	}
	if req.URL.Scheme != "https" {
		return errors.New("gemini: redirect fuori da https rifiutato")
	}
	return nil
}

// apiOrigin dice se u ha schema, host e porta dell'API e nessuna credenziale:
// solo lì va la chiave.
func (c *Client) apiOrigin(u *url.URL) bool {
	b, err := url.Parse(c.base())
	if err != nil || u == nil || u.User != nil || u.Host == "" {
		return false
	}
	return u.Scheme == b.Scheme && strings.EqualFold(u.Hostname(), b.Hostname()) && port(u) == port(b)
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

// videoHTTP è il client delle richieste video: senza tetto proprio (lo dà il
// contesto, cioè --timeout) e coi redirect di checkRedirect.
func (c *Client) videoHTTP() *http.Client {
	hc := &http.Client{CheckRedirect: c.checkRedirect}
	if c.HTTP != nil {
		hc.Transport, hc.Jar = c.HTTP.Transport, c.HTTP.Jar
	}
	return hc
}

// videoJSON è doJSONWith per le richieste video: client senza tetto, schema
// fissato da Api-Revision, risposta entro maxVideoBytes.
func (c *Client) videoJSON(ctx context.Context, retry httpx.RetryFunc, method, url string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	return retry(ctx, func() error {
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rdr)
		if err != nil {
			return err
		}
		req.Header.Set("x-goog-api-key", c.APIKey)
		req.Header.Set("Api-Revision", apiRevision)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.videoHTTP().Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		data, err := readLimited(resp.Body)
		if err != nil {
			return err
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return &httpx.Error{Prefix: "gemini", Status: resp.StatusCode, Msg: apiErrorMessage(data),
				RetryAfter: httpx.ParseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		if out != nil {
			return json.Unmarshal(data, out)
		}
		return nil
	})
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxVideoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxVideoBytes {
		return nil, fmt.Errorf("gemini: risposta oltre %d MiB", maxVideoBytes>>20)
	}
	return data, nil
}

// ValidID dice se id può essere l'id di un'interaction (job) di Gemini: da 1 a
// 256 caratteri fra lettere, cifre e _ - . = ; finisce in un percorso, quindi
// niente / e niente segmenti . o ..
func ValidID(id string) bool {
	if id == "" || len(id) > 256 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.', r == '=':
		default:
			return false
		}
	}
	return true
}

func (c *Client) pollURL(id string) string { return c.base() + "/interactions/" + id }

// VideoCaps è la tabella fissa delle capacità video (nessuna chiamata).
func (c *Client) VideoCaps(ctx context.Context) ([]caps.Model, error) { return caps.Gemini(), nil }

// GenerateVideo genera un video con l'API interactions.
func (c *Client) GenerateVideo(ctx context.Context, req provider.VideoRequest) (*provider.Result, error) {
	body, err := videoBody(req)
	if err != nil {
		return nil, err
	}
	return c.run(ctx, body, req.Timeout, func(job provider.Job) {
		if provider.OnJobStart != nil {
			provider.OnJobStart(c.Name(), job, req)
		}
	})
}

// videoBody è il corpo di giv video: immagini in base64 nell'ordine dei ruoli
// (primo e ultimo fotogramma, oppure riferimenti) e poi il prompt; il compito
// è sempre indicato, per non affidare i ruoli al solo prompt. store: true
// serve a extend ed edit dopo.
func videoBody(req provider.VideoRequest) (map[string]any, error) {
	if req.Negative != "" {
		fmt.Fprintln(os.Stderr, "avviso: --negative non supportato da gemini, ignorato")
	}
	if req.Seed != 0 {
		fmt.Fprintln(os.Stderr, "avviso: --seed non supportato da gemini, ignorato")
	}
	if len(req.Params) > 0 {
		fmt.Fprintln(os.Stderr, "avviso: --param non supportato da gemini, ignorato")
	}
	task, images := "text_to_video", []string(nil)
	switch {
	case req.Image != "" && len(req.Refs) > 0, req.LastFrame != "" && req.Image == "":
		return nil, errors.New("gemini: combinazione di immagini non supportata")
	case len(req.Refs) > 0:
		task, images = "reference_to_video", req.Refs
	case req.Image != "":
		task, images = "image_to_video", []string{req.Image}
		if req.LastFrame != "" {
			images = append(images, req.LastFrame)
		}
	}
	var input any = req.Prompt
	if len(images) > 0 {
		parts := make([]map[string]any, 0, len(images)+1)
		for _, p := range images {
			mime, data, err := provider.ReadImage(p)
			if err != nil {
				return nil, fmt.Errorf("input %s: %w", p, err)
			}
			parts = append(parts, map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(data), "mime_type": mime})
		}
		input = append(parts, map[string]any{"type": "text", "text": req.Prompt})
	}
	rf := map[string]any{"type": "video", "delivery": "uri"}
	if req.Duration > 0 {
		rf["duration"] = fmt.Sprintf("%ds", req.Duration)
	}
	if req.Resolution != "" {
		rf["resolution"] = strings.ToLower(req.Resolution)
	}
	if req.Aspect != "" {
		rf["aspect_ratio"] = req.Aspect
	}
	return map[string]any{
		"model": req.Model, "input": input, "response_format": rf, "store": true,
		"generation_config": map[string]any{"video_config": map[string]any{"task": task}},
	}, nil
}

// GenerateClip estende o modifica un video generato da Gemini (--from: id
// dell'interaction di partenza). Dall'UE i video caricati non sono ammessi.
func (c *Client) GenerateClip(ctx context.Context, req provider.ClipRequest) (*provider.Result, error) {
	switch {
	case req.Op != "extend" && req.Op != "edit":
		return nil, fmt.Errorf("gemini: giv %s non disponibile", req.Op)
	case req.Video != "":
		return nil, errors.New("gemini: dall'UE si estendono e modificano solo video generati dal modello: usa --from")
	case !ValidID(req.From):
		return nil, fmt.Errorf("gemini: --from %q non è un id di job gemini", req.From)
	}
	rf := map[string]any{"type": "video", "delivery": "uri"}
	if req.Op == "extend" {
		d := req.Duration
		if d <= 0 {
			d = caps.GeminiExtendDuration
		}
		rf["duration"] = fmt.Sprintf("%ds", d)
	}
	body := map[string]any{
		"model": req.Model, "previous_interaction_id": req.From, "input": req.Prompt,
		"response_format": rf, "store": true,
		"generation_config": map[string]any{"video_config": map[string]any{"task": req.Op}},
	}
	return c.run(ctx, body, req.Timeout, func(job provider.Job) { provider.ClipJobStarted(c.Name(), job, req) })
}

// run invia la richiesta (sincrona e a pagamento: mai ripetuta), segnala il
// job appena ha l'id e lo porta a termine: risposta già completa o attesa.
func (c *Client) run(ctx context.Context, body map[string]any, timeout time.Duration, started func(provider.Job)) (*provider.Result, error) {
	timeout = jobTimeout(timeout)
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var it interaction
	if err := c.videoJSON(stay(tctx), httpx.NoRetry, http.MethodPost, c.base()+"/interactions", body, &it); err != nil {
		if ctx.Err() == nil && errors.Is(tctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("gemini: nessuna risposta entro %s (--timeout): %w", timeout, err)
		}
		return nil, httpx.Start(err)
	}
	if !ValidID(it.ID) {
		if it.ID == "" && it.Status == "completed" && it.video() != nil {
			// video già pagato e consegnato, ma senza id: niente recupero
			return c.result(tctx, provider.Job{}, &it)
		}
		return nil, httpx.Start(errors.New("gemini: nessun id valido del job nella risposta"))
	}
	job := provider.Job{ID: it.ID, PollingURL: c.pollURL(it.ID)}
	fmt.Fprintf(os.Stderr, "job avviato: %s\n", job.ID)
	started(job)
	return c.finish(ctx, tctx, timeout, job, &it)
}

// FetchVideo riprende un job già partito senza un nuovo avvio: il GET
// dell'interaction ridà il video in base64.
func (c *Client) FetchVideo(ctx context.Context, job provider.Job, timeout time.Duration) (*provider.Result, error) {
	if err := c.CheckJob(job); err != nil {
		return nil, err
	}
	job.PollingURL = c.pollURL(job.ID)
	timeout = jobTimeout(timeout)
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fmt.Fprintf(os.Stderr, "attesa del job %s (polling ogni %s)\n", job.ID, pollInterval)
	var it interaction
	if err := c.videoJSON(stay(tctx), httpx.RetryPoll, http.MethodGet, job.PollingURL, nil, &it); err != nil {
		return nil, httpx.Job(job.ID, err)
	}
	return c.finish(ctx, tctx, timeout, job, &it)
}

// CheckJob rifiuta, senza richieste, un job che non può essere di Gemini: id
// fuori formato o indirizzo di attesa diverso da quello del job sull'API.
func (c *Client) CheckJob(job provider.Job) error {
	if !ValidID(job.ID) {
		return fmt.Errorf("gemini: id del job non valido: %q", job.ID)
	}
	if job.PollingURL != "" && job.PollingURL != c.pollURL(job.ID) {
		return fmt.Errorf("gemini: l'indirizzo di attesa non è quello del job %s sull'API gemini: rifiutato", job.ID)
	}
	return nil
}

// finish attende il job fino al tetto e ne scarica il risultato; ogni errore
// porta l'id del job.
func (c *Client) finish(ctx, tctx context.Context, timeout time.Duration, job provider.Job, it *interaction) (res *provider.Result, err error) {
	defer func() { err = httpx.Job(job.ID, err) }()
	defer func() {
		if err != nil && ctx.Err() == nil && errors.Is(tctx.Err(), context.DeadlineExceeded) {
			res, err = nil, fmt.Errorf("gemini: timeout dopo %s", timeout)
		}
	}()
	for {
		done, err := it.state()
		if err != nil {
			return nil, err
		}
		if done {
			return c.result(tctx, job, it)
		}
		select {
		case <-tctx.Done():
			return nil, tctx.Err()
		case <-time.After(pollInterval):
		}
		*it = interaction{}
		if err := c.videoJSON(stay(tctx), httpx.RetryPoll, http.MethodGet, job.PollingURL, nil, it); err != nil {
			return nil, err
		}
		fmt.Fprint(os.Stderr, ".")
	}
}

type interaction struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Steps  []struct {
		Type    string    `json:"type"`
		Content []content `json:"content"`
	} `json:"steps"`
	Outputs []content       `json:"outputs"` // schema senza steps (non verificato)
	Error   json.RawMessage `json:"error"`
	Usage   *usage          `json:"usage"`
}

type content struct {
	Type     string `json:"type"`
	MimeType string `json:"mime_type"`
	Data     string `json:"data"`
	URI      string `json:"uri"`
}

type usage struct {
	TotalInput       int64 `json:"total_input_tokens"`
	TotalOutput      int64 `json:"total_output_tokens"`
	TotalThought     int64 `json:"total_thought_tokens"`
	OutputByModality []struct {
		Modality string `json:"modality"`
		Tokens   int64  `json:"tokens"`
	} `json:"output_tokens_by_modality"`
}

// video è l'ultimo video prodotto dal modello (non negli ingressi dell'utente).
func (it *interaction) video() *content {
	var v *content
	for i := range it.Steps {
		if it.Steps[i].Type == "user_input" {
			continue
		}
		for j := range it.Steps[i].Content {
			if it.Steps[i].Content[j].Type == "video" {
				v = &it.Steps[i].Content[j]
			}
		}
	}
	if v != nil {
		return v
	}
	for j := range it.Outputs {
		if it.Outputs[j].Type == "video" {
			v = &it.Outputs[j]
		}
	}
	return v
}

// state: true = finito col video; false e nil = ancora in corso; errore =
// finito male (ErrJobFailed) o stato non previsto (job recuperabile).
func (it *interaction) state() (bool, error) {
	failed := func() error {
		msg := strings.TrimSpace(string(it.Error))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return provider.JobFailed(fmt.Errorf("gemini: job %s: %s", it.Status, msg))
	}
	switch it.Status {
	case "completed":
		return true, nil
	case "in_progress":
		return false, nil
	case "":
		return it.video() != nil, nil
	case "incomplete":
		if it.video() != nil {
			return true, nil
		}
		return false, failed()
	case "failed", "cancelled":
		return false, failed()
	}
	return false, fmt.Errorf("gemini: stato del job non previsto: %q", it.Status)
}

// result estrae il video (base64 o file da scaricare) e la stima dai token riportati.
func (c *Client) result(ctx context.Context, job provider.Job, it *interaction) (*provider.Result, error) {
	fmt.Fprintln(os.Stderr)
	v := it.video()
	if v == nil {
		return nil, errors.New("gemini: job completato ma nessun video nella risposta")
	}
	var data []byte
	var err error
	switch {
	case v.Data != "":
		if data, err = base64.StdEncoding.DecodeString(v.Data); err != nil {
			return nil, fmt.Errorf("gemini: video non leggibile nella risposta: %w", err)
		}
	case v.URI != "":
		if data, err = c.downloadFile(ctx, v.URI); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("gemini: job completato ma il video non ha né dati né indirizzo")
	}
	if provider.VideoMime(data) == "" {
		fmt.Fprintln(os.Stderr, "avviso: gemini: il video ricevuto non sembra un mp4, salvato com'è")
	}
	res := &provider.Result{Media: []provider.Media{{Mime: "video/mp4", Data: data}}, JobID: job.ID}
	if usd, src, ok := it.Usage.cost(); ok {
		res.CostEstUSD, res.CostSource = math.Round(usd*1e4)/1e4, src
	}
	return res, nil
}

// cost è il costo dai token riportati: l'uscita non di testo (video, audio)
// alla tariffa video, quella non suddivisa per modalità pure.
func (u *usage) cost() (float64, string, bool) {
	if u == nil {
		return 0, "", false
	}
	g := caps.GeminiUsage{Input: u.TotalInput, Thought: u.TotalThought, OutputVideo: u.TotalOutput}
	if len(u.OutputByModality) > 0 {
		g.OutputVideo = 0
		for _, m := range u.OutputByModality {
			if !strings.EqualFold(m.Modality, "text") {
				g.OutputVideo += m.Tokens
			}
		}
		g.OutputOther = max(u.TotalOutput-g.OutputVideo, 0)
	}
	return caps.GeminiUsageCost(g)
}

// downloadFile scarica il file di un video generato: indirizzo solo
// sull'origine dell'API e sotto <base>/files/, attesa dello stato ACTIVE, poi
// lo scaricamento (la chiave non segue i redirect verso altri host).
func (c *Client) downloadFile(ctx context.Context, raw string) ([]byte, error) {
	id, ok := c.fileID(raw)
	if !ok {
		return nil, errors.New("gemini: indirizzo del video fuori dall'API dei file di gemini: rifiutato")
	}
	if err := c.waitFile(ctx, id); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", c.APIKey)
	resp, err := c.videoHTTP().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gemini: download HTTP %d", resp.StatusCode)
	}
	return readLimited(resp.Body)
}

// fileID è l'id del file in un indirizzo <base>/files/<id>[:download][?alt=media]
// sull'origine dell'API; false per ogni altro indirizzo.
func (c *Client) fileID(raw string) (string, bool) {
	u, err := url.Parse(raw)
	b, berr := url.Parse(c.base())
	if err != nil || berr != nil || !c.apiOrigin(u) || u.Fragment != "" || (u.RawQuery != "" && u.RawQuery != "alt=media") {
		return "", false
	}
	rest, ok := strings.CutPrefix(u.EscapedPath(), strings.TrimSuffix(b.EscapedPath(), "/")+"/files/")
	if !ok {
		return "", false
	}
	id := strings.TrimSuffix(rest, ":download")
	return id, ValidID(id)
}

// waitFile attende che il file sia ACTIVE (FAILED = errore; stato assente: si
// prova lo scaricamento).
func (c *Client) waitFile(ctx context.Context, id string) error {
	for first := true; ; first = false {
		if !first {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pollInterval):
			}
		}
		var f struct {
			State string `json:"state"`
		}
		if err := c.videoJSON(stay(ctx), httpx.RetryPoll, http.MethodGet, c.base()+"/files/"+id, nil, &f); err != nil {
			return err
		}
		switch f.State {
		case "ACTIVE", "":
			return nil
		case "FAILED":
			return fmt.Errorf("gemini: file del video %s in stato FAILED", id)
		}
		fmt.Fprint(os.Stderr, ".")
	}
}

// jobTimeout è il tetto di attesa di un job video: 15 minuti se non indicato.
func jobTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return 15 * time.Minute
	}
	return d
}
