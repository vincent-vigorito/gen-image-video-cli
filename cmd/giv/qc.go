package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/story"
)

// giv qc: controllo tecnico di un file o delle clip e dei montati di uno
// storyboard (montage.Tool.QC), in locale con ffprobe e ffmpeg: nessuna
// chiamata a provider, nessuna spesa, nessun file scritto (niente riga in
// giv-log.jsonl). Il controllo avvisa e basta: non corregge, non rigenera.

const qcHelp = `giv qc — controllo tecnico di un video (o di un audio) in locale con ffprobe e ffmpeg:
gratuito, nessun file scritto

Uso:
  giv qc <file|storyboard.json> [--expect-duration <s>] [--expect-size <LxA>] [--expect-fps <n>]
         [--expect-audio on|off] [--expect-loudness <lufs>] [--for <canali>] [--strict]
         [--black-min <s>] [--freeze-min <s>] [--freeze-noise <dB>] [--silence-min <s>] [--silence-noise <dB>]

Controlli, ognuno ok, avviso o difetto, con misura, attesa, soglia e tratti trovati:
  duration  durata: sotto l'attesa difetto, sopra avviso (tolleranza 0,2 s o il 2%); senza attesa
            sotto 1 s avviso
  size      misure, contro --expect-size (diverse: difetto)
  fps       fotogrammi al secondo, contro --expect-fps (scarto oltre 0,1: difetto)
  audio     traccia audio, contro --expect-audio (diversa dall'attesa: difetto; assente senza attesa: avviso)
  black     tratti neri: avviso; per metà della durata o più: difetto
  freeze    tratti fermi (anche la scena quasi ferma), esclusi i neri: avviso; per metà o più: difetto
  silence   tratti muti dell'audio: avviso; per metà della durata o più: difetto
  loudness  volume integrato (LUFS), contro --expect-loudness (scarto oltre 1 LU: avviso)
  peak      picco vero: sopra -1 dBTP avviso, sopra 0 difetto
  streams   tracce in più (seconda traccia video o audio, sottotitoli, dati): avviso; la copertina
            allegata (come nelle clip xAI) no

Flag:
  --expect-duration <s>    durata attesa in secondi
  --expect-size <LxA>      misure attese in pixel (es. 1080x1920)
  --expect-fps <n>         fotogrammi al secondo attesi
  --expect-audio on|off    traccia audio attesa (on) o da non avere (off)
  --expect-loudness <lufs> volume integrato atteso (da -70 a -5)
  --for <canali>           confronta anche col profilo dei canali di giv export: instagram, tiktok,
                           youtube, linkedin separati da virgole, col formato facoltativo
                           <canale>:<formato> (es. instagram,tiktok o instagram:4:5); per canale
                           misure, fps, durata, codec, inizio rapido (moov in testa), volume -14 LUFS
  --strict                 un difetto fa uscire con codice 1 (il JSON esce comunque)
  --black-min <s>          tratto nero più corto segnalato (default 0.5)
  --freeze-min <s>         tratto fermo più corto segnalato (default 2)
  --freeze-noise <dB>      differenza fra fotogrammi sotto cui l'immagine è ferma (default -60;
                           più alto, es. -50, trova anche le scene quasi ferme)
  --silence-min <s>        tratto muto più corto segnalato (default 2)
  --silence-noise <dB>     volume sotto cui l'audio è muto (default -50)
  -h, --help               questo aiuto

Con uno storyboard (.json): una riga per scena (scenes) con la clip in uso di giv story run o il
file della scena (immagine: senza tratti fermi), con la durata e l'audio chiesti dallo storyboard,
e una per montato di giv story cut (outputs) con misure e fps del formato (il cartello finale non
conta fra i tratti fermi); --for vale per i montati del formato del canale. Le scene senza clip e i montati che mancano sono "assente". Le attese
--expect-* valgono solo per un file.

Uscita: su stdout solo il JSON (result, counts, checks, channels…); codice 0 anche con difetti
(1 con --strict); file mancante o illeggibile, canale o valore non valido: codice 2.
`

// qcAbsent è l'esito di una riga dello storyboard senza file da controllare
// (scena non generata, montato che manca): non conta per --strict.
const qcAbsent = "assente"

// qcVoice è giv qc --voice, il controllo della voce (cmd/giv/qc_voice.go,
// V-T2): se Run c'è, riceve tutti gli argomenti di giv qc quando fra loro
// c'è --voice; Usage va in coda all'aiuto.
var qcVoice struct {
	Usage string
	Run   func(args []string) error
}

