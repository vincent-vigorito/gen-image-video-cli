package main

import (
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/story"
)

// giv sheet, giv frame e giv gif: strumenti di servizio in locale con ffmpeg
// (foglio di contatto, fotogramma, GIF animata), con i flag comuni e le
// regole dei comandi locali (local.go). Nessuna chiamata a provider, nessuna
// spesa.

const sheetHelp = `giv sheet — foglio di contatto (JPEG) di un video, di una cartella o di uno storyboard,
in locale con ffmpeg: nessuna chiamata a provider, nessuna spesa

Uso:
  giv sheet <file|cartella|storyboard.json> [--every <s> | --frames <n>] [--cols <n>] [--labels]
            [--out <dir>] [--name <nome>] [--dry-run]

Flag:
  --every <s>      un fotogramma ogni s secondi, da 0 (più di 0, al più 3600)
  --frames <n>     n fotogrammi per video a intervalli uguali (1-100); senza --every né
                   --frames, uno per colonna
  --cols <n>       colonne della griglia (1-12, default 4)
  --labels         un'etichetta su ogni riquadro: l'istante; con una cartella anche il nome
                   del file; con uno storyboard l'id della scena e la ripresa

Con una cartella: una riga per file (video .mp4 .m4v .mov .mkv .webm .avi .ts, immagini
.png .jpg .jpeg .webp; solo file normali, senza seguire link), in ordine di nome.
Con uno storyboard (.json): una riga per scena, col fotogramma chiave se c'è e i
fotogrammi della clip in uso (giv story run) o del file della scena; le scene senza
clip né fotogramma chiave si saltano con un avviso. Il file si può indicare anche con
--video. Uscita: <nome>-foglio.jpg in --out.
`

const frameHelp = `giv frame — un fotogramma del video in PNG (con --width la miniatura), in locale con ffmpeg:
nessuna chiamata a provider, nessuna spesa

Uso:
  giv frame --video <file> --at <s> | --last [--width <px>] [--out <dir>] [--name <nome>] [--dry-run]

Flag:
  --at <s>         istante del fotogramma, in secondi dall'inizio (anche decimali: 1.5), entro la durata
  --last           l'ultimo fotogramma (per esempio il primo di una scena che continua)
  --width <px>     larghezza in pixel (16-7680), altezza in proporzione: la miniatura
Uno solo fra --at e --last. Uscita: <nome del video>-fotogramma.png in --out.
`

const gifHelp = `giv gif — GIF animata da un tratto del video (palette calcolata in due passate), in locale
con ffmpeg: nessuna chiamata a provider, nessuna spesa

Uso:
  giv gif --video <file> [--start <s>] [--end <s>] [--width <px>] [--fps <n>] [--out <dir>] [--name <nome>] [--dry-run]

Flag:
  --start <s>      inizio del tratto, in secondi (default 0)
  --end <s>        fine del tratto, in secondi (default: la fine del video); al più 60 s di GIF
  --width <px>     larghezza in pixel (16-1920, default 480; mai oltre quella del video),
                   altezza in proporzione
  --fps <n>        fotogrammi al secondo (1-30, default 12)
Uscita: <nome del video>-gif.gif in --out, ripetuta all'infinito.
`

