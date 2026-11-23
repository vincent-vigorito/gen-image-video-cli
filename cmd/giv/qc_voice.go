package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/provider/openaicompat"
	"gen-image-video-cli/internal/speech"
)

// giv qc --voice: controllo della voce. Il controllo tecnico del file
// (montage.Tool.QC, gratuito) e poi la trascrizione (a pagamento, una sola
// richiesta mai ripetuta) confrontata col testo atteso parola per parola,
// tenendo conto delle pronunce. Il controllo avvisa e basta: non rigenera,
// non scarta, non spende oltre la trascrizione, che passa da stima e tetto.

const qcVoiceHelp = `Controllo della voce (a pagamento: trascrizione con xai, 0,10 $ per ora di audio):
  giv qc --voice <file audio> --script "<testo>" | --script-file <f> [--pronuncia <file.json>]
         [--kit <kit.json>] [--nome <nome>]... [--lang it] [--max-cost <usd>] [--dry-run] [--strict]

  Prima i controlli tecnici del file (gratuiti), poi la trascrizione confrontata col testo
  atteso, parola per parola senza maiuscole, punteggiatura e accenti; una parola detta come
  la sua pronuncia del dizionario vale come la parola scritta; i nomi propri (--nome e
  nomi del kit) e le parole del dizionario vanno al provider come termini da riconoscere
  (al massimo 100: oltre, un avviso e i seguenti non inviati). Controlli in più:
  voice     parole diverse, mancanti e in più: nessuna ok, altrimenti avviso; meno di metà
            delle parole attese sentite uguali: difetto
  language  lingua rilevata diversa da --lang: avviso (non riportata: saltato)

  --voice <file>         audio da controllare: wav, mp3, ogg, flac, aac, m4a, mp4 (al massimo 100 MiB),
                         con una sola traccia audio (più tracce: codice 2, estrai quella da controllare)
  --script "<testo>"     testo atteso (come scritto); oppure --script-file <file> UTF-8
  --pronuncia <file>     pronunce JSON {"parola": "come dirla"} (sopra quelle del kit)
  --kit <kit.json>       pronunce, nomi propri e lingua dal kit
  --nome <nome>          nome proprio da far riconoscere (marca, personaggio), ripetibile: al più
                         100, ognuno al più 50 caratteri, senza caratteri di controllo
  --lang <codice>        lingua attesa (default it, o quella del kit)
  --provider xai         provider della trascrizione (solo xai)
  --max-cost <usd>       rifiuta (codice 2) se la stima supera il tetto o non è stimabile
  --dry-run              controlli tecnici, richiesta e stima, senza trascrizione
  --strict               un difetto fa uscire con codice 1 (il JSON esce comunque)

  Stima dalla durata contata sui campioni decodificati, arrotondata al minuto per eccesso (la granularità
  dell'addebito non è documentata): 30 s contano 0,001667 $. Una trascrizione
  dall'esito incerto esce con codice 5 e una riga failed in giv-log.jsonl, senza
  rilancio; un caricamento non arrivato fino in fondo è un errore senza addebito.
`

func init() {
	qcVoice.Usage = qcVoiceHelp
	qcVoice.Run = cmdQCVoice
}

// qcVoiceDefectRatio: sotto questa quota di parole attese sentite uguali il
// controllo della voce è un difetto (testo o lingua sbagliati, voce muta).
const qcVoiceDefectRatio = 0.5

// qcVoiceOptions sono i parametri del controllo della voce, nella forma dei
// flag. Dict, se non nil, sono le pronunce già pronte (giv story run): al
// posto di Kit e Pronuncia.
type qcVoiceOptions struct {
	Provider, File, Script, ScriptFile string
	Pronuncia, Kit, Lang               string
	Nomi                               []string // nomi propri (--nome; in giv story run quelli del kit)
	DryRun, Strict                     bool
	MaxCost                            *float64 // nil = nessun tetto
	Set                                []string // flag indicati
	Dict                               speech.Dictionary

	client speech.Transcriber // già creato (test); nil = dal provider all'avvio
}

