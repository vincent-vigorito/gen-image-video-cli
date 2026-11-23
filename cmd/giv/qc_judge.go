package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/judge"
	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/provider/gemini"
	"gen-image-video-cli/internal/speech"
)

// giv qc --judge: giudizio di aderenza di un video (internal/judge). I
// controlli tecnici del file (gratuiti), poi quattro fotogrammi, la richiesta
// della scena e il riferimento facoltativo vanno a Gemini diretto in una sola
// richiesta, mai ripetuta, dopo stima e tetto; prima dell'invio stderr dice
// destinatario, dati e stima. Il rapporto avvisa e basta: nessun giudizio
// rigenera un video. Lo stesso servizio (prepareJudge, judgeCheck.run) serve
// al controllo.aderenza di giv story.

const qcJudgeHelp = `Giudizio di aderenza (a pagamento: gemini diretto, solo su richiesta con --judge):
  giv qc <video> --judge --prompt "<richiesta>" | --prompt-file <f> [--ref <immagine>]
         [--model <modello>] [--max-cost <usd>] [--dry-run] [--strict]

  Prima i controlli tecnici del file (gratuiti), poi 4 fotogrammi del video (al centro di
  quattro parti uguali della durata), la richiesta e il riferimento facoltativo vanno a gemini
  (generativelanguage.googleapis.com) in una sola richiesta, mai ripetuta. Le immagini vanno
  ricodificate in JPEG (lato lungo al più 768 px) senza metadati; nient'altro del file o della
  cartella. Il rapporto (judgment) dice aderente, non aderente o incerto, con riconoscibilità
  del riferimento, azione richiesta, motivi, difetti visti e ciò che non si può valutare. Il
  giudizio avvisa e basta: nessun video si rigenera, l'autore decide se rifare la scena.
  Controllo in più:
  adherence  aderente: ok; non aderente o incerto: avviso

  --judge                chiede il giudizio (senza --judge giv qc non invia niente)
  --video <file>         video da giudicare (oppure come argomento)
  --prompt "<testo>"     richiesta della scena (al più 4000 caratteri); oppure --prompt-file <f> UTF-8
  --ref <immagine>       riferimento facoltativo, file .png, .jpg o .webp: personaggio o prodotto
  --model <modello>      modello visivo di gemini (default gemini-2.5-flash-lite)
  --max-cost <usd>       rifiuta (codice 2, nessun invio) se la stima supera il tetto o non è stimabile
  --dry-run              controlli tecnici, destinatario, immagini, richiesta e stima, senza invio
  --strict               un difetto fa uscire con codice 1 (il JSON esce comunque)

  Prima dell'invio, su stderr: destinatario, immagini (istante, misure, byte), caratteri della
  richiesta e stima. Stima per eccesso dal listino: ingresso al massimo (testo un token per
  byte, immagini 258 token per riquadro) e uscita al limite della richiesta (maxOutputTokens
  2048); l'addebito viene dai token riportati. Ogni giudizio inviato lascia una riga in
  giv-log.jsonl. Risposta illeggibile: giudizio incerto, codice 0. Esito incerto (redirect,
  errore del server, rete): codice 5 e riga failed, senza rilancio (può essere stato addebitato).
`

func init() {
	qcJudge.Usage = qcJudgeHelp
	qcJudge.Run = cmdQCJudge
}

// judgeClient è chi invia il giudizio (gemini.Client; nei test un finto).
type judgeClient interface {
	Judge(ctx context.Context, model string, body []byte) (*gemini.JudgeReply, error)
}

// judgeOptions sono i parametri del giudizio, nella forma dei flag.
type judgeOptions struct {
	File, Prompt, PromptFile, Ref, Model string
	DryRun, Strict                       bool
	MaxCost                              *float64 // nil = nessun tetto
	Set                                  []string // flag indicati
	// NoTech: niente controllo tecnico del file (giv story: lo fa
	// controllo.tecnico).
	NoTech bool

	client judgeClient // già creato (test); nil = gemini all'invio
}

