package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/story"
)

// Valori del montaggio quando lo storyboard non li indica.
const (
	cutAspect      = "16:9"
	cutResolution  = "720p"
	cutFPS         = 30
	cutFadeSeconds = 0.5  // durata di ogni dissolvenza
	cutMusicVolume = 0.25 // musica sotto la voce
	cutVoiceSlack  = 0.05 // secondi di sovrapposizione della voce tollerati senza avviso
)

// storyCutResult è l'uscita di giv story cut.
type storyCutResult struct {
	Storyboard string   `json:"storyboard"`
	Output     string   `json:"output"`
	Scenes     int      `json:"scenes"`
	DurationS  float64  `json:"duration_s"`          // letta dal file scritto
	ExpectedS  float64  `json:"expected_duration_s"` // dal piano: somma delle scene meno le dissolvenze
	Width      int      `json:"width"`
	Height     int      `json:"height"`
	FPS        float64  `json:"fps"`
	VideoCodec string   `json:"video_codec"`
	AudioCodec string   `json:"audio_codec,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Format     string   `json:"format,omitempty"` // con più formati: il formato del file
	// Exports sono i file per canale di --export (senza più formati)
	Exports []exportItem `json:"exports,omitempty"`
}

// storyCutFormats è l'uscita di giv story cut con montaggio.formati o
// --formato: un risultato per formato, nell'ordine dello storyboard, e i
// file per canale di --export.
type storyCutFormats struct {
	Storyboard string           `json:"storyboard"`
	Scenes     int              `json:"scenes"`
	Formats    []storyCutResult `json:"formats"`
	Exports    []exportItem     `json:"exports,omitempty"`
}

// runStoryCut è giv story cut: monta con ffmpeg le clip già presenti, senza
// generazione e senza rete; il file d'uscita dello storyboard si riscrive a
// ogni montaggio.
func runStoryCut(args []string) error {
	path, only, exportSpec, err := storyCutArgs(args)
	if err != nil {
		return err
	}
	// la cartella dello storyboard si apre prima di leggerlo e resta aperta
	// fino alla pubblicazione: una cartella sostituita nel frattempo (rinomina
	// e link) non porta altrove il file montato
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return usageError("%s: storyboard non trovato", path)
		}
		return usageError("%s: %v", path, err)
	}
	defer root.Close()
	s, err := loadStory(path)
	if err != nil {
		return err
	}
	formats, err := cutFormats(s, only)
	if err != nil {
		return usageError("%s: %v", path, err)
	}
	// --export: ogni canale vuole uno dei formati montati, controllato prima
	var profiles []montage.Profile
	var sources []int // per canale, l'indice del formato montato
	if exportSpec != "" {
		if profiles, err = montage.ParseChannels(exportSpec); err != nil {
			return usageError("--export: %s", localErrMsg(err))
		}
		for _, pr := range profiles {
			i := slices.IndexFunc(formats, func(f string) bool { return cutAspectOf(s, f) == pr.Aspect.String() })
			if i < 0 {
				return usageError("%s: --export %s vuole il formato %s, che non si monta: aggiungilo a montaggio.formati (o a --formato)", path, pr.Spec(), pr.Aspect)
			}
			sources = append(sources, i)
		}
	}
	// tutti i piani prima di montare: un problema in un formato ferma tutto
	plans := make([]montage.Plan, len(formats))
	var outs []string // montati ed esportazioni, controllati prima di pubblicare
	for i, f := range formats {
		if plans[i], err = cutPlanFor(s, path, f); err != nil {
			return err
		}
		plans[i].RootDir = root
		outs = append(outs, plans[i].Output)
	}
	for k, pr := range profiles {
		outs = append(outs, exportName(plans[sources[k]].Output, formats[sources[k]], pr))
	}
	if err := checkCutOutputs(s, path, outs); err != nil {
		return err
	}
	tool := montage.Tool{}
	if err := tool.Check(); err != nil {
		return &cliError{code: exitError, msg: err.Error()}
	}
	multi := &storyCutFormats{Storyboard: path, Scenes: len(plans[0].Clips), Formats: []storyCutResult{}}
	for i := range plans {
		var clash []string
		if plans[i].Height >= plans[i].Width { // non orizzontale: 9:16, 4:5, 1:1
			for _, n := range logoTitleClash(s.Montaggio) {
				w := logoTitleWarning(n, cutAspectOf(s, formats[i]))
				fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
				clash = append(clash, w)
			}
		}
		out, err := cutOne(tool, path, &plans[i], formats[i])
		if err != nil {
			return err
		}
		out.Warnings = append(out.Warnings, clash...)
		multi.Formats = append(multi.Formats, *out)
	}
	contactSheet(s, path)
	if len(profiles) > 0 {
		if multi.Exports, err = cutExport(s, path, profiles, sources, multi.Formats); err != nil {
			return err
		}
	}
	if formats[0] == "" {
		out := multi.Formats[0]
		out.Exports = multi.Exports
		return output.PrintJSON(out)
	}
	return output.PrintJSON(multi)
}

// storyFiles sono i file locali che lo storyboard nomina e che esistono come
// file normali (scene, voci, immagini, riferimenti, musica, sottotitoli,
// logo, font): con cutProtected, i file che giv story cut non sostituisce mai.
func storyFiles(s *story.Storyboard) []string {
	var names []string
	add := func(p ...string) { names = append(names, p...) }
	for _, set := range s.Riferimenti {
		add(set...)
	}
	for i := range s.Scene {
		sc := &s.Scene[i]
		add(sc.File, sc.Voce, sc.Immagine, sc.UltimoFotogramma)
		add(sc.FotogrammiIntermedi...)
	}
	if m := s.Montaggio; m != nil {
		if m.Musica != nil {
			add(m.Musica.File)
		}
		add(m.Sottotitoli)
		if m.Logo != nil {
			add(m.Logo.File)
		}
		if c := m.CartelloFinale; c != nil {
			add(c.Logo)
		}
		if st := m.StileTesti; st != nil {
			add(st.Font)
		}
		for _, t := range m.Titoli {
			if t.Stile != nil {
				add(t.Stile.Font)
			}
		}
	}
	var out []string
	for _, n := range names {
		if n == "" || story.IsURL(n) {
			continue
		}
		p := s.Path(n)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			out = append(out, p)
		}
	}
	return out
}

// cutProtected sono i file che le uscite di giv story cut e il foglio di
// contatto non sostituiscono mai: lo storyboard, il suo stato, i file che
// nomina (storyFiles) e quelli generati che lo stato elenca (clip in uso,
// fotogrammi chiave, riprese sostituite) che esistono come file normali.
// Uno stato illeggibile non elenca file (ferma già il montaggio delle scene
// video).
func cutProtected(s *story.Storyboard, path string) []string {
	files := storyFiles(s)
	for _, p := range []string{path, story.StatePath(path)} {
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			files = append(files, p)
		}
	}
	st, err := story.LoadState(story.StatePath(path))
	if err != nil {
		return files
	}
	for _, sc := range st.Scenes {
		takes := []*story.Take{sc.Clip, sc.Keyframe}
		for i := range sc.Previous {
			takes = append(takes, &sc.Previous[i])
		}
		for _, t := range takes {
			if t == nil || t.File == "" || story.IsURL(t.File) {
				continue
			}
			if st, err := os.Stat(s.Path(t.File)); err == nil && st.Mode().IsRegular() {
				files = append(files, s.Path(t.File))
			}
		}
	}
	return files
}

// samePath dice se due percorsi sono lo stesso file: stesso percorso
// assoluto o, se esistono tutti e due, lo stesso file (link compresi).
func samePath(a, b string) bool {
	if aa, err := filepath.Abs(a); err == nil {
		if bb, err := filepath.Abs(b); err == nil && aa == bb {
			return true
		}
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}

// checkCutOutputs controlla, prima di pubblicare qualcosa, le uscite di giv
// story cut (montati per formato ed esportazioni): diverse fra loro e mai uno
// dei file di cutProtected. Errore d'uso, nessun montaggio.
func checkCutOutputs(s *story.Storyboard, path string, outs []string) error {
	protected := cutProtected(s, path)
	for i, o := range outs {
		for _, p := range protected {
			if samePath(o, p) {
				return usageError("%s: l'uscita %s sostituirebbe %s, un file dello storyboard o generato da giv story run: cambia montaggio.uscita", path, o, p)
			}
		}
		for _, prev := range outs[:i] {
			if samePath(o, prev) {
				return usageError("%s: due uscite nello stesso file %s: cambia montaggio.uscita", path, o)
			}
		}
	}
	return nil
}

// exportName è il file dell'esportazione pr dal montato del formato format
// ("" : quello dello storyboard): <uscita>-<canale>-<WxH>.mp4.
func exportName(montato, format string, pr montage.Profile) string {
	stem := strings.TrimSuffix(montato, filepath.Ext(montato))
	if format != "" {
		stem = strings.TrimSuffix(stem, "-"+strings.ReplaceAll(format, ":", "x"))
	}
	return stem + "-" + pr.Tag() + ".mp4"
}

// cutAspectOf è il rapporto del formato montato f ("" : quello dello storyboard).
func cutAspectOf(s *story.Storyboard, f string) string {
	if f != "" {
		return f
	}
	var fm story.Formato
	if s.Formato != nil {
		fm = *s.Formato
	}
	return firstOf(fm.Aspect, s.Default.Aspect, cutAspect)
}

// cutExport esporta per ogni canale il file montato nel suo formato (come
// giv export, profili di R-T7): file <uscita>-<canale>-<WxH>.mp4 accanto al
// montato, riscritti a ogni giv story cut; volume montaggio.volume o -14
// LUFS. Una durata fuori dai limiti del canale è un errore d'uso.
func cutExport(s *story.Storyboard, path string, profiles []montage.Profile, sources []int, done []storyCutResult) ([]exportItem, error) {
	loud := montage.DefaultLoudness
	if m := s.Montaggio; m != nil && m.Volume != nil {
		loud = *m.Volume
	}
	caches := map[string]*montage.ExportCache{}
	var items []exportItem
	for k, pr := range profiles {
		src := done[sources[k]]
		if err := pr.CheckDuration(src.DurationS); err != nil {
			return items, usageError("%s: --export %s: %s", path, pr.Spec(), localErrMsg(err))
		}
		info, err := localMedia("montato", src.Output)
		if err != nil {
			return items, err
		}
		e := montage.Export{Profile: pr, Fit: montage.FitCrop, SrcFPS: info.Video.FPS, Audio: info.Audio != nil}
		if e.Audio {
			e.Loudness = &loud
		}
		if caches[src.Output] == nil {
			caches[src.Output] = &montage.ExportCache{}
		}
		out := exportName(src.Output, src.Format, pr)
		fmt.Fprintf(os.Stderr, "esportazione %s in %s\n", pr.Spec(), out)
		res, err := localTool.RunOp(context.Background(), montage.Op{Inputs: append(cutProtected(s, path), src.Output), Output: out, Root: s.Dir,
			Replace: true, Build: montage.ExportBuild(src.Output, e, caches[src.Output])})
		if err != nil {
			if errors.Is(err, montage.ErrInvalid) {
				return items, usageError("%s: --export %s: %s", path, pr.Spec(), localErrMsg(err))
			}
			return items, err
		}
		item := exportItem{Channel: pr.Channel, Aspect: pr.Aspect.String(), File: res.Output, Width: pr.Width, Height: pr.Height,
			Convention: pr.Convention}
		if e.Audio && !caches[src.Output].Muted {
			item.LoudnessLUFS = &loud
		}
		if i := res.Info; i != nil {
			item.DurationS = round3(i.Duration)
			if v := i.Video; v != nil {
				item.Width, item.Height, item.FPS, item.VideoCodec = v.Width, v.Height, round3(v.FPS), v.Codec
			}
			if a := i.Audio; a != nil {
				item.AudioCodec = a.Codec
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// contactSheetName è il foglio di contatto che giv story run e cut scrivono
// da soli accanto allo storyboard.
const contactSheetName = "foglio-contatto.jpg"

// contactSheet scrive (o riscrive) foglio-contatto.jpg accanto allo
// storyboard: una riga per scena con id e ripresa (storySheet, come giv sheet
// sullo storyboard), salvo foglio_contatto false. È un di più: un problema
// è un avviso su stderr e non cambia l'esito del comando.
func contactSheet(s *story.Storyboard, path string) {
	if s.FoglioContatto != nil && !*s.FoglioContatto {
		return
	}
	warn := func(err error) {
		fmt.Fprintf(os.Stderr, "avviso: %s non scritto: %s\n", contactSheetName, strings.ReplaceAll(localErrMsg(err), "\n", " "))
	}
	p, err := storySheet(s, path, sheetOptions{Cols: 4, Labels: true})
	if err != nil {
		warn(err)
		return
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(os.Stderr, "avviso: %s: %s\n", contactSheetName, w)
	}
	if err := p.Sheet.Check(); err != nil {
		warn(err)
		return
	}
	inputs := cutProtected(s, path)
	for _, k := range sortedKeys(p.Inputs) {
		inputs = append(inputs, p.Inputs[k])
	}
	res, err := localTool.RunOp(context.Background(), montage.Op{Inputs: inputs, Output: filepath.Join(s.Dir, contactSheetName),
		Root: s.Dir, Replace: true, Build: p.Sheet.Build})
	if err != nil {
		warn(err)
		return
	}
	fmt.Fprintf(os.Stderr, "foglio di contatto: %s\n", res.Output)
}

// cutOne monta un formato (format vuoto: quello dello storyboard), con una
// scadenza sola per le letture delle clip e per il montaggio.
func cutOne(tool montage.Tool, path string, plan *montage.Plan, format string) (*storyCutResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), montage.DefaultTimeout)
	defer cancel()
	if err := keepAudio(ctx, tool, plan.Clips); err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		return nil, usageError("%s: %v", path, err)
	}
	fmt.Fprintf(os.Stderr, "montaggio di %d scene in %s\n", len(plan.Clips), plan.Output)
	res, err := tool.Montage(ctx, *plan)
	if err != nil {
		if errors.Is(err, montage.ErrInvalid) {
			return nil, usageError("%s: %v", path, err)
		}
		return nil, err
	}
	out := storyCutResult{Storyboard: path, Output: res.Output, Scenes: len(plan.Clips),
		ExpectedS: round3(res.Duration), Warnings: res.Warnings, Format: format}
	if i := res.Info; i != nil {
		out.DurationS = round3(i.Duration)
		if v := i.Video; v != nil {
			out.Width, out.Height, out.FPS, out.VideoCodec = v.Width, v.Height, round3(v.FPS), v.Codec
		}
		if a := i.Audio; a != nil {
			out.AudioCodec = a.Codec
		}
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
	}
	return &out, nil
}

// storyCutArgs legge gli argomenti di giv story cut: il file dello storyboard,
// --formato e --export (anche dopo il file).
func storyCutArgs(args []string) (path, format, export string, err error) {
	fl := flag.NewFlagSet("story cut", flag.ExitOnError)
	only := fl.String("formato", "", "monta solo questo formato")
	exp := fl.String("export", "", "canali per giv export")
	fl.Usage = func() { fmt.Fprint(os.Stderr, storySubHelp["cut"]) }
	const use = "uso: giv story cut [--formato <r>] [--export <canali>] <storyboard.json>"
	if err := fl.Parse(args); err != nil {
		return "", "", "", usageError("%v", err)
	}
	rest := fl.Args()
	if len(rest) == 0 {
		return "", "", "", usageError(use)
	}
	path = rest[0]
	if err := fl.Parse(rest[1:]); err != nil {
		return "", "", "", usageError("%v", err)
	}
	if fl.NArg() > 0 {
		return "", "", "", usageError(use)
	}
	set := flagsSet(fl)
	if set["formato"] && strings.TrimSpace(*only) == "" {
		return "", "", "", usageError("--formato vuoto (%s)", strings.Join(story.Formati, ", "))
	}
	if set["export"] && strings.TrimSpace(*exp) == "" {
		return "", "", "", usageError("--export vuoto (%s)", strings.Join(montage.Channels(), ", "))
	}
	return path, *only, *exp, nil
}

// cutFormats sono i formati da montare: con only quello solo (fra
// montaggio.formati, se ci sono), altrimenti montaggio.formati; senza
// nessuno dei due un solo formato vuoto, quello dello storyboard come prima.
func cutFormats(s *story.Storyboard, only string) ([]string, error) {
	var declared []string
	if s.Montaggio != nil {
		declared = s.Montaggio.Formati
	}
	if only == "" {
		if len(declared) == 0 {
			return []string{""}, nil
		}
		return declared, nil
	}
	if !slices.Contains(story.Formati, only) {
		return nil, fmt.Errorf("--formato %q sconosciuto (%s)", only, strings.Join(story.Formati, ", "))
	}
	if len(declared) > 0 && !slices.Contains(declared, only) {
		return nil, fmt.Errorf("--formato %s non è fra montaggio.formati (%s)", only, strings.Join(declared, ", "))
	}
	return []string{only}, nil
}

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

// cutPlan traduce lo storyboard nel piano di montaggio: formato, una clip per
// scena (file della scena, o la clip generata da giv story run), voce,
// transizione, musica, sottotitoli e uscita. Ogni problema è un errore d'uso.
func cutPlan(s *story.Storyboard, path string) (montage.Plan, error) {
	return cutPlanFor(s, path, "")
}

// cutPlanFor è cutPlan per il formato aspect ("9:16"…; vuoto: quello dello
// storyboard): stesso lato corto, file <uscita>-<WxH>.mp4.
func cutPlanFor(s *story.Storyboard, path, aspect string) (montage.Plan, error) {
	var p montage.Plan
	w, h, fps, err := cutFormatFor(s, aspect)
	if err != nil {
		return p, usageError("%s: %v", path, err)
	}
	p.Width, p.Height, p.FPS = w, h, fps
	m := s.Montaggio
	if m == nil {
		m = &story.Montaggio{}
	}
	switch m.Adatta {
	case "ritaglio":
		p.Fit = montage.FitCrop
	case "sfocato":
		p.Fit = montage.FitBlur
	case "bande":
		p.Fit = montage.FitPad
	}
	trans := montage.Transition{Kind: montage.Cut}
	if m.Transizione == "dissolvenza" {
		trans = montage.Transition{Kind: montage.Fade, Duration: cutFadeSeconds}
	}
	var missing []string
	var st *story.State // stato di giv story run, letto alla prima scena video
	for i := range s.Scene {
		sc := &s.Scene[i]
		c := montage.Clip{Duration: float64(sc.Durata), Transition: trans}
		switch sc.Tipo {
		case story.Clip:
			c.Path = s.Path(sc.File)
		case story.Immagine:
			c.Path, c.Image = s.Path(sc.File), true
		case story.Video, story.Avatar:
			if st == nil {
				var err error
				if st, err = story.LoadState(story.StatePath(path)); err != nil {
					return p, usageError("%s: %v", path, err)
				}
			}
			clip, err := st.ClipFile(s, sc.ID)
			if err != nil {
				missing = append(missing, sceneMissing(st, sc.ID, err))
				continue
			}
			c.Path = clip
		default:
			return p, usageError("%s: scena %s: tipo %q non montabile", path, sc.ID, sc.Tipo)
		}
		// la voce di una scena avatar è già nella clip (guida il labiale): non
		// si aggiunge una seconda volta, resta l'audio della clip
		if sc.Voce != "" && sc.Tipo != story.Avatar {
			c.Voice, c.VoiceAt = s.Path(sc.Voce), sc.Ritardo()
		}
		sceneCut(&c, sc)
		p.Clips = append(p.Clips, c)
	}
	if len(missing) > 0 {
		return p, usageError("%s: clip mancanti (genera le scene con giv story run):\n  %s", path, strings.Join(missing, "\n  "))
	}
	if mu := m.Musica; mu != nil {
		vol := cutMusicVolume
		if mu.Volume != nil {
			vol = *mu.Volume
		}
		// volume 0: musica muta, quindi niente musica (per montage 0 vale 1)
		if vol > 0 {
			p.Music = &montage.Music{Path: s.Path(mu.File), Volume: vol}
		}
	}
	if m.Sottotitoli != "" {
		p.Subtitles = s.Path(m.Sottotitoli)
		// con montaggio.formati o --formato un .srt sta nei margini sicuri del
		// formato (nel 9:16 sopra il 35% in basso); senza, come prima
		p.SafeSubtitles = aspect != ""
	}
	if err := cutOverlays(s, m, &p); err != nil {
		return p, usageError("%s: %v", path, err)
	}
	out := m.Uscita
	if out == "" {
		out = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + ".mp4"
	}
	if aspect != "" {
		ext := filepath.Ext(out)
		out = strings.TrimSuffix(out, ext) + "-" + strings.ReplaceAll(aspect, ":", "x") + ext
	}
	p.Output, p.Root = s.Path(out), s.Dir
	// controllo anticipato, per un errore d'uso prima del montaggio; la
	// pubblicazione resta comunque vincolata a Root
	if err := insideDir(s.Dir, filepath.Dir(p.Output)); err != nil {
		return p, usageError("%s: montaggio.uscita: %v", path, err)
	}
	// il montato è il prodotto dello storyboard: rifare cut lo riscrive
	p.Overwrite = true
	return p, nil
}

// cutOverlays porta nel piano, per il suo formato, logo, titoli, cartello
// finale, stile dei testi e audio del blocco montaggio (già convalidati da
// story.Validate): senza questi campi il piano resta quello di prima.
func cutOverlays(s *story.Storyboard, m *story.Montaggio, p *montage.Plan) error {
	h := p.Height
	fonts := map[string]bool{}
	// stile: base di montage, poi stile_testi, poi lo stile del titolo
	style := func(base montage.TextStyle, sts ...*story.StileTesti) (montage.TextStyle, error) {
		ts := base
		for _, st := range sts {
			if st == nil {
				continue
			}
			if st.Font != "" {
				file := s.Path(st.Font)
				fam, err := montage.FontFamily(file)
				if err != nil {
					return ts, fmt.Errorf("font %s: %v", st.Font, localErrMsg(err))
				}
				ts.Font = fam
				if !fonts[file] {
					fonts[file] = true
					p.Fonts = append(p.Fonts, file)
				}
			}
			if st.Colore != "" {
				c, err := montage.ParseColor(st.Colore)
				if err != nil {
					return ts, err
				}
				ts.Color = c
			}
			if d := st.Dimensione; d != nil {
				ts.Size = max(12, int(math.Round(*d*float64(h))))
				if ts.Border > 0 {
					ts.Border = math.Max(1, math.Round(float64(ts.Size)/16))
				}
			}
			switch st.Bordo {
			case "":
			case "nessuno":
				ts.Border = 0
			default:
				c, err := montage.ParseColor(st.Bordo)
				if err != nil {
					return ts, err
				}
				ts.Outline = c
				if ts.Border == 0 {
					ts.Border = math.Max(1, math.Round(float64(ts.Size)/16))
				}
			}
		}
		return ts, nil
	}
	if l := m.Logo; l != nil {
		lg := montage.Logo{Path: s.Path(l.File), Pos: montage.TopRight, Width: montage.DefaultLogoWidth, Opacity: 1}
		if l.Posizione != "" {
			lg.Pos = montage.Position(l.Posizione)
		}
		if l.Larghezza != nil {
			lg.Width = *l.Larghezza
		}
		if l.Opacita != nil {
			lg.Opacity = *l.Opacita
		}
		if l.Da != nil {
			lg.From = *l.Da
		}
		if l.A != nil {
			lg.To = *l.A
		}
		p.Logo = &lg
	}
	for i, t := range m.Titoli {
		ts, err := style(montage.DefaultTextStyle(h), m.StileTesti, t.Stile)
		if err != nil {
			return fmt.Errorf("montaggio.titoli n. %d: %v", i+1, err)
		}
		d := montage.DefaultTitleSeconds
		if t.Durata != nil {
			d = *t.Durata
		}
		pos := montage.Bottom
		if t.Posizione != "" {
			pos = montage.Position(t.Posizione)
		}
		ti := montage.Title{Text: montage.Text{Text: t.Testo, Start: t.Da, End: t.Da + d, Pos: pos, Style: ts, Fade: montage.TextFade(d)}}
		if t.Scena != "" {
			for j := range s.Scene {
				if s.Scene[j].ID == t.Scena {
					ti.Clip = j + 1
				}
			}
		}
		p.Titles = append(p.Titles, ti)
	}
	if c := m.CartelloFinale; c != nil {
		ts, err := style(montage.CardTextStyle(h), m.StileTesti)
		if err != nil {
			return fmt.Errorf("montaggio.cartello_finale: %v", err)
		}
		card := montage.EndCard{Duration: montage.DefaultCardSeconds, Color: montage.Black, Text: c.Testo, Subtext: c.Sottotesto, Style: ts}
		if c.Durata != nil {
			card.Duration = *c.Durata
		}
		if c.Sfondo != "" {
			if card.Color, err = montage.ParseColor(c.Sfondo); err != nil {
				return err
			}
		}
		if c.Logo != "" {
			card.Logo = &montage.Logo{Path: s.Path(c.Logo), Opacity: 1}
		}
		p.EndCard = &card
	}
	if mu := m.Musica; mu != nil && p.Music != nil {
		p.Duck = mu.SottoVoce
		if mu.SfumaFine != nil {
			p.MusicFade = *mu.SfumaFine
		}
	}
	if m.Volume != nil {
		p.Loudness = *m.Volume
	}
	return nil
}

// sceneCut porta nella clip fuoco, taglio e velocità della scena. Una scena
// tagliata o accelerata dura quanto il pezzo usato (tratto di taglio,
// altrimenti la clip, diviso velocità), anche con durata dichiarata: durata
// è la lunghezza della clip chiesta o attesa.
func sceneCut(c *montage.Clip, sc *story.Scene) {
	if f := sc.Fuoco; f != nil {
		x, y := f.XY()
		c.Focus = &montage.Focus{X: x, Y: y}
	}
	if sc.Taglio == nil && sc.Velocita == nil {
		return
	}
	if t := sc.Taglio; t != nil {
		c.TrimStart = t.Da
		if t.A != nil {
			c.TrimEnd = *t.A
		}
	}
	if v := sc.Velocita; v != nil {
		c.Speed = *v
	}
	c.Duration = 0
}

// retimedLength è la durata nel montaggio di una scena lunga length secondi
// (0: ignota) con taglio e velocità; 0 se non si può sapere.
func retimedLength(sc *story.Scene, length float64) float64 {
	if sc.Taglio == nil && sc.Velocita == nil {
		return length
	}
	if t := sc.Taglio; t != nil {
		if t.A != nil && (length == 0 || *t.A < length) {
			length = *t.A
		}
		if length == 0 {
			return 0
		}
		length -= t.Da
	}
	if v := sc.Velocita; v != nil && *v > 0 {
		length /= *v
	}
	return max(length, 0)
}

// keepAudio tiene l'audio delle clip che ne hanno uno (riprese vere, video
// generati con audio on); le altre scene sono mute sotto voce e musica.
func keepAudio(ctx context.Context, tool montage.Tool, clips []montage.Clip) error {
	for i := range clips {
		c := &clips[i]
		if c.Image {
			continue
		}
		info, err := tool.Probe(ctx, c.Path)
		if err != nil {
			return fmt.Errorf("scena n. %d: %v", i+1, err)
		}
		c.KeepAudio = info.Audio != nil
	}
	return nil
}

// cutFormat ricava risoluzione e fps del montato: formato dello storyboard,
// poi blocco default, poi 16:9 720p 30 fps. La risoluzione è il lato corto.
func cutFormat(s *story.Storyboard) (w, h, fps int, err error) {
	return cutFormatFor(s, "")
}

// cutFormatFor è cutFormat col rapporto forced ("9:16"…) al posto di quello
// dello storyboard, se non è vuoto.
func cutFormatFor(s *story.Storyboard, forced string) (w, h, fps int, err error) {
	var f story.Formato
	if s.Formato != nil {
		f = *s.Formato
	}
	aspect := firstOf(forced, f.Aspect, s.Default.Aspect, cutAspect)
	res := firstOf(f.Risoluzione, s.Default.Risoluzione, cutResolution)
	fps = f.FPS
	if fps == 0 {
		fps = cutFPS
	}
	short, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(res), "p"))
	if err != nil || !strings.HasSuffix(strings.ToLower(res), "p") || short < 144 || short > 2160 {
		return 0, 0, 0, fmt.Errorf("formato.risoluzione %q: serve un valore come 720p o 1080p", res)
	}
	a, b, ok := strings.Cut(aspect, ":")
	aw, err1 := strconv.Atoi(a)
	ah, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || aw < 1 || ah < 1 || aw > 100 || ah > 100 {
		return 0, 0, 0, fmt.Errorf("formato.aspect %q: serve un rapporto come 16:9 o 9:16", aspect)
	}
	even := func(v float64) int { return int(v/2+0.5) * 2 }
	if aw >= ah {
		w, h = even(float64(short)*float64(aw)/float64(ah)), even(float64(short))
	} else {
		w, h = even(float64(short)), even(float64(short)*float64(ah)/float64(aw))
	}
	if fps < 1 || fps > 120 {
		return 0, 0, 0, fmt.Errorf("formato.fps %d: da 1 a 120", fps)
	}
	return w, h, fps, nil
}

func firstOf(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

// insideDir controlla che dir (o la sua parte che esiste già) resti nella
// cartella dello storyboard anche seguendo i link.
func insideDir(root, dir string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("cartella dello storyboard non leggibile: %v", err)
	}
	d := dir
	for {
		if _, err := os.Lstat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	real, err := filepath.EvalSymlinks(d)
	if err != nil {
		return fmt.Errorf("%s: percorso non leggibile", dir)
	}
	if rel, err := filepath.Rel(realRoot, real); err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("la cartella d'uscita porta fuori dalla cartella dello storyboard")
	}
	return nil
}

// voiceOverlaps controlla la voce di ogni scena sulla linea del tempo del
// montaggio (durate delle scene, dissolvenze, ritardo_voce): deve finire prima
// che parta la voce della scena successiva che ne ha una, l'ultima prima della
// fine del montaggio. Ogni avviso va nella scena e su stderr; una voce che
// supera la sua scena senza toccarne un'altra non ne ha. out sono tutte le
// scene dello storyboard, nello stesso ordine. Le letture con ffprobe stanno
// tutte entro montage.ProbeTimeout.
func (chk *storyChecker) voiceOverlaps(out *storyCheck) {
	s := chk.s
	warn := func(i int, w string) {
		out.Scenes[i].Warnings = append(out.Scenes[i].Warnings, w)
		fmt.Fprintf(os.Stderr, "avviso: scena %s: %s\n", s.Scene[i].ID, w)
	}
	var voiced []int
	for i := range s.Scene {
		if s.Scene[i].Voce != "" {
			voiced = append(voiced, i)
		}
	}
	if len(voiced) == 0 {
		return
	}
	tool := montage.Tool{}
	if err := tool.Check(); err != nil {
		for _, i := range voiced {
			warn(i, "durata della voce non controllata: ffprobe non trovato")
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), montage.ProbeTimeout)
	defer cancel()
	n := len(s.Scene)
	voiceLen := make([]float64, n)
	for _, i := range voiced {
		voiceLen[i] = math.NaN()
		var info *montage.Info
		var err error
		if ctx.Err() == nil {
			info, err = tool.Probe(ctx, s.Path(s.Scene[i].Voce))
		}
		switch {
		case ctx.Err() != nil:
			warn(i, "durata della voce non controllata: letture oltre il tempo massimo di "+montage.ProbeTimeout.String())
		case err != nil || info.Audio == nil:
			warn(i, fmt.Sprintf("voce %s illeggibile come audio: giv story cut la rifiuterà", s.Scene[i].Voce))
		default:
			voiceLen[i] = info.Duration
		}
	}
	// inizio di ogni scena nel montaggio, starts[n] è la fine; NaN da una
	// scena di durata ignota in poi (nessun confronto)
	fade := 0.0
	if m := s.Montaggio; m != nil && m.Transizione == "dissolvenza" {
		fade = cutFadeSeconds
	}
	starts := make([]float64, n+1)
	for i := range s.Scene {
		l := out.Scenes[i].length
		if l == 0 && s.Scene[i].Tipo == story.Clip && ctx.Err() == nil {
			if info, err := tool.Probe(ctx, s.Path(s.Scene[i].File)); err == nil {
				l = retimedLength(&s.Scene[i], info.Duration)
			}
		}
		if l <= 0 {
			l = math.NaN()
		}
		starts[i+1] = starts[i] + l
		if i+1 < n {
			starts[i+1] -= fade
		}
	}
	secs := func(v float64) string { return strconv.FormatFloat(round3(v), 'f', -1, 64) }
	for k, i := range voiced {
		end := starts[i] + s.Scene[i].Ritardo() + voiceLen[i]
		if k+1 < len(voiced) {
			j := voiced[k+1]
			next := starts[j] + s.Scene[j].Ritardo()
			if d := end - next; d > cutVoiceSlack {
				warn(i, fmt.Sprintf("la voce si sovrappone di %s s a quella della scena %s: finisce a %s s del montaggio, quella di %s parte a %s s",
					secs(d), s.Scene[j].ID, secs(end), s.Scene[j].ID, secs(next)))
			}
		} else if d := end - starts[n]; d > cutVoiceSlack {
			warn(i, fmt.Sprintf("la voce supera di %s s la fine del montaggio e viene tagliata: finisce a %s s, il montaggio dura %s s",
				secs(d), secs(end), secs(starts[n])))
		}
	}
}

// titleWarnings controlla i titoli sulle durate previste (quando sono note):
// un titolo che comincia oltre la fine delle scene è un errore d'uso; uno che
// esce dalla sua scena o dalla fine (dove si ferma) o sta in basso come i
// sottotitoli è un avviso, in out.Warnings e su stderr.
func titleWarnings(s *story.Storyboard, path string, out *storyCheck) error {
	m := s.Montaggio
	if m == nil || len(m.Titoli) == 0 {
		return nil
	}
	warn := func(w string) {
		out.Warnings = append(out.Warnings, w)
		fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
	}
	secs := func(v float64) string { return strconv.FormatFloat(round3(v), 'f', -1, 64) }
	// inizio di ogni scena nel montaggio e fine delle scene; NaN da una scena
	// di durata ignota in poi (nessun confronto)
	fade := 0.0
	if m.Transizione == "dissolvenza" {
		fade = cutFadeSeconds
	}
	start, length := map[string]float64{}, map[string]float64{}
	total := 0.0
	for i, c := range out.Scenes {
		start[c.ID], length[c.ID] = total, c.length
		l := c.length
		if l <= 0 {
			l = math.NaN()
		}
		total += l
		if i+1 < len(out.Scenes) {
			total -= fade
		}
	}
	for i, t := range m.Titoli {
		d := 3.0
		if t.Durata != nil {
			d = *t.Durata
		}
		at := t.Da
		if t.Scena != "" {
			at += start[t.Scena]
		}
		if at >= total {
			return usageError("%s: titolo n. %d: comincia a %s s del montaggio, le scene finiscono a %s s", path, i+1, secs(at), secs(total))
		}
		if l := length[t.Scena]; t.Scena != "" && l > 0 && t.Da+d > l+cutVoiceSlack {
			warn(fmt.Sprintf("titolo n. %d: finisce a %s s della scena %s, che dura %s s", i+1, secs(t.Da+d), t.Scena, secs(l)))
		}
		if at+d > total+cutVoiceSlack {
			warn(fmt.Sprintf("titolo n. %d: finisce a %s s, le scene finiscono a %s s: si ferma lì", i+1, secs(at+d), secs(total)))
		}
		if pos := t.Posizione; m.Sottotitoli != "" && (pos == "" || strings.HasPrefix(pos, "basso")) {
			warn(fmt.Sprintf("titolo n. %d: in basso come i sottotitoli, possono sovrapporsi (posizione alto o centro)", i+1))
		}
	}
	if clash := logoTitleClash(m); len(clash) > 0 {
		var vert []string // formati montati non orizzontali (9:16, 4:5, 1:1)
		formats, _ := cutFormats(s, "")
		for _, f := range formats {
			if w, h, _, err := cutFormatFor(s, f); err == nil && h >= w {
				vert = append(vert, cutAspectOf(s, f))
			}
		}
		if len(vert) > 0 {
			for _, n := range clash {
				warn(logoTitleWarning(n, strings.Join(vert, ", ")))
			}
		}
	}
	return nil
}

// logoTitleClash sono i numeri (da 1) dei titoli in alto quando anche il logo
// è in alto (default alto-destra): nei formati non orizzontali il titolo va
// a capo o arriva sotto il logo, che lo copre. Nessuno spostamento, solo l'avviso.
func logoTitleClash(m *story.Montaggio) []int {
	if m == nil || m.Logo == nil || (m.Logo.Posizione != "" && !strings.HasPrefix(m.Logo.Posizione, "alto")) {
		return nil
	}
	var n []int
	for i, t := range m.Titoli {
		if strings.HasPrefix(t.Posizione, "alto") {
			n = append(n, i+1)
		}
	}
	return n
}

// logoTitleWarning è l'avviso di logoTitleClash per il titolo n nei formati non orizzontali formats.
func logoTitleWarning(n int, formats string) string {
	return fmt.Sprintf("titolo n. %d: in alto come il logo, in %s possono sovrapporsi (titolo al centro o in basso, o logo in basso)", n, formats)
}

// focusWarnings avvisa delle scene col fuoco quando il montaggio non ritaglia:
// il fuoco vale solo con montaggio.adatta ritaglio.
func focusWarnings(s *story.Storyboard, out *storyCheck) {
	if m := s.Montaggio; m != nil && m.Adatta == "ritaglio" {
		return
	}
	for i := range s.Scene {
		if s.Scene[i].Fuoco == nil {
			continue
		}
		w := "fuoco ignorato: vale solo con montaggio.adatta ritaglio"
		for j := range out.Scenes {
			if out.Scenes[j].ID == s.Scene[i].ID {
				out.Scenes[j].Warnings = append(out.Scenes[j].Warnings, w)
			}
		}
		fmt.Fprintf(os.Stderr, "avviso: scena %s: %s\n", s.Scene[i].ID, w)
	}
}

// sceneMissing descrive una scena video senza clip da montare: l'errore di
// ClipFile (che nomina la scena) e lo stato registrato da giv story run, se ce
// n'è uno; l'errore del provider resta nello stato, non si ristampa.
func sceneMissing(st *story.State, id string, err error) string {
	msg := err.Error()
	if sc := st.Scenes[id]; sc != nil && sc.Status != "" && sc.Status != story.Fatto {
		msg += " (stato: " + strings.ReplaceAll(sc.Status, "_", " ") + ")"
	}
	return msg
}