// qcVoicePlan è l'uscita di --dry-run.
type qcVoicePlan struct {
	DryRun   bool   `json:"dry_run"`
	Command  string `json:"command"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
	*montage.QCReport
	Params     map[string]any `json:"params"`
	Request    map[string]any `json:"request"`
	CostEstUSD *float64       `json:"cost_est_usd"`
	CostSource string         `json:"cost_source,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
}

// qcTranscript è la trascrizione nell'uscita.
type qcTranscript struct {
	Text      string  `json:"text"`
	Language  string  `json:"language,omitempty"`
	DurationS float64 `json:"duration_s,omitempty"`
	Words     int     `json:"words"`
}

// qcVoiceOut è l'uscita di giv qc --voice: il controllo tecnico del file coi
// controlli voice e language in coda, la trascrizione, il confronto e i costi.
type qcVoiceOut struct {
	Command  string `json:"command"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
	*montage.QCReport
	Counts          map[string]int     `json:"counts"`
	Script          string             `json:"script"`
	Transcript      qcTranscript       `json:"transcript"`
	Comparison      *speech.Comparison `json:"comparison"`
	Params          map[string]any     `json:"params"`
	Warnings        []string           `json:"warnings,omitempty"`
	CostEstUSD      float64            `json:"cost_est_usd"`
	CostSource      string             `json:"cost_source,omitempty"`
	CostUsageUSD    float64            `json:"cost_usage_usd"`
	CostUsageSource string             `json:"cost_usage_source,omitempty"`

	usageUSD float64 // CostUsageUSD non arrotondato (conto del tetto di story run)
}

// voiceCheck è un controllo della voce verificato e pronto a partire:
// controllo tecnico fatto, stima e tetto passati.
type voiceCheck struct {
	o      qcVoiceOptions
	req    speech.TranscribeRequest
	script string
	dict   speech.Dictionary
	lang   string
	report *montage.QCReport
	secs   float64 // durata dai campioni decodificati (o, senza tetto, del contenitore)
	est    *speech.Estimate
	plan   *qcVoicePlan
}

func cmdQCVoice(args []string) error {
	o, err := parseQCVoiceFlags(args)
	if err != nil {
		return err
	}
	vc, err := prepareVoiceCheck(o)
	if err != nil {
		return err
	}
	if o.DryRun {
		return output.PrintJSON(vc.plan)
	}
	out, err := vc.run(context.Background())
	if err != nil {
		return err
	}
	if err := output.PrintJSON(out); err != nil {
		return err
	}
	if o.Strict && out.Result == montage.QCDefect {
		return &cliError{code: exitError, msg: fmt.Sprintf("giv qc --voice %s: %d difetti (--strict)", o.File, out.Counts[montage.QCDefect])}
	}
	return nil
}

// parseQCVoiceFlags legge i flag di giv qc --voice (nessun argomento
// posizionale: il file va in --voice).
func parseQCVoiceFlags(args []string) (qcVoiceOptions, error) {
	var o qcVoiceOptions
	fl := flag.NewFlagSet("qc", flag.ExitOnError)
	fl.Usage = func() { fmt.Fprint(fl.Output(), qcVoiceHelp) }
	fl.StringVar(&o.File, "voice", "", "file audio da controllare")
	fl.StringVar(&o.Script, "script", "", "testo atteso")
	fl.StringVar(&o.ScriptFile, "script-file", "", "testo atteso da un file")
	fl.StringVar(&o.Pronuncia, "pronuncia", "", "file JSON delle pronunce")
	fl.StringVar(&o.Kit, "kit", "", "kit di marca: pronunce, nomi propri e lingua")
	fl.Var((*stringList)(&o.Nomi), "nome", "nome proprio da far riconoscere (ripetibile)")
	fl.StringVar(&o.Lang, "lang", "", "lingua attesa")
	fl.StringVar(&o.Provider, "provider", "", "provider della trascrizione")
	maxCost := fl.Float64("max-cost", 0, "rifiuta se il costo stimato supera questi dollari")
	fl.BoolVar(&o.DryRun, "dry-run", false, "nessuna trascrizione: richiesta e costo stimato")
	fl.BoolVar(&o.Strict, "strict", false, "un difetto esce con codice 1")
	if err := fl.Parse(args); err != nil {
		return o, usageError("%v", err)
	}
	if fl.NArg() > 0 {
		return o, usageError("giv qc --voice: argomento in più %q (il file va in --voice; aiuto: giv qc --help)", fl.Arg(0))
	}
	fl.Visit(func(f *flag.Flag) {
		o.Set = append(o.Set, f.Name)
		if f.Name == "max-cost" {
			o.MaxCost = maxCost
		}
	})
	return o, nil
}

// prepareVoiceCheck fa tutto quello che non costa: flag, testo atteso,
// pronunce, lettura del file, controllo tecnico, stima e tetto. Nessuna
// chiave, nessuna chiamata.
func prepareVoiceCheck(o qcVoiceOptions) (*voiceCheck, error) {
	if o.MaxCost != nil && (math.IsNaN(*o.MaxCost) || math.IsInf(*o.MaxCost, 0) || *o.MaxCost < 0) {
		return nil, usageError("--max-cost deve essere un importo finito e non negativo in dollari")
	}
	for _, f := range []struct{ name, val string }{{"voice", o.File}, {"script", o.Script}, {"script-file", o.ScriptFile},
		{"pronuncia", o.Pronuncia}, {"kit", o.Kit}, {"lang", o.Lang}, {"provider", o.Provider}} {
		if slices.Contains(o.Set, f.name) && strings.TrimSpace(f.val) == "" {
			return nil, usageError("--%s vuoto", f.name)
		}
	}
	if o.File == "" {
		return nil, usageError("giv qc --voice <file audio> --script \"<testo>\": manca il file")
	}
	script := o.Script
	switch {
	case o.Script != "" && o.ScriptFile != "":
		return nil, usageError("--script e --script-file insieme: scegline uno")
	case o.ScriptFile != "":
		data, err := speech.ReadSmallFile(o.ScriptFile, speech.MaxTextFileBytes)
		if err != nil {
			return nil, usageError("--script-file %s: %v", o.ScriptFile, err)
		}
		script = string(data)
	}
	if !utf8.ValidString(script) {
		return nil, usageError("testo atteso non valido: serve UTF-8")
	}
	script = strings.TrimSpace(script)
	if script == "" {
		return nil, usageError("manca il testo atteso: --script \"<testo>\" oppure --script-file <file>")
	}
	if n := utf8.RuneCountInString(script); n > speech.MaxTextChars {
		return nil, usageError("testo atteso di %d caratteri: al massimo %d", n, speech.MaxTextChars)
	}
	names, err := speech.CheckNames("--nome", o.Nomi)
	if err != nil {
		return nil, usageError("%v", err)
	}
	dict, lang := o.Dict, o.Lang
	if o.Kit != "" {
		k, err := loadKit(o.Kit)
		if err != nil {
			return nil, err
		}
		names = append(names, k.Nomi...)
		if len(k.Pronuncia) > 0 {
			dict = dict.Merge(speech.Dictionary(maps.Clone(k.Pronuncia)))
		}
		if lang == "" && k.Voce != nil {
			lang = k.Voce.Lingua
		}
	}
	if o.Pronuncia != "" {
		d, err := speech.ReadDictionaryFile(o.Pronuncia)
		if err != nil {
			return nil, usageError("--pronuncia %v", err)
		}
		dict = dict.Merge(d)
	}
	if err := dict.Check(); err != nil {
		return nil, usageError("%v", err)
	}
	if lang == "" {
		lang = speech.DefaultLanguage
	}
	if _, _, err := speech.Check(speech.Request{Language: lang, Text: "-"}); err != nil {
		return nil, usageError("--lang: %v", err)
	}
	// il file si legge prima di ffprobe: una FIFO o un device non bloccano
	format := strings.TrimPrefix(strings.ToLower(filepath.Ext(o.File)), ".")
	if _, ok := speech.AudioFormats[format]; !ok {
		return nil, usageError("--voice %s: formato non ammesso (wav, mp3, ogg, flac, aac, m4a, mp4)", o.File)
	}
	audio, err := speech.ReadSmallFile(o.File, speech.MaxTranscribeBytes)
	if err != nil {
		return nil, usageError("--voice %s: %v", o.File, err)
	}
	req, warns, err := speech.CheckTranscribe(speech.TranscribeRequest{
		Provider: o.Provider, Audio: audio, Format: format, Keyterms: speech.Keyterms(names, dict),
	})
	if err != nil {
		return nil, usageError("--voice %s: %v", o.File, err)
	}
	// controllo tecnico e misura sulla stessa copia dei byte che vanno al
	// provider: un file cambiato nel frattempo non conta
	tmp, err := audioCopy(audio, format)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	audioOn := true
	rep, err := qcFile(tmp, montage.QCOptions{ExpectAudio: &audioOn})
	if err != nil {
		return nil, withPath(err, tmp, o.File)
	}
	rep.File = o.File
	// una sola traccia audio: quella misurata è quella che trascrive il provider
	if n, err := audioTracks(tmp); err != nil {
		return nil, usageError("--voice %s: tracce audio non lette: %v", o.File, withPath(err, tmp, o.File))
	} else if n != 1 {
		return nil, usageError("--voice %s: %d tracce audio: giv qc --voice ne accetta una sola (estrai quella da controllare, es. ffmpeg -i <file> -map 0:a:0 -c copy voce.%s)", o.File, n, format)
	}
	secs, measureErr := decodedSeconds(tmp)
	if measureErr != nil {
		measureErr = withPath(measureErr, tmp, o.File)
		if o.MaxCost != nil {
			return nil, usageError("--max-cost: costo della trascrizione non stimabile (durata dell'audio non misurata sui campioni: %v)", measureErr)
		}
		secs = rep.DurationS
		warns = append(warns, fmt.Sprintf("durata dell'audio non misurata sui campioni (%v): stima sulla durata dichiarata dal file", measureErr))
	}
	params := map[string]any{"file": o.File, "format": format, "bytes": len(audio), "script_chars": utf8.RuneCountInString(script),
		"language": lang, "keyterms": len(req.Keyterms)}
	if len(dict) > 0 {
		params["pronunce"] = len(dict)
	}
	if len(names) > 0 {
		params["nomi"] = len(names)
	}
	for _, f := range []struct{ k, v string }{{"script_file", o.ScriptFile}, {"pronuncia_file", o.Pronuncia}, {"kit", o.Kit}} {
		if f.v != "" {
			params[f.k] = f.v
		}
	}
	plan := &qcVoicePlan{DryRun: true, Command: "qc", Provider: req.Provider, Model: "stt", Mode: "voice", QCReport: rep,
		Params: params, Request: map[string]any{
			"fields": openaicompat.GrokSTTFields(req),
			"file":   map[string]any{"name": "audio." + format, "mime": speech.AudioFormats[format], "bytes": len(audio)},
		}}
	for _, w := range warns {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
		plan.Warnings = append(plan.Warnings, w)
	}
	est, why := speech.EstimateTranscribe(req.Provider, secs, time.Now())
	if est != nil {
		plan.CostEstUSD, plan.CostSource = &est.USD, est.Source
	}
	if o.MaxCost != nil {
		if est == nil {
			return nil, usageError("--max-cost: costo della trascrizione non stimabile (%s)", why)
		}
		if est.Exact > *o.MaxCost { // sul valore non arrotondato
			return nil, usageError("costo stimato della trascrizione %.6f $ oltre --max-cost %.6f $ (%s)", est.USD, *o.MaxCost, est.Source)
		}
	}
	if est == nil {
		msg := "stima del costo della trascrizione non disponibile: " + why
		fmt.Fprintf(os.Stderr, "avviso: %s\n", msg)
		plan.Warnings = append(plan.Warnings, msg)
	}
	return &voiceCheck{o: o, req: req, script: script, dict: dict, lang: lang, report: rep, secs: secs, est: est, plan: plan}, nil
}

// audioCopy scrive i byte dell'audio in un file temporaneo privato (0600)
// con l'estensione del formato: la copia che si controlla e si misura.
func audioCopy(audio []byte, format string) (string, error) {
	f, err := os.CreateTemp("", "giv-qc-voce-*."+format)
	if err != nil {
		return "", fmt.Errorf("giv qc --voice: copia temporanea dell'audio: %w", err)
	}
	_, werr := f.Write(audio)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("giv qc --voice: copia temporanea dell'audio: %w", werr)
	}
	return f.Name(), nil
}

// withPath rimette nei messaggi il percorso dell'utente al posto della copia.
func withPath(err error, tmp, path string) error {
	var ce *cliError
	if errors.As(err, &ce) {
		return &cliError{code: ce.code, msg: strings.ReplaceAll(ce.msg, tmp, path)}
	}
	return errors.New(strings.ReplaceAll(err.Error(), tmp, path))
}

// qcVoiceDecodeTimeout: tempo massimo della decodifica che misura l'audio.
const qcVoiceDecodeTimeout = 5 * time.Minute

// audioTracks conta le tracce audio di path con ffprobe (gli argomenti di
// montage.ProbeArgs: lista, solo file locali e formati ammessi).
func audioTracks(path string) (int, error) {
	args, err := montage.ProbeArgs(path)
	if err != nil {
		return 0, err
	}
	bin := localTool.FFprobe
	if bin == "" {
		bin = "ffprobe"
	}
	if bin, err = exec.LookPath(bin); err != nil {
		return 0, errors.New("ffprobe non trovato")
	}
	ctx, cancel := context.WithTimeout(context.Background(), montage.ProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return 0, errors.New("ffprobe oltre il tempo massimo")
		}
		return 0, fmt.Errorf("ffprobe non riuscito (%v)", err)
	}
	var raw struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return 0, errors.New("risposta di ffprobe illeggibile")
	}
	n := 0
	for _, st := range raw.Streams {
		if st.CodecType == "audio" {
			n++
		}
	}
	return n, nil
}

// decodedSeconds è la durata dell'audio di path contata sui campioni
// decodificati dell'unica traccia audio (audioTracks) (ffmpeg con argomenti separati, PCM
// a 16 bit mono alla frequenza della traccia, nessun ricampionamento): non la
// durata dichiarata dal contenitore, nessun arrotondamento.
func decodedSeconds(path string) (float64, error) {
	info, err := localMedia("--voice", path)
	if err != nil {
		return 0, err
	}
	if info.Audio == nil || info.Audio.SampleRate <= 0 || info.Audio.SampleRate > 768000 {
		return 0, errors.New("traccia audio o frequenza non lette")
	}
	rate := info.Audio.SampleRate
	bin := localTool.FFmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	if bin, err = exec.LookPath(bin); err != nil {
		return 0, errors.New("ffmpeg non trovato")
	}
	ctx, cancel := context.WithTimeout(context.Background(), qcVoiceDecodeTimeout)
	defer cancel()
	var n byteCounter
	cmd := exec.CommandContext(ctx, bin, "-nostdin", "-hide_banner", "-loglevel", "error", "-i", path,
		"-map", "0:a:0", "-ac", "1", "-ar", strconv.Itoa(rate), "-f", "s16le", "-")
	cmd.Stdout, cmd.Stderr = &n, io.Discard
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return 0, errors.New("decodifica dell'audio oltre il tempo massimo")
		}
		return 0, fmt.Errorf("decodifica dell'audio non riuscita (%v)", err)
	}
	if n < 2 {
		return 0, errors.New("nessun campione audio decodificato")
	}
	return float64(n/2) / float64(rate), nil
}

// byteCounter conta i byte scritti e li scarta.
type byteCounter int64

func (c *byteCounter) Write(p []byte) (int, error) {
	*c += byteCounter(len(p))
	return len(p), nil
}

// run trascrive e confronta. Una trascrizione dall'esito incerto lascia una
// riga failed nel registro (può essere stata addebitata) ed esce con codice 5.
func (v *voiceCheck) run(ctx context.Context) (*qcVoiceOut, error) {
	c := v.o.client
	if c == nil {
		p, err := newProvider(v.req.Provider)
		if err != nil {
			return nil, err
		}
		t, ok := p.(speech.Transcriber)
		if !ok {
			return nil, usageError("trascrizione non disponibile con %s", v.req.Provider)
		}
		c = t
	}
	man := manifest{Command: "qc", Provider: v.req.Provider, Model: "stt", Prompt: v.script, Params: v.plan.Params,
		Files: []output.FileInfo{}, Warnings: slices.Clone(v.plan.Warnings)}
	if v.est != nil {
		man.CostEstUSD, man.CostSource = v.est.USD, v.est.Source
	}
	fmt.Fprintf(os.Stderr, "giv qc --voice: trascrizione con %s (%.1f s di audio)…\n", v.req.Provider, v.secs)
	at := time.Now()
	tr, err := c.Transcribe(ctx, v.req)
	if err != nil {
		var se *httpx.StartError
		if errors.As(err, &se) {
			failed := man
			failed.Status = "failed"
			logRun(failed)
			return nil, &cliError{code: exitAPI, msg: fmt.Sprintf("trascrizione dall'esito incerto: %v — può essere stata addebitata, richiesta non ripetuta: controlla sul provider prima di rilanciare", se.Err)}
		}
		return nil, err
	}
	rep := *v.report
	rep.Checks = slices.Clone(rep.Checks)
	out := &qcVoiceOut{Command: "qc", Provider: v.req.Provider, Model: "stt", Mode: "voice", QCReport: &rep,
		Counts: map[string]int{montage.QCOK: 0, montage.QCWarning: 0, montage.QCDefect: 0}, Script: v.script,
		Transcript: qcTranscript{Text: tr.Text, Language: tr.Language, DurationS: tr.DurationS, Words: len(tr.Words)},
		Params:     v.plan.Params, Warnings: slices.Clone(v.plan.Warnings)}
	for _, w := range tr.Warnings {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
		out.Warnings = append(out.Warnings, w)
	}
	if v.est != nil {
		out.CostEstUSD, out.CostSource = v.est.USD, v.est.Source
	}
	usd, src := tr.UsageUSD, tr.UsageSource
	if usd <= 0 { // durata non riportata: quella misurata sul file
		if u, s, ok := speech.TranscribeCost(v.req.Provider, v.secs, at); ok {
			usd, src = u, s+" (durata misurata sul file)"
		}
	}
	out.usageUSD, out.CostUsageUSD, out.CostUsageSource = usd, math.Round(usd*1e6)/1e6, src
	cmp, err := speech.Compare(v.script, tr.Text, v.dict)
	voice, lang := voiceCompareCheck(cmp, err), voiceLanguageCheck(v.lang, tr.Language)
	rep.Checks = append(rep.Checks, voice, lang)
	rep.Result = montage.QCWorst(rep.Result, voice.Result, lang.Result)
	out.Comparison = cmp
	rep.Tally(out.Counts)
	man.DurationS, man.Warnings = tr.DurationS, out.Warnings
	man.CostUsageUSD, man.CostUsageSource, man.usageEstUSD = out.CostUsageUSD, out.CostUsageSource, usd
	logRun(man)
	return out, nil
}

// voiceCompareCheck è il controllo voice dal confronto.
func voiceCompareCheck(c *speech.Comparison, err error) montage.QCCheck {
	ch := montage.QCCheck{Name: "voice", Expected: 1.0, Threshold: qcVoiceDefectRatio}
	if err != nil {
		ch.Result, ch.Skipped, ch.Message = montage.QCWarning, true, err.Error()
		return ch
	}
	ch.Value = math.Round(c.Ratio*1000) / 1000
	switch {
	case c.Same():
		ch.Result, ch.Message = montage.QCOK, "trascrizione uguale al testo atteso"
	case c.Ratio < qcVoiceDefectRatio:
		ch.Result = montage.QCDefect
	default:
		ch.Result = montage.QCWarning
	}
	if !c.Same() {
		ch.Message = fmt.Sprintf("%d parole attese su %d sentite uguali: %d diverse, %d mancanti, %d in più",
			c.Matched, c.Words, len(c.Different), len(c.Missing), len(c.Extra))
	}
	return ch
}

// voiceLanguageCheck è il controllo language: la lingua rilevata contro
// quella attesa; non riportata, saltato.
func voiceLanguageCheck(expected, detected string) montage.QCCheck {
	ch := montage.QCCheck{Name: "language", Result: montage.QCOK, Expected: expected}
	if detected == "" {
		ch.Skipped, ch.Message = true, "lingua non riportata dalla trascrizione"
		return ch
	}
	ch.Value = detected
	if !speech.LanguageMatches(expected, detected) {
		ch.Result, ch.Message = montage.QCWarning, fmt.Sprintf("lingua rilevata %s, attesa %s", detected, expected)
	}
	return ch
}