// judgeImageInfo descrive un'immagine inviata.
type judgeImageInfo struct {
	Role   string   `json:"role"` // fotogramma, riferimento
	File   string   `json:"file"` // video o immagine da cui viene
	TimeS  *float64 `json:"time_s,omitempty"`
	Width  int      `json:"width"`
	Height int      `json:"height"`
	Mime   string   `json:"mime"`
	Bytes  int      `json:"bytes"`
	SHA256 string   `json:"sha256"`
}

// judgeRequestInfo dice destinatario e dati del giudizio: tutto ciò che va al
// provider (con Body solo nel piano di --dry-run, immagini in segnaposto).
type judgeRequestInfo struct {
	Destination     string           `json:"destination"`
	Endpoint        string           `json:"endpoint"`
	Images          []judgeImageInfo `json:"images"`
	PromptChars     int              `json:"prompt_chars"`
	MaxOutputTokens int              `json:"max_output_tokens"`
	Body            map[string]any   `json:"body,omitempty"`
}

// qcJudgePlan è l'uscita di --dry-run.
type qcJudgePlan struct {
	DryRun   bool   `json:"dry_run"`
	Command  string `json:"command"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
	*montage.QCReport
	Prompt     string           `json:"prompt"`
	Params     map[string]any   `json:"params"`
	Request    judgeRequestInfo `json:"request"`
	CostEstUSD *float64         `json:"cost_est_usd"`
	CostSource string           `json:"cost_source,omitempty"`
	Warnings   []string         `json:"warnings,omitempty"`
}

// qcJudgeOut è l'uscita di giv qc --judge: il controllo tecnico col
// controllo adherence in coda, il rapporto, i dati inviati e i costi.
type qcJudgeOut struct {
	Command  string `json:"command"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
	*montage.QCReport
	Counts          map[string]int   `json:"counts,omitempty"`
	Prompt          string           `json:"prompt"`
	Judgment        *judge.Report    `json:"judgment"`
	JudgedAt        time.Time        `json:"judged_at"`
	Request         judgeRequestInfo `json:"request"`
	Params          map[string]any   `json:"params"`
	Warnings        []string         `json:"warnings,omitempty"`
	CostEstUSD      float64          `json:"cost_est_usd"`
	CostSource      string           `json:"cost_source,omitempty"`
	CostUsageUSD    float64          `json:"cost_usage_usd"`
	CostUsageSource string           `json:"cost_usage_source,omitempty"`

	usageUSD float64 // CostUsageUSD non arrotondato (conto del tetto di giv story run)
}

// judgeCheck è un giudizio verificato e pronto a partire: fotogrammi
// estratti, richiesta costruita, stima e tetto passati. Nessuna chiave letta,
// nessuna chiamata fatta.
type judgeCheck struct {
	o      judgeOptions
	req    judge.Request
	body   []byte
	report *montage.QCReport // controllo tecnico (nil con NoTech)
	est    *judge.Estimate
	plan   *qcJudgePlan
}

// qcJudgeArg dice se gli argomenti chiedono il giudizio.
func qcJudgeArg(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && name == "judge" {
			return true
		}
	}
	return false
}

func cmdQCJudge(args []string) error {
	o, err := parseQCJudgeFlags(args)
	if err != nil {
		return err
	}
	jc, err := prepareJudge(o)
	if err != nil {
		return err
	}
	if o.DryRun {
		return output.PrintJSON(jc.plan)
	}
	out, err := jc.run(context.Background())
	if err != nil {
		return err
	}
	if err := output.PrintJSON(out); err != nil {
		return err
	}
	if o.Strict && out.Result == montage.QCDefect {
		return &cliError{code: exitError, msg: fmt.Sprintf("giv qc --judge %s: %d difetti (--strict)", o.File, out.Counts[montage.QCDefect])}
	}
	return nil
}