// flagsSet sono i flag indicati sulla riga di comando.
func flagsSet(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// finiteNum: numero finito (i flag numerici accettano anche NaN e Inf).
func finiteNum(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// secondsLabel è un istante per le etichette: un decimale, virgola italiana.
func secondsLabel(t float64) string {
	return strings.Replace(strconv.FormatFloat(math.Round(t*10)/10, 'f', -1, 64), ".", ",", 1) + " s"
}

// cmdFrame è giv frame.
func cmdFrame(args []string) error {
	fs, lf := newLocalFlags("frame", frameHelp)
	at := fs.Float64("at", 0, "istante del fotogramma, secondi")
	last := fs.Bool("last", false, "l'ultimo fotogramma")
	width := fs.Int("width", 0, "larghezza in pixel")
	if err := parseLocal(fs, args); err != nil {
		return err
	}
	set := flagsSet(fs)
	if set["at"] == *last {
		return usageError("giv frame: serve --at <s> oppure --last (uno solo)")
	}
	if set["at"] && (!finiteNum(*at) || *at < 0) {
		return usageError("--at %v: secondi dall'inizio, da 0", *at)
	}
	if set["width"] && (*width < 16 || *width > 7680) {
		return usageError("--width %d: da 16 a 7680 pixel", *width)
	}
	src, err := lf.source()
	if err != nil {
		return err
	}
	if d := src.Info.Duration; set["at"] && d > 0 && *at >= d {
		return usageError("--at %s: oltre la fine del video (%s s; per l'ultimo fotogramma: --last)",
			strconv.FormatFloat(*at, 'f', -1, 64), strconv.FormatFloat(round3(d), 'f', -1, 64))
	}
	f := montage.Frame{Video: src.Path, At: *at, Last: *last, Width: *width}
	if err := f.Check(); err != nil {
		return usageError("giv frame: %s", localErrMsg(err))
	}
	params := map[string]any{}
	if *last {
		params["last"] = true
	} else {
		params["at_s"] = *at
	}
	if *width > 0 {
		params["width"] = *width
	}
	return runLocal(lf, localOp{Command: "frame", Inputs: map[string]string{"video": src.Path},
		Name: localName(src.Path, "fotogramma"), Ext: ".png", Params: params, Build: f.Build})
}

// cmdGIF è giv gif.
func cmdGIF(args []string) error {
	fs, lf := newLocalFlags("gif", gifHelp)
	start := fs.Float64("start", 0, "inizio del tratto, secondi")
	end := fs.Float64("end", 0, "fine del tratto, secondi")
	width := fs.Int("width", 480, "larghezza in pixel")
	fps := fs.Int("fps", 12, "fotogrammi al secondo")
	if err := parseLocal(fs, args); err != nil {
		return err
	}
	set := flagsSet(fs)
	if !finiteNum(*start) || *start < 0 {
		return usageError("--start %v: secondi dall'inizio, da 0", *start)
	}
	if set["end"] && (!finiteNum(*end) || *end <= *start) {
		return usageError("--end %v: secondi, oltre --start (%v)", *end, *start)
	}
	if *width < 16 || *width > montage.MaxGIFWidth {
		return usageError("--width %d: da 16 a %d pixel", *width, montage.MaxGIFWidth)
	}
	if *fps < 1 || *fps > montage.MaxGIFFPS {
		return usageError("--fps %d: da 1 a %d", *fps, montage.MaxGIFFPS)
	}
	src, err := lf.source()
	if err != nil {
		return err
	}
	d := src.Info.Duration
	sec := func(v float64) string { return strconv.FormatFloat(round3(v), 'f', -1, 64) }
	g := montage.GIF{Video: src.Path, Start: *start, End: *end, Width: *width, FPS: *fps}
	if !set["end"] {
		if d <= 0 {
			return usageError("--video %s: durata non misurata, indica --end", src.Path)
		}
		g.End = d
	}
	if d > 0 && *start >= d {
		return usageError("--start %s: oltre la fine del video (%s s)", sec(*start), sec(d))
	}
	if d > 0 && g.End > d+0.001 {
		return usageError("--end %s: oltre la fine del video (%s s)", sec(g.End), sec(d))
	}
	if g.End-g.Start > montage.MaxGIFSeconds {
		return usageError("giv gif: tratto di %s s, una GIF dura al più %d s: indica --start e --end",
			sec(g.End-g.Start), montage.MaxGIFSeconds)
	}
	var warnings []string
	if v := src.Info.Video; v.Width > 0 && g.Width > v.Width {
		warnings = append(warnings, fmt.Sprintf("--width %d oltre la larghezza del video: resta %d", g.Width, v.Width))
		g.Width = v.Width
	}
	if err := g.Check(); err != nil {
		return usageError("giv gif: %s", localErrMsg(err))
	}
	params := map[string]any{"start_s": round3(g.Start), "end_s": round3(g.End), "width": g.Width, "fps": g.FPS}
	return runLocal(lf, localOp{Command: "gif", Inputs: map[string]string{"video": src.Path},
		Name: localName(src.Path, "gif"), Ext: ".gif", Params: params, Warnings: warnings, Build: g.Build})
}

// sheetOptions sono le scelte del foglio di contatto.
type sheetOptions struct {
	Every  float64 // un fotogramma ogni Every secondi da 0; 0: Frames
	Frames int     // fotogrammi per video a intervalli uguali; 0: uno per colonna libera
	Cols   int     // colonne della griglia
	Labels bool    // etichette sui riquadri
}

// sheetPlan è un foglio di contatto pronto da comporre: i riquadri, i file
// letti per ruolo (localOp.Inputs), gli avvisi e, con uno storyboard, le
// scene mostrate.
type sheetPlan struct {
	Sheet    montage.Sheet
	Inputs   map[string]string
	Warnings []string
	Scenes   []string
}

func newSheetPlan(o sheetOptions) *sheetPlan {
	return &sheetPlan{Sheet: montage.Sheet{Cols: o.Cols}, Inputs: map[string]string{}}
}

// sheetImage: il file misurato è un'immagine ferma (non un video).
func sheetImage(info *montage.Info) bool {
	for _, f := range strings.Split(info.Format, ",") {
		switch f {
		case "image2", "png_pipe", "jpeg_pipe", "webp_pipe":
			return true
		}
	}
	return false
}

// add aggiunge al foglio i riquadri di un file misurato: un'immagine è un
// riquadro solo, un video i fotogrammi scelti da o (senza --every né
// --frames uno per ogni colonna libera: used sono i posti già presi nella
// riga). newRow fa cominciare una riga nuova; label è l'inizio delle
// etichette (con --labels). La misura dei riquadri viene dal primo file.
func (p *sheetPlan) add(path string, info *montage.Info, o sheetOptions, label string, newRow bool, used int) error {
	if info.Video == nil {
		return errors.New("nessuna traccia video")
	}
	if p.Sheet.CellWidth == 0 {
		p.Sheet.CellWidth, p.Sheet.CellHeight = montage.SheetCellSize(info.Video.Width, info.Video.Height)
	}
	text := func(parts ...string) string {
		if !o.Labels {
			return ""
		}
		var nonEmpty []string
		for _, s := range parts {
			if s != "" {
				nonEmpty = append(nonEmpty, s)
			}
		}
		return strings.Join(nonEmpty, " · ")
	}
	if sheetImage(info) {
		p.Sheet.Cells = append(p.Sheet.Cells, montage.SheetCell{Path: path, Image: true, Label: text(label), NewRow: newRow})
		return nil
	}
	frames := o.Frames
	if o.Every == 0 && frames == 0 {
		frames = max(o.Cols-used, 1)
	}
	ts, err := montage.SheetTimes(info.Duration, info.Video.FPS, o.Every, frames)
	if err != nil {
		return errors.New(localErrMsg(err))
	}
	for i, t := range ts {
		p.Sheet.Cells = append(p.Sheet.Cells, montage.SheetCell{Path: path, At: t,
			Label: text(label, secondsLabel(t)), NewRow: newRow && i == 0})
	}
	return nil
}

// skip registra un file saltato; err non d'uso (ffprobe assente, scadenza)
// ferma tutto.
func (p *sheetPlan) skip(what string, err error) error {
	if code, _ := exitCode(err); code != exitUsage {
		return err
	}
	p.Warnings = append(p.Warnings, fmt.Sprintf("%s saltato: %v", what, err))
	return nil
}

// fileSheet è il foglio di un file: i fotogrammi del video in righe di --cols.
func fileSheet(path string, o sheetOptions) (*sheetPlan, error) {
	info, err := localMedia("giv sheet", path)
	if err != nil {
		return nil, err
	}
	p := newSheetPlan(o)
	if err := p.add(path, info, o, "", true, 0); err != nil {
		return nil, usageError("giv sheet %s: %v", path, err)
	}
	role := "video"
	if sheetImage(info) {
		role = "immagine"
	}
	p.Inputs[role] = path
	return p, nil
}

// Estensioni lette da giv sheet in una cartella.
var (
	sheetVideoExt = map[string]bool{".mp4": true, ".m4v": true, ".mov": true, ".mkv": true, ".webm": true, ".avi": true, ".ts": true}
	sheetImageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}
)

// maxSheetFiles è il numero di file letti da una cartella.
const maxSheetFiles = 100

// dirSheet è il foglio di una cartella: una riga per file, in ordine di
// nome; solo file normali (link e cartelle non si seguono), i file
// illeggibili si saltano con un avviso.
func dirSheet(dir string, o sheetOptions) (*sheetPlan, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, usageError("giv sheet %s: cartella illeggibile: %v", dir, err)
	}
	p := newSheetPlan(o)
	var names []string
	links := 0
	for _, e := range entries {
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if strings.HasPrefix(name, ".") || !sheetVideoExt[ext] && !sheetImageExt[ext] {
			continue
		}
		if !e.Type().IsRegular() {
			links++
			continue
		}
		names = append(names, name)
	}
	if links > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%d link o cartelle saltati: si leggono solo i file normali", links))
	}
	if len(names) == 0 {
		return nil, usageError("giv sheet %s: nessun video (.mp4 .m4v .mov .mkv .webm .avi .ts) né immagine (.png .jpg .jpeg .webp)", dir)
	}
	if len(names) > maxSheetFiles {
		return nil, usageError("giv sheet %s: %d file, al più %d: indica una cartella con meno file", dir, len(names), maxSheetFiles)
	}
	for i, name := range names {
		path := filepath.Join(dir, name)
		info, err := localMedia("file", path)
		if err == nil {
			label := name
			if r := []rune(label); len(r) > 40 {
				label = string(r[:39]) + "…"
			}
			if err = p.add(path, info, o, label, true, 0); err != nil {
				err = usageError("%v", err)
			}
		}
		if err != nil {
			if err := p.skip(name, err); err != nil {
				return nil, err
			}
			continue
		}
		p.Inputs["file-"+strconv.Itoa(i+1)] = path
	}
	if len(p.Sheet.Cells) == 0 {
		return nil, usageError("giv sheet %s: nessun file leggibile:\n  %s", dir, strings.Join(p.Warnings, "\n  "))
	}
	return p, nil
}