// qcJudge è giv qc --judge, il giudizio di aderenza (cmd/giv/qc_judge.go):
// se Run c'è, riceve tutti gli argomenti di giv qc quando fra loro c'è
// --judge; Usage va in coda all'aiuto.
var qcJudge struct {
	Usage string
	Run   func(args []string) error
}

// qcFileOut è l'uscita di giv qc su un file.
type qcFileOut struct {
	Command  string `json:"command"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
	*montage.QCReport
	Counts   map[string]int `json:"counts"`
	Params   map[string]any `json:"params"`
	Warnings []string       `json:"warnings,omitempty"`
	CostUSD  float64        `json:"cost_usd"`
}

// qcRow è una riga di giv qc su uno storyboard: una scena (ID, Tipo, Take)
// o un montato (Format), col controllo del suo file.
type qcRow struct {
	ID     string `json:"id,omitempty"`
	Tipo   string `json:"tipo,omitempty"`
	Take   int    `json:"take,omitempty"`   // ripresa in uso (scene video e avatar)
	Format string `json:"format,omitempty"` // formato del montato
	*montage.QCReport
	Message string `json:"message,omitempty"` // perché la riga è assente o illeggibile
}

// qcStoryOut è l'uscita di giv qc su uno storyboard.
type qcStoryOut struct {
	Command    string         `json:"command"`
	Provider   string         `json:"provider"`
	Model      string         `json:"model"`
	Mode       string         `json:"mode"`
	Storyboard string         `json:"storyboard"`
	Result     string         `json:"result"` // il peggiore delle righe controllate; nessuna: assente
	Counts     map[string]int `json:"counts"`
	Scenes     []qcRow        `json:"scenes"`
	Outputs    []qcRow        `json:"outputs"`
	Params     map[string]any `json:"params"`
	Warnings   []string       `json:"warnings,omitempty"`
	CostUSD    float64        `json:"cost_usd"`
}

// qcVoiceArg dice se gli argomenti chiedono il controllo della voce.
func qcVoiceArg(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		name, _, _ := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if strings.HasPrefix(a, "-") && name == "voice" {
			return true
		}
	}
	return false
}

// qcSize legge --expect-size "<L>x<A>".
func qcSize(v string) (int, int, error) {
	a, b, ok := strings.Cut(strings.ToLower(strings.TrimSpace(v)), "x")
	w, err1 := strconv.Atoi(a)
	h, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || w < 1 || h < 1 || w > 7680 || h > 7680 || a[0] == '+' || b[0] == '+' {
		return 0, 0, usageError("--expect-size %q: larghezza e altezza in pixel, es. 1080x1920 (da 1 a 7680)", v)
	}
	return w, h, nil
}

// qcFile controlla un file locale (montage.Tool.QC): file mancante,
// illeggibile o senza tracce e valori non validi sono errori d'uso (codice
// 2), ffmpeg o ffprobe assenti codice 1. Serve a giv qc e a giv story run e
// cut (V-T8).
func qcFile(path string, o montage.QCOptions) (*montage.QCReport, error) {
	info, err := localMedia("giv qc", path)
	if err != nil {
		return nil, err
	}
	rep, err := localTool.QC(context.Background(), path, info, o)
	switch {
	case err == nil:
		return rep, nil
	case errors.Is(err, montage.ErrInvalid):
		return nil, usageError("giv qc %s: %s", path, localErrMsg(err))
	case errors.Is(err, montage.ErrMissingTool):
		return nil, &cliError{code: exitError, msg: err.Error()}
	}
	return nil, fmt.Errorf("giv qc: %w", err)
}

// qcSceneOptions sono le attese per la clip o il file della scena sc: la
// durata chiesta e l'audio on/off (scene video), nessun tratto fermo per le
// immagini.
func qcSceneOptions(s *story.Storyboard, sc *story.Scene) montage.QCOptions {
	var o montage.QCOptions
	switch sc.Tipo {
	case story.Immagine:
		o.Still = true
	case story.Video:
		if sc.Durata > 0 {
			o.ExpectDurationS = float64(sc.Durata)
		}
		switch firstOf(sc.Audio, s.Default.Audio) {
		case "on":
			on := true
			o.ExpectAudio = &on
		case "off":
			off := false
			o.ExpectAudio = &off
		}
	}
	return o
}

// qcCutName è il montato del formato format ("" : quello dello storyboard),
// relativo alla cartella dello storyboard: lo stesso nome di cutPlanFor.
func qcCutName(s *story.Storyboard, path, format string) string {
	out := ""
	if s.Montaggio != nil {
		out = s.Montaggio.Uscita
	}
	if out == "" {
		out = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + ".mp4"
	}
	if format != "" {
		ext := filepath.Ext(out)
		out = strings.TrimSuffix(out, ext) + "-" + strings.ReplaceAll(format, ":", "x") + ext
	}
	return out
}

// qcCardSeconds è la durata del cartello finale accodato da giv story cut
// (montaggio.cartello_finale, anche dal kit); 0 senza cartello.
func qcCardSeconds(s *story.Storyboard) float64 {
	if s.Montaggio == nil || s.Montaggio.CartelloFinale == nil {
		return 0
	}
	if d := s.Montaggio.CartelloFinale.Durata; d != nil {
		return *d
	}
	return montage.DefaultCardSeconds
}

// qcRowCheck controlla il file della riga: un file illeggibile diventa una
// riga con esito difetto, ffmpeg assente ferma tutto.
func qcRowCheck(row *qcRow, file string, o montage.QCOptions) error {
	rep, err := qcFile(file, o)
	if err != nil {
		var ce *cliError
		if errors.As(err, &ce) && ce.code == exitUsage {
			row.QCReport = &montage.QCReport{File: file, Result: montage.QCDefect, Checks: []montage.QCCheck{}}
			row.Message = strings.TrimPrefix(err.Error(), "giv qc ")
			return nil
		}
		return err
	}
	row.QCReport = rep
	return nil
}

// absent segna la riga come senza file da controllare.
func (row *qcRow) absent(file, msg string) {
	row.QCReport = &montage.QCReport{File: file, Result: qcAbsent, Checks: []montage.QCCheck{}}
	row.Message = msg
}

// qcStory controlla lo storyboard già letto (path è il suo file): una riga
// per scena, con la clip in uso (video, avatar) o il file della scena (clip,
// immagine), e una per montato di giv story cut; i canali valgono per i
// montati del loro formato. Tutti i file restano nella cartella dello
// storyboard (Storyboard.CheckFile, State.ClipFile).
func qcStory(s *story.Storyboard, path string, th montage.QCThresholds, profiles []montage.Profile) (*qcStoryOut, error) {
	out := &qcStoryOut{Command: "qc", Provider: localProvider, Model: localModel, Mode: "storyboard", Storyboard: path,
		Counts: map[string]int{montage.QCOK: 0, montage.QCWarning: 0, montage.QCDefect: 0}, Scenes: []qcRow{}, Outputs: []qcRow{}}
	var st *story.State
	images := false
	for i := range s.Scene {
		sc := &s.Scene[i]
		row := qcRow{ID: sc.ID, Tipo: sc.Tipo}
		var file string
		switch sc.Tipo {
		case story.Clip, story.Immagine:
			images = images || sc.Tipo == story.Immagine
			f, err := s.CheckFile(sc.File)
			if err != nil {
				row.absent(sc.File, err.Error())
				out.Scenes = append(out.Scenes, row)
				continue
			}
			file = f
		case story.Video, story.Avatar:
			if st == nil {
				var err error
				if st, err = story.LoadState(story.StatePath(path)); err != nil {
					return nil, usageError("%s: %v", path, err)
				}
			}
			clip, err := st.ClipFile(s, sc.ID)
			if err != nil {
				msg := err.Error()
				if errors.Is(err, story.ErrNotDone) {
					msg = "clip non ancora generata (giv story run)"
				}
				row.absent("", msg)
				out.Scenes = append(out.Scenes, row)
				continue
			}
			file, row.Take = clip, st.Scenes[sc.ID].ClipTakes()
		default:
			continue
		}
		o := qcSceneOptions(s, sc)
		o.Thresholds = th
		if err := qcRowCheck(&row, file, o); err != nil {
			return nil, err
		}
		out.Scenes = append(out.Scenes, row)
	}
	formats, err := cutFormats(s, "")
	if err != nil {
		return nil, usageError("%s: %v", path, err)
	}
	matched := map[string]bool{}
	for _, f := range formats {
		aspect := cutAspectOf(s, f)
		row := qcRow{Format: aspect}
		rel := qcCutName(s, path, f)
		file, err := s.CheckFile(rel)
		if err != nil {
			row.absent(s.Path(rel), "montato non ancora fatto (giv story cut)")
			out.Outputs = append(out.Outputs, row)
			continue
		}
		var o montage.QCOptions
		if w, h, fps, err := cutFormatFor(s, f); err == nil {
			o.ExpectWidth, o.ExpectHeight, o.ExpectFPS = w, h, float64(fps)
		}
		// con scene immagine il montato ha tratti fermi voluti; il cartello
		// finale (cutOverlays) è una coda ferma voluta
		o.Still, o.StillTailS, o.Thresholds = images, qcCardSeconds(s), th
		for _, p := range profiles {
			if p.Aspect.String() == aspect {
				o.Profiles = append(o.Profiles, p)
				matched[p.Spec()] = true
			}
		}
		if err := qcRowCheck(&row, file, o); err != nil {
			return nil, err
		}
		out.Outputs = append(out.Outputs, row)
	}
	for _, p := range profiles {
		if !matched[p.Spec()] {
			out.Warnings = append(out.Warnings, fmt.Sprintf("--for %s: nessun montato nel formato %s (montaggio.formati)", p.Spec(), p.Aspect))
		}
	}
	var results []string
	for _, rows := range [][]qcRow{out.Scenes, out.Outputs} {
		for _, r := range rows {
			if montage.QCRank(r.Result) >= 0 {
				results = append(results, r.Result)
				r.Tally(out.Counts)
			}
		}
	}
	out.Result = qcAbsent
	if len(results) > 0 {
		out.Result = montage.QCWorst(results...)
	}
	return out, nil
}

// qcTarget legge gli argomenti di giv qc: i flag prima o dopo il file.
func qcTarget(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", usageError("%v", err)
	}
	var target string
	if rest := fs.Args(); len(rest) > 0 {
		target = rest[0]
		if err := fs.Parse(rest[1:]); err != nil {
			return "", usageError("%v", err)
		}
		if fs.NArg() > 0 {
			return "", usageError("giv qc: un solo file o storyboard (in più: %q)", fs.Arg(0))
		}
	}
	if strings.TrimSpace(target) == "" {
		return "", usageError("giv qc: manca il file o lo storyboard (giv qc <file|storyboard.json>)")
	}
	return target, nil
}

// cmdQC è giv qc.
func cmdQC(args []string) error {
	if qcJudgeArg(args) {
		if qcVoiceArg(args) {
			return usageError("giv qc: --judge e --voice insieme: uno alla volta")
		}
		if qcJudge.Run == nil {
			return usageError("giv qc --judge: giudizio di aderenza non disponibile in questa versione")
		}
		return qcJudge.Run(args)
	}
	if qcVoiceArg(args) {
		if qcVoice.Run == nil {
			return usageError("giv qc --voice: controllo della voce non disponibile in questa versione")
		}
		return qcVoice.Run(args)
	}
	fs := flag.NewFlagSet("qc", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, qcHelp)
		if qcVoice.Usage != "" {
			fmt.Fprint(os.Stderr, "\n"+qcVoice.Usage)
		}
		if qcJudge.Usage != "" {
			fmt.Fprint(os.Stderr, "\n"+qcJudge.Usage)
		}
	}
	expDur := fs.Float64("expect-duration", 0, "durata attesa in secondi")
	expSize := fs.String("expect-size", "", "misure attese LxA")
	expFPS := fs.Float64("expect-fps", 0, "fps attesi")
	expAudio := fs.String("expect-audio", "", "on|off")
	expLoud := fs.Float64("expect-loudness", 0, "volume atteso in LUFS")
	forFlag := fs.String("for", "", "canali")
	strict := fs.Bool("strict", false, "un difetto esce con codice 1")
	var th montage.QCThresholds
	fs.Float64Var(&th.BlackMinS, "black-min", 0, "tratto nero minimo in secondi")
	fs.Float64Var(&th.FreezeMinS, "freeze-min", 0, "tratto fermo minimo in secondi")
	fs.Float64Var(&th.FreezeNoiseDB, "freeze-noise", 0, "soglia dei tratti fermi in dB")
	fs.Float64Var(&th.SilenceMinS, "silence-min", 0, "tratto muto minimo in secondi")
	fs.Float64Var(&th.SilenceNoiseDB, "silence-noise", 0, "soglia del muto in dB")
	target, err := qcTarget(fs, args)
	if err != nil {
		return err
	}
	set := flagsSet(fs)
	var o montage.QCOptions
	// un flag dato vale per il suo valore: zero, vuoto o non finito è un errore
	for _, f := range []struct {
		name string
		v    float64
		neg  bool // livello in dB: sotto zero
	}{{"expect-duration", *expDur, false}, {"expect-fps", *expFPS, false}, {"black-min", th.BlackMinS, false},
		{"freeze-min", th.FreezeMinS, false}, {"silence-min", th.SilenceMinS, false},
		{"freeze-noise", th.FreezeNoiseDB, true}, {"silence-noise", th.SilenceNoiseDB, true}} {
		if !set[f.name] {
			continue
		}
		switch {
		case !finiteNum(f.v):
			return usageError("--%s %v: serve un numero", f.name, f.v)
		case f.neg && f.v >= 0:
			return usageError("--%s %v: dB sotto 0 (es. -50)", f.name, f.v)
		case !f.neg && f.v <= 0:
			return usageError("--%s %v: più di 0", f.name, f.v)
		}
	}
	o.ExpectDurationS, o.ExpectFPS, o.Thresholds = *expDur, *expFPS, th
	if set["expect-size"] {
		if o.ExpectWidth, o.ExpectHeight, err = qcSize(*expSize); err != nil {
			return err
		}
	}
	if set["expect-audio"] {
		switch *expAudio {
		case "on", "off":
			v := *expAudio == "on"
			o.ExpectAudio = &v
		default:
			return usageError("--expect-audio %q: on oppure off", *expAudio)
		}
	}
	if set["expect-loudness"] {
		if !finiteNum(*expLoud) {
			return usageError("--expect-loudness %v: serve un numero", *expLoud)
		}
		v := *expLoud
		o.ExpectLoudness = &v
	}
	if set["for"] {
		if o.Profiles, err = montage.ParseChannels(*forFlag); err != nil {
			return usageError("--for: %s", localErrMsg(err))
		}
	}
	if err := o.Check(); err != nil {
		return usageError("giv qc: %s", localErrMsg(err))
	}
	params := map[string]any{"strict": *strict, "thresholds": th.WithDefaults()}
	if len(o.Profiles) > 0 {
		specs := make([]string, len(o.Profiles))
		for i, p := range o.Profiles {
			specs[i] = p.Spec()
		}
		params["for"] = specs
	}

	st, err := os.Stat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return usageError("giv qc %s: non trovato", target)
	case err != nil:
		return usageError("giv qc %s: %v", target, err)
	case st.IsDir():
		return usageError("giv qc %s: è una cartella: indica un file o uno storyboard", target)
	}

	var result string
	var defects int
	if strings.EqualFold(filepath.Ext(target), ".json") {
		for _, name := range []string{"expect-duration", "expect-size", "expect-fps", "expect-audio", "expect-loudness"} {
			if set[name] {
				return usageError("giv qc: --%s vale solo per un file: con uno storyboard le attese vengono dalle scene e dal formato", name)
			}
		}
		s, err := loadStory(target)
		if err != nil {
			return err
		}
		out, err := qcStory(s, target, th, o.Profiles)
		if err != nil {
			return err
		}
		out.Params = params
		for _, w := range out.Warnings {
			fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
		}
		if err := output.PrintJSON(out); err != nil {
			return err
		}
		result, defects = out.Result, out.Counts[montage.QCDefect]
		for _, rows := range [][]qcRow{out.Scenes, out.Outputs} {
			for _, r := range rows {
				if r.Result == montage.QCDefect && len(r.Checks) == 0 {
					defects++ // file illeggibile
				}
			}
		}
	} else {
		rep, err := qcFile(target, o)
		if err != nil {
			return err
		}
		out := qcFileOut{Command: "qc", Provider: localProvider, Model: localModel, Mode: "file", QCReport: rep,
			Counts: map[string]int{montage.QCOK: 0, montage.QCWarning: 0, montage.QCDefect: 0}, Params: params}
		rep.Tally(out.Counts)
		if err := output.PrintJSON(out); err != nil {
			return err
		}
		result, defects = rep.Result, out.Counts[montage.QCDefect]
	}
	if *strict && result == montage.QCDefect {
		return &cliError{code: exitError, msg: fmt.Sprintf("giv qc %s: %d difetti (--strict)", target, defects)}
	}
	return nil
}