// parseQCJudgeFlags legge i flag di giv qc --judge: il video in --video o
// come unico argomento, i flag prima o dopo.
func parseQCJudgeFlags(args []string) (judgeOptions, error) {
	var o judgeOptions
	fl := flag.NewFlagSet("qc", flag.ExitOnError)
	fl.Usage = func() { fmt.Fprint(fl.Output(), qcJudgeHelp) }
	judgeOn := fl.Bool("judge", false, "giudizio di aderenza")
	fl.StringVar(&o.File, "video", "", "video da giudicare")
	fl.StringVar(&o.Prompt, "prompt", "", "richiesta della scena")
	fl.StringVar(&o.PromptFile, "prompt-file", "", "richiesta della scena da un file")
	fl.StringVar(&o.Ref, "ref", "", "immagine di riferimento")
	fl.StringVar(&o.Model, "model", "", "modello visivo di gemini")
	maxCost := fl.Float64("max-cost", 0, "rifiuta se il costo stimato supera questi dollari")
	fl.BoolVar(&o.DryRun, "dry-run", false, "nessun invio: destinatario, dati e costo stimato")
	fl.BoolVar(&o.Strict, "strict", false, "un difetto esce con codice 1")
	if err := fl.Parse(args); err != nil {
		return o, usageError("%v", err)
	}
	if rest := fl.Args(); len(rest) > 0 {
		if o.File != "" {
			return o, usageError("giv qc --judge: il video va in --video oppure come argomento, non tutti e due (in più: %q)", rest[0])
		}
		o.File = rest[0]
		if err := fl.Parse(rest[1:]); err != nil {
			return o, usageError("%v", err)
		}
		if fl.NArg() > 0 {
			return o, usageError("giv qc --judge: un solo video (in più: %q)", fl.Arg(0))
		}
	}
	// il giudizio parte solo col valore effettivo di --judge: --judge=false,
	// o --judge preso come valore di un altro flag, non inviano niente
	if !*judgeOn {
		return o, usageError("giv qc: --judge non attivo (--judge=false o usato come valore di un altro flag): nessun invio; per il giudizio --judge da solo")
	}
	fl.Visit(func(f *flag.Flag) {
		o.Set = append(o.Set, f.Name)
		if f.Name == "max-cost" {
			o.MaxCost = maxCost
		}
	})
	return o, nil
}

// judgeEndpoint è l'indirizzo del giudizio per model, come lo usa il client
// di gemini (baseURLs; i test lo puntano a un server finto).
func judgeEndpoint(model string) (dest, endpoint string) {
	c := &gemini.Client{Base: baseURLs["gemini"]}
	return c.APIOrigin(), c.JudgeEndpoint(model)
}