// storySheet è il foglio di contatto di uno storyboard già letto e
// controllato (path è il suo file): una riga per scena, nell'ordine; il
// fotogramma chiave registrato da giv story run, se c'è, e i fotogrammi della
// clip in uso (scene video e avatar) o del file della scena (clip,
// immagine), con etichette id e ripresa. Le scene senza clip né fotogramma
// chiave e i file illeggibili si saltano con un avviso; nessuna scena da
// mostrare è un errore d'uso. Tutti i file restano nella cartella dello
// storyboard (Storyboard.CheckFile, State.ClipFile). Serve a giv sheet e a
// giv story run e cut, che scrivono il foglio da soli.
func storySheet(s *story.Storyboard, path string, o sheetOptions) (*sheetPlan, error) {
	p := newSheetPlan(o)
	var st *story.State
	for i := range s.Scene {
		sc := &s.Scene[i]
		where := "scena " + sc.ID
		switch sc.Tipo {
		case story.Clip, story.Immagine:
			file, err := s.CheckFile(sc.File)
			var info *montage.Info
			if err == nil {
				info, err = localMedia(where, file)
			}
			if err == nil {
				if err = p.add(file, info, o, sc.ID, true, 0); err != nil {
					err = usageError("%v", err)
				}
			}
			if err != nil {
				if err := p.skip(where, err); err != nil {
					return nil, err
				}
				continue
			}
			p.Inputs[sc.Tipo+"-"+sc.ID] = file
		case story.Video, story.Avatar:
			if st == nil {
				var err error
				if st, err = story.LoadState(story.StatePath(path)); err != nil {
					return nil, usageError("%s: %v", path, err)
				}
			}
			ss := st.Scenes[sc.ID]
			newRow, used := true, 0
			if ss != nil && ss.Keyframe != nil {
				kf, err := s.CheckFile(ss.Keyframe.File)
				var info *montage.Info
				if err == nil {
					info, err = localMedia(where+": fotogramma chiave", kf)
				}
				if err == nil {
					if err = p.add(kf, info, o, sc.ID+" · fotogramma chiave", true, 0); err != nil {
						err = usageError("%v", err)
					}
				}
				if err != nil {
					if err := p.skip(where+": fotogramma chiave", err); err != nil {
						return nil, err
					}
				} else {
					p.Inputs["fotogramma-"+sc.ID] = kf
					newRow, used = false, 1
				}
			}
			clip, err := st.ClipFile(s, sc.ID)
			if err != nil {
				if errors.Is(err, story.ErrNotDone) && !newRow {
					p.Warnings = append(p.Warnings, where+": solo il fotogramma chiave, clip non ancora generata")
				} else {
					p.Warnings = append(p.Warnings, sceneMissing(st, sc.ID, err)+": saltata")
				}
				if !newRow {
					p.Scenes = append(p.Scenes, sc.ID)
				}
				continue
			}
			info, err := localMedia(where, clip)
			if err == nil {
				label := sc.ID + " · ripresa " + strconv.Itoa(ss.ClipTakes())
				if err = p.add(clip, info, o, label, newRow, used); err != nil {
					err = usageError("%v", err)
				}
			}
			if err != nil {
				if err := p.skip(where+": clip", err); err != nil {
					return nil, err
				}
				if !newRow {
					p.Scenes = append(p.Scenes, sc.ID)
				}
				continue
			}
			p.Inputs["clip-"+sc.ID] = clip
		default:
			continue
		}
		p.Scenes = append(p.Scenes, sc.ID)
	}
	if len(p.Sheet.Cells) == 0 {
		msg := "nessuna scena da mostrare (genera le scene con giv story run)"
		if len(p.Warnings) > 0 {
			msg += ":\n  " + strings.Join(p.Warnings, "\n  ")
		}
		return nil, usageError("%s: %s", path, msg)
	}
	p.Inputs["storyboard"] = path
	return p, nil
}

