package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/judge"
)

// Giudizio di aderenza (giv qc --judge, controllo.aderenza): POST
// /v1beta/models/<modello>:generateContent con le immagini in linea, una sola
// richiesta a pagamento, senza redirect né ritentativi, chiave solo
// all'origine dell'API, risposta entro maxJudgeBytes, errori filtrati. La
// risposta è un dato: se ne leggono solo il testo della prima candidata, il
// motivo di fine, il blocco e i token; un 2xx illeggibile non è un errore
// (giudizio incerto), perché la richiesta è stata fatta.

// judgeTimeout è il tempo massimo della richiesta del giudizio.
const judgeTimeout = 3 * time.Minute

// maxJudgeBytes è il massimo della risposta del giudizio (solo testo).
const maxJudgeBytes = 2 << 20

// JudgeReply è la risposta del giudizio, coi soli campi letti.
type JudgeReply struct {
	Text    string       // testo della prima candidata, senza le parti di ragionamento
	Finish  string       // motivo di fine (STOP, MAX_TOKENS, SAFETY…), ripulito
	Blocked string       // motivo del blocco della richiesta, ripulito
	Invalid string       // perché la risposta non si è potuta leggere ("" se letta)
	Usage   *judge.Usage // token riportati; nil se mancano
}

// JudgeEndpoint è l'indirizzo della richiesta del giudizio per model.
func (c *Client) JudgeEndpoint(model string) string {
	return c.base() + "/models/" + model + ":generateContent"
}

// APIOrigin è schema e host dell'API: il destinatario del giudizio.
func (c *Client) APIOrigin() string {
	u, err := url.Parse(c.base())
	if err != nil || u.Host == "" {
		return "<api>"
	}
	return u.Scheme + "://" + u.Host
}

// Judge invia la richiesta del giudizio (body: judge.Body con le immagini).
// Errori classificati con httpx.Start: 4xx e connessione mai aperta = non
// partita; 3xx, 5xx, rete e scadenza = *httpx.StartError (può essere
// addebitata, non si rilancia). Un 2xx si restituisce sempre, anche se
// illeggibile (Invalid).
func (c *Client) Judge(ctx context.Context, model string, body []byte) (*JudgeReply, error) {
	if !judge.ValidModel(model) {
		return nil, fmt.Errorf("gemini: modello %q non valido: richiesta non fatta", model)
	}
	data, err := c.judgeCall(ctx, c.JudgeEndpoint(model), body)
	if err != nil {
		return nil, httpx.Start(err)
	}
	return parseJudge(hideKey(data, c.APIKey)), nil
}

// judgeCall è la POST del giudizio: solo sull'origine dell'API (lì va la
// chiave), nessun redirect (checkRedirect: un 3xx torna come errore HTTP,
// esito incerto), nessun ritentativo, errori senza corpo grezzo né chiave.
func (c *Client) judgeCall(ctx context.Context, rawURL string, body []byte) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !c.apiOrigin(u) {
		return nil, errors.New("gemini: indirizzo fuori dall'origine dell'API: richiesta non fatta")
	}
	ctx, cancel := context.WithTimeout(ctx, judgeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("gemini: richiesta non valida")
	}
	req.Header.Set("x-goog-api-key", c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.videoHTTP().Do(req)
	if err != nil {
		return nil, netError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJudgeBytes+1))
	if err != nil {
		return nil, netError(err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := "redirect non seguito"
		if resp.StatusCode < 300 || resp.StatusCode > 399 {
			msg = videoAPIError(hideKey(data, c.APIKey)) // la chiave via prima di accorciare
		}
		return nil, &httpx.Error{Prefix: "gemini", Status: resp.StatusCode, Msg: msg,
			RetryAfter: httpx.ParseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if len(data) > maxJudgeBytes {
		// la richiesta è arrivata: risposta troppo grande, giudizio incerto
		return []byte{}, nil
	}
	return data, nil
}

// parseJudge legge una risposta 2xx di generateContent.
func parseJudge(data []byte) *JudgeReply {
	r := &JudgeReply{}
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    *string `json:"text"`
					Thought bool    `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback *struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		UsageMetadata *struct {
			PromptTokenCount     *int64 `json:"promptTokenCount"`
			CandidatesTokenCount *int64 `json:"candidatesTokenCount"`
			ThoughtsTokenCount   *int64 `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	if len(data) == 0 {
		r.Invalid = fmt.Sprintf("risposta vuota o oltre %d MiB", maxJudgeBytes>>20)
		return r
	}
	if json.Unmarshal(data, &resp) != nil {
		r.Invalid = "risposta non JSON"
		return r
	}
	if m := resp.UsageMetadata; m != nil && m.PromptTokenCount != nil {
		u := &judge.Usage{Prompt: *m.PromptTokenCount}
		if m.CandidatesTokenCount != nil {
			u.Candidates = *m.CandidatesTokenCount
		}
		if m.ThoughtsTokenCount != nil {
			u.Thoughts = *m.ThoughtsTokenCount
		}
		if u.Prompt > 0 && u.Candidates >= 0 && u.Thoughts >= 0 {
			r.Usage = u
		}
	}
	if f := resp.PromptFeedback; f != nil && f.BlockReason != "" {
		r.Blocked = safeText(f.BlockReason, 60)
	}
	if len(resp.Candidates) == 0 {
		if r.Blocked == "" {
			r.Invalid = "risposta senza candidate"
		}
		return r
	}
	cand := resp.Candidates[0]
	r.Finish = safeText(cand.FinishReason, 60)
	var b strings.Builder
	for _, p := range cand.Content.Parts {
		if p.Text != nil && !p.Thought {
			b.WriteString(*p.Text)
		}
	}
	r.Text = b.String()
	return r
}