// prepareJudge fa tutto quello che non costa: flag, richiesta della scena,
// controllo tecnico, fotogrammi, riferimento, corpo della richiesta, stima e
// tetto. Nessuna chiave, nessuna chiamata.
func prepareJudge(o judgeOptions) (*judgeCheck, error) {
	if o.MaxCost != nil && (math.IsNaN(*o.MaxCost) || math.IsInf(*o.MaxCost, 0) || *o.MaxCost < 0) {
		return nil, usageError("--max-cost deve essere un importo finito e non negativo in dollari")
	}
	for _, f := range []struct{ name, val string }{{"video", o.File}, {"prompt", o.Prompt}, {"prompt-file", o.PromptFile},
		{"ref", o.Ref}, {"model", o.Model}} {
		if slices.Contains(o.Set, f.name) && strings.TrimSpace(f.val) == "" {
			return nil, usageError("--%s vuoto", f.name)
		}
	}
	if strings.TrimSpace(o.File) == "" {
		return nil, usageError("giv qc --judge <video> --prompt \"<richiesta>\": manca il video")
	}
	prompt := o.Prompt
	switch {
	case o.Prompt != "" && o.PromptFile != "":
		return nil, usageError("--prompt e --prompt-file insieme: scegline uno")
	case o.PromptFile != "":
		data, err := speech.ReadSmallFile(o.PromptFile, 64<<10)
		if err != nil {
			return nil, usageError("--prompt-file %s: %v", o.PromptFile, err)
		}
		prompt = string(data)
	}
	if !utf8.ValidString(prompt) {
		return nil, usageError("richiesta della scena non valida: serve UTF-8")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, usageError("manca la richiesta della scena: --prompt \"<testo>\" oppure --prompt-file <file>")
	}
	if n := utf8.RuneCountInString(prompt); n > judge.MaxPromptChars {
		return nil, usageError("richiesta della scena di %d caratteri: al massimo %d", n, judge.MaxPromptChars)
	}
	model := o.Model
	if model == "" {
		model = judge.DefaultModel
	}
	if !judge.ValidModel(model) {
		return nil, usageError("--model %q: nome di modello non valido (lettere minuscole, cifre, punti e trattini)", model)
	}
	if o.Ref != "" && strings.Contains(o.Ref, "://") {
		return nil, usageError("--ref %s: serve un file locale (.png, .jpg, .webp), non un indirizzo", o.Ref)
	}

	info, err := localMedia("giv qc", o.File)
	if err != nil {
		return nil, err
	}
	if info.Video == nil {
		return nil, usageError("giv qc --judge %s: nessuna traccia video da giudicare", o.File)
	}
	var rep *montage.QCReport
	if !o.NoTech {
		if rep, err = qcFile(o.File, montage.QCOptions{}); err != nil {
			return nil, err
		}
	}
	ctx := context.Background()
	stills, err := localTool.JudgeFrames(ctx, o.File, info)
	if err != nil {
		return nil, judgeLocalErr("giv qc --judge", err)
	}
	req := judge.Request{Model: model, Prompt: prompt}
	images := []judgeImageInfo{}
	if o.Ref != "" {
		st, err := localTool.JudgeImage(ctx, o.Ref)
		if err != nil {
			return nil, judgeLocalErr("--ref", err)
		}
		req.Ref = &judge.Image{Role: judge.RoleRef, Width: st.Width, Height: st.Height, Data: st.Data}
		images = append(images, judgeImage(*req.Ref, o.Ref, false))
	}
	for _, st := range stills {
		im := judge.Image{Role: judge.RoleFrame, TimeS: st.TimeS, Width: st.Width, Height: st.Height, Data: st.Data}
		req.Frames = append(req.Frames, im)
		images = append(images, judgeImage(im, o.File, true))
	}
	if err := req.Check(); err != nil {
		return nil, usageError("giv qc --judge: %v", err)
	}
	body, err := json.Marshal(judge.Body(req, true))
	if err != nil {
		return nil, errors.New("giv qc --judge: richiesta non costruita")
	}
	dest, endpoint := judgeEndpoint(model)
	params := map[string]any{"file": o.File, "frames": len(req.Frames), "prompt_chars": utf8.RuneCountInString(prompt)}
	for _, f := range []struct{ k, v string }{{"prompt_file", o.PromptFile}, {"ref", o.Ref}} {
		if f.v != "" {
			params[f.k] = f.v
		}
	}
	plan := &qcJudgePlan{DryRun: true, Command: "qc", Provider: judge.Provider, Model: model, Mode: "judge", QCReport: rep,
		Prompt: prompt, Params: params, Request: judgeRequestInfo{Destination: dest, Endpoint: endpoint, Images: images,
			PromptChars: utf8.RuneCountInString(prompt), MaxOutputTokens: judge.MaxOutputTokens, Body: judge.Body(req, false)}}
	est, why := judge.EstimateCost(req, time.Now())
	if est != nil {
		plan.CostEstUSD, plan.CostSource = &est.USD, est.Source
	}
	if o.MaxCost != nil {
		if est == nil {
			return nil, usageError("--max-cost: costo del giudizio non stimabile (%s): nessun invio", why)
		}
		if est.Exact > *o.MaxCost { // sul valore non arrotondato
			return nil, usageError("costo stimato del giudizio %.6f $ oltre --max-cost %.6f $ (%s): nessun invio", est.USD, *o.MaxCost, est.Source)
		}
	}
	if est == nil {
		msg := "stima del costo del giudizio non disponibile: " + why
		fmt.Fprintf(os.Stderr, "avviso: %s\n", msg)
		plan.Warnings = append(plan.Warnings, msg)
	}
	return &judgeCheck{o: o, req: req, body: body, report: rep, est: est, plan: plan}, nil
}