// sheetTarget legge gli argomenti di giv sheet: i flag prima o dopo il file
// (o la cartella, o lo storyboard), che si può dare anche con --video.
func sheetTarget(fs *flag.FlagSet, lf *localFlags, args []string) (string, error) {
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
			return "", usageError("giv sheet: un solo file, cartella o storyboard (in più: %q)", fs.Arg(0))
		}
	}
	if target != "" && lf.Video != "" {
		return "", usageError("giv sheet: il file va indicato una volta sola, come argomento o con --video")
	}
	if target == "" {
		target = lf.Video
	}
	if strings.TrimSpace(target) == "" {
		return "", usageError("giv sheet: manca il file, la cartella o lo storyboard (giv sheet <file|cartella|storyboard.json>)")
	}
	return target, nil
}

// cmdSheet è giv sheet.
func cmdSheet(args []string) error {
	fs, lf := newLocalFlags("sheet", sheetHelp)
	every := fs.Float64("every", 0, "un fotogramma ogni s secondi")
	frames := fs.Int("frames", 0, "fotogrammi per video")
	cols := fs.Int("cols", 4, "colonne della griglia")
	labels := fs.Bool("labels", false, "etichette sui riquadri")
	target, err := sheetTarget(fs, lf, args)
	if err != nil {
		return err
	}
	set := flagsSet(fs)
	switch {
	case set["every"] && set["frames"]:
		return usageError("giv sheet: --every oppure --frames, non entrambi")
	case set["every"] && (!finiteNum(*every) || *every <= 0 || *every > 3600):
		return usageError("--every %v: secondi fra un fotogramma e l'altro, più di 0 e al più 3600", *every)
	case set["frames"] && (*frames < 1 || *frames > montage.MaxSheetFrames):
		return usageError("--frames %d: da 1 a %d", *frames, montage.MaxSheetFrames)
	case *cols < 1 || *cols > montage.MaxSheetCols:
		return usageError("--cols %d: da 1 a %d", *cols, montage.MaxSheetCols)
	}
	o := sheetOptions{Every: *every, Frames: *frames, Cols: *cols, Labels: *labels}
	var p *sheetPlan
	var mode, name string
	st, err := os.Stat(target)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return usageError("giv sheet %s: non trovato", target)
	case err != nil:
		return usageError("giv sheet %s: %v", target, err)
	case st.IsDir():
		mode = "cartella"
		abs, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		name = "foglio"
		if b := filepath.Base(abs); b != "" && b != string(filepath.Separator) && !strings.HasPrefix(b, ".") {
			name = b + "-foglio"
		}
		p, err = dirSheet(target, o)
		if err != nil {
			return err
		}
	case strings.EqualFold(filepath.Ext(target), ".json"):
		mode, name = "storyboard", localName(target, "foglio")
		s, err := loadStory(target)
		if err != nil {
			return err
		}
		if p, err = storySheet(s, target, o); err != nil {
			return err
		}
	default:
		mode, name = "file", localName(target, "foglio")
		if p, err = fileSheet(target, o); err != nil {
			return err
		}
	}
	sh := p.Sheet
	if err := sh.Check(); err != nil {
		return usageError("giv sheet: %s", localErrMsg(err))
	}
	w, h := sh.Size()
	params := map[string]any{"mode": mode, "cols": sh.Cols, "labels": o.Labels, "cells": len(sh.Cells),
		"rows": sh.Rows(), "cell_width": sh.CellWidth, "cell_height": sh.CellHeight, "sheet_width": w, "sheet_height": h}
	switch {
	case o.Every > 0:
		params["every_s"] = o.Every
	case o.Frames > 0:
		params["frames"] = o.Frames
	}
	if mode == "storyboard" {
		params["scenes"] = p.Scenes
	}
	return runLocal(lf, localOp{Command: "sheet", Inputs: p.Inputs, Name: name, Ext: ".jpg",
		Params: params, Warnings: p.Warnings, Build: sh.Build})
}