// judgeImage descrive un'immagine inviata (dal file file).
func judgeImage(im judge.Image, file string, frame bool) judgeImageInfo {
	sum := sha256.Sum256(im.Data)
	info := judgeImageInfo{Role: im.Role, File: file, Width: im.Width, Height: im.Height, Mime: "image/jpeg",
		Bytes: len(im.Data), SHA256: hex.EncodeToString(sum[:])}
	if frame {
		t := im.TimeS
		info.TimeS = &t
	}
	return info
}

// judgeLocalErr traduce gli errori di ffmpeg dei fotogrammi: dati non validi
// = errore d'uso (codice 2), ffmpeg assente = codice 1.
func judgeLocalErr(what string, err error) error {
	switch {
	case errors.Is(err, montage.ErrInvalid):
		return usageError("%s: %s", what, localErrMsg(err))
	case errors.Is(err, montage.ErrMissingTool):
		return &cliError{code: exitError, msg: err.Error()}
	}
	return fmt.Errorf("%s: %w", what, err)
}

// EstimateUSD è la stima non arrotondata del giudizio (ok false se non
// stimabile): il valore da riservare nel conto di giv story run.
func (j *judgeCheck) EstimateUSD() (float64, bool) {
	if j.est == nil {
		return 0, false
	}
	return j.est.Exact, true
}

// announce scrive su stderr, prima dell'invio, destinatario, dati e stima.
func (j *judgeCheck) announce() {
	r := j.plan.Request
	fmt.Fprintf(os.Stderr, "giudizio di aderenza: invio a %s %s (modello %s), una sola richiesta:\n", judge.Provider, r.Destination, j.req.Model)
	for _, im := range r.Images {
		at := ""
		if im.TimeS != nil {
			at = fmt.Sprintf(" a %.2f s", *im.TimeS)
		}
		fmt.Fprintf(os.Stderr, "  %s di %s%s: JPEG %dx%d senza metadati, %d byte\n", im.Role, im.File, at, im.Width, im.Height, im.Bytes)
	}
	fmt.Fprintf(os.Stderr, "  richiesta della scena: %d caratteri\n", r.PromptChars)
	if j.est != nil {
		fmt.Fprintf(os.Stderr, "  stima: %.6f $ (%s)\n", j.est.USD, j.est.Source)
	} else {
		fmt.Fprintln(os.Stderr, "  stima: non disponibile")
	}
}

// run invia il giudizio (una sola richiesta) e legge il rapporto. Un esito
// incerto lascia una riga failed nel registro (può essere stato addebitato)
// ed esce con codice 5; una risposta illeggibile è un giudizio incerto.
func (j *judgeCheck) run(ctx context.Context) (*qcJudgeOut, error) {
	c := j.o.client
	if c == nil {
		p, err := newProvider(judge.Provider)
		if err != nil {
			return nil, err
		}
		jc, ok := p.(judgeClient)
		if !ok {
			return nil, usageError("giudizio non disponibile con %s", judge.Provider)
		}
		c = jc
	}
	man := manifest{Command: "qc", Provider: judge.Provider, Model: j.req.Model, Prompt: j.req.Prompt, Params: j.plan.Params,
		Files: []output.FileInfo{}, Warnings: slices.Clone(j.plan.Warnings)}
	if j.est != nil {
		man.CostEstUSD, man.CostSource = j.est.USD, j.est.Source
	}
	j.announce()
	at := time.Now()
	reply, err := c.Judge(ctx, j.req.Model, j.body)
	if err != nil {
		var se *httpx.StartError
		if errors.As(err, &se) {
			failed := man
			failed.Status = "failed"
			logRun(failed)
			return nil, &cliError{code: exitAPI, msg: fmt.Sprintf("giudizio dall'esito incerto: %v — può essere stato addebitato, richiesta non ripetuta: controlla sul provider prima di rilanciare", se.Err)}
		}
		return nil, err
	}
	out := &qcJudgeOut{Command: "qc", Provider: judge.Provider, Model: j.req.Model, Mode: "judge", Prompt: j.req.Prompt,
		Judgment: judgeReport(reply), JudgedAt: at.UTC().Truncate(time.Second), Params: j.plan.Params,
		Warnings: slices.Clone(j.plan.Warnings)}
	out.Request = j.plan.Request
	out.Request.Body = nil
	if j.report != nil {
		rep := *j.report
		rep.Checks = slices.Clone(rep.Checks)
		ch := adherenceCheck(out.Judgment)
		rep.Checks = append(rep.Checks, ch)
		rep.Result = montage.QCWorst(rep.Result, ch.Result)
		out.QCReport = &rep
		out.Counts = map[string]int{montage.QCOK: 0, montage.QCWarning: 0, montage.QCDefect: 0}
		rep.Tally(out.Counts)
	}
	if j.est != nil {
		out.CostEstUSD, out.CostSource = j.est.USD, j.est.Source
	}
	usd, src, ok := judge.UsageCost(j.req.Model, reply.Usage, at)
	switch {
	case ok:
	case j.est != nil:
		// token non riportati: vale la stima per eccesso
		usd, src = j.est.Exact, "stima per eccesso (gemini non ha riportato i token)"
	default:
		w := "costo del giudizio non calcolabile: nessuna tariffa per il modello e nessun token riportato"
		if reply.Usage != nil {
			w = "costo del giudizio non calcolabile: nessuna tariffa per il modello"
		}
		out.Warnings = append(out.Warnings, w)
		fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
	}
	if j.est != nil && usd > j.est.Exact {
		w := fmt.Sprintf("addebito del giudizio %.6f $ oltre la stima %.6f $", usd, j.est.USD)
		out.Warnings = append(out.Warnings, w)
		fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
	}
	out.usageUSD, out.CostUsageUSD, out.CostUsageSource = usd, math.Round(usd*1e6)/1e6, src
	fmt.Fprintf(os.Stderr, "giudizio: %s\n", out.Judgment.Verdict)
	man.Warnings = out.Warnings
	man.CostUsageUSD, man.CostUsageSource, man.usageEstUSD = out.CostUsageUSD, out.CostUsageSource, usd
	logRun(man)
	return out, nil
}

// judgeReport è il rapporto dalla risposta: richiesta bloccata o risposta
// illeggibile danno un giudizio incerto.
func judgeReport(r *gemini.JudgeReply) *judge.Report {
	switch {
	case r == nil:
		return judge.UncertainReport("risposta del modello assente")
	case r.Blocked != "":
		return judge.UncertainReport("richiesta bloccata da gemini (" + r.Blocked + ")")
	case r.Invalid != "":
		return judge.UncertainReport("risposta del modello illeggibile (" + r.Invalid + ")")
	}
	rep := judge.Parse(r.Text)
	if rep.Message != "" && r.Finish != "" && r.Finish != "STOP" {
		rep.Message += " (fine della risposta: " + r.Finish + ")"
	}
	return rep
}

// adherenceCheck è il controllo adherence dal rapporto: aderente ok, non
// aderente o incerto avviso (il giudizio di un modello sbaglia nei due sensi:
// mai un difetto).
func adherenceCheck(r *judge.Report) montage.QCCheck {
	ch := montage.QCCheck{Name: "adherence", Result: montage.QCWarning, Value: r.Verdict, Expected: judge.Adherent}
	if r.Verdict == judge.Adherent {
		ch.Result = montage.QCOK
	}
	msgs := slices.Clone(r.Reasons)
	if r.Message != "" {
		msgs = append([]string{r.Message}, msgs...)
	}
	ch.Message = strings.Join(msgs, "; ")
	return ch
}
