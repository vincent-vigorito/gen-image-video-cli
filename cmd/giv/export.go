package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/output"
)

// giv export: un file per canale col profilo di internal/montage
// (ParseChannels, ExportBuild), in locale con ffmpeg, gratuito.

const exportHelp = `giv export — un file per canale (Instagram, TikTok, YouTube, LinkedIn) in locale con ffmpeg, gratuito

Uso:
  giv export --video <file> --for <canali> [--fit crop|pad|blur] [--focus <x>[,<y>]]
             [--loudness <lufs>|off] [--out <dir>] [--name <nome>] [--dry-run]

Flag:
  --for <canali>   obbligatorio: canali separati da virgole, ognuno col formato facoltativo
                   <canale>:<formato> (es. instagram,tiktok,youtube o instagram:4:5,youtube:9:16)
  --fit <modo>     come portare il video al formato: crop (default) taglia i bordi,
                   pad bande nere, blur sfondo sfocato (vedi giv post reframe)
  --focus <x>[,<y>]
                   solo con crop: il punto da tenere, da 0 a 1 con i decimali col punto
                   (default 0.5,0.5 = centro)
  --loudness <lufs>|off
                   volume finale in LUFS integrati (default -14, da -70 a -5; off: com'è),
                   normalizzato in due passate (loudnorm, picco -1 dBTP)

Profili (formato predefinito per primo; misure 9:16 1080x1920, 1:1 1080x1080,
4:5 1080x1350, 16:9 1920x1080; H.264 High, moov in testa, AAC 48 kHz stereo):
  instagram  9:16 (Reel), anche 4:5 e 1:1   30 fps, 3 s-15 min, audio 128 kbps
  tiktok     9:16, anche gli altri          30 fps, fino a 10 min, audio 128 kbps
  youtube    16:9; 9:16, 1:1, 4:5 (Shorts)  fps del video (altrimenti 30), fino a 15 min (Shorts 3 min)
  linkedin   16:9, anche gli altri          fps del video se 10-60 (altrimenti 30), 3 s-10 min
Una durata fuori dai limiti del canale esce con codice 2 prima di scrivere qualsiasi file.
Convenzioni, non nelle specifiche ufficiali: -14 LUFS, misure del feed di Instagram (4:5, 1:1),
formato di TikTok; il manifest le elenca in exports[].convention.

Nomi: <nome>-<canale>-<formato>.mp4 in --out, con <nome> = --name o il nome del video
(es. spot-instagram-9x16.mp4). Una riga a costo zero in giv-log.jsonl per file.
`

// exportItem è un file esportato nel manifest di giv export.
type exportItem struct {
	Channel      string   `json:"channel"`
	Aspect       string   `json:"aspect"`
	File         string   `json:"file,omitempty"`   // file scritto
	Output       string   `json:"output,omitempty"` // solo --dry-run: il file che verrebbe scritto
	Width        int      `json:"width"`
	Height       int      `json:"height"`
	FPS          float64  `json:"fps,omitempty"`
	DurationS    float64  `json:"duration_s,omitempty"`
	VideoCodec   string   `json:"video_codec,omitempty"`
	AudioCodec   string   `json:"audio_codec,omitempty"`
	LoudnessLUFS *float64 `json:"loudness_lufs,omitempty"`
	Convention   []string `json:"convention,omitempty"`
}

// exportManifest è l'uscita di giv export: i file di tutti i canali.
type exportManifest struct {
	DryRun   bool                 `json:"dry_run,omitempty"`
	Command  string               `json:"command"`
	Provider string               `json:"provider"`
	Model    string               `json:"model"`
	Inputs   map[string]inputFile `json:"inputs"`
	Params   map[string]any       `json:"params"`
	Files    []output.FileInfo    `json:"files"`
	Exports  []exportItem         `json:"exports"`
	CostUSD  float64              `json:"cost_usd"`
	Warnings []string             `json:"warnings,omitempty"`
}

func cmdExport(args []string) error {
	fs, lf := newLocalFlags("export", exportHelp)
	forFlag := fs.String("for", "", "canali: instagram, tiktok, youtube, linkedin (con :<formato>)")
	fit := fs.String("fit", montage.FitCrop, "crop, pad o blur")
	focusFlag := fs.String("focus", "", "solo con crop: <x>[,<y>] da 0 a 1")
	loudFlag := fs.String("loudness", "", "volume finale in LUFS (default -14) o off")
	if err := parseLocal(fs, args); err != nil {
		return err
	}
	profiles, err := montage.ParseChannels(*forFlag)
	if err != nil {
		return usageError("--for: %s", localErrMsg(err))
	}
	switch *fit {
	case montage.FitCrop, montage.FitPad, montage.FitBlur:
	default:
		return usageError("--fit %q: valori ammessi crop, pad, blur", *fit)
	}
	focus, err := montage.ParseFocus(*focusFlag)
	if err != nil {
		return usageError("--focus: %s", localErrMsg(err))
	}
	var warnings []string
	if strings.TrimSpace(*focusFlag) != "" && *fit != montage.FitCrop {
		warnings = append(warnings, "--focus vale solo con --fit crop: ignorato")
	}
	var loudness *float64
	loudDefault := false
	switch l := strings.TrimSpace(*loudFlag); l {
	case "off":
	case "":
		v := montage.DefaultLoudness
		loudness, loudDefault = &v, true
	default:
		v, err := strconv.ParseFloat(l, 64)
		if err != nil {
			return usageError("--loudness %q: un numero di LUFS (es. -14) oppure off", *loudFlag)
		}
		if err := montage.CheckLoudness(v); err != nil {
			return usageError("--loudness: %s", localErrMsg(err))
		}
		loudness = &v
	}
	src, err := lf.source()
	if err != nil {
		return err
	}
	for _, p := range profiles {
		if err := p.CheckDuration(src.Info.Duration); err != nil {
			return usageError("giv export: %s", localErrMsg(err))
		}
	}
	base := strings.TrimSuffix(lf.Name, ".mp4")
	if lf.Name == "" {
		base = strings.TrimSuffix(localName(src.Path, "export"), "-export") // nome del video senza estensione
	}
	for _, p := range profiles {
		if err := checkName(base + "-" + p.Tag()); err != nil {
			return err
		}
	}
	hasAudio := src.Info.Audio != nil
	if !hasAudio && loudness != nil {
		warnings = append(warnings, "il video non ha audio: esportato senza, volume non normalizzato")
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
	}
	params := map[string]any{"fit": *fit}
	var specs []string
	for _, p := range profiles {
		specs = append(specs, p.Spec())
	}
	params["for"] = specs
	if *fit == montage.FitCrop {
		params["focus"] = map[string]float64{"x": focus.X, "y": focus.Y}
	}
	if loudness != nil {
		params["loudness_lufs"] = *loudness
	} else {
		params["loudness_lufs"] = "off"
	}
	man := &exportManifest{DryRun: lf.DryRun, Command: "export", Provider: localProvider, Model: localModel,
		Params: params, Files: []output.FileInfo{}, Exports: []exportItem{}, Warnings: warnings}
	exports := make([]montage.Export, len(profiles))
	for i, p := range profiles {
		exports[i] = montage.Export{Profile: p, Fit: *fit, SrcFPS: src.Info.Video.FPS, Audio: hasAudio, Loudness: loudness}
		if *fit == montage.FitCrop {
			exports[i].Focus = &focus
		}
		if err := exports[i].Check(); err != nil {
			return usageError("giv export %s: %s", p.Spec(), localErrMsg(err))
		}
	}
	cache := &montage.ExportCache{}
	mutedWarned := false
	for i, p := range profiles {
		e := exports[i]
		conv := append([]string(nil), p.Convention...)
		if loudDefault && hasAudio {
			conv = append(conv, "volume -14 LUFS: convenzione dei social, nessuna specifica ufficiale")
		}
		item := exportItem{Channel: p.Channel, Aspect: p.Aspect.String(), Width: p.Width, Height: p.Height, Convention: conv}
		if hasAudio {
			item.LoudnessLUFS = loudness
		}
		one := *lf
		one.Name = base + "-" + p.Tag()
		m, err := execLocal(&one, localOp{
			Command: "export",
			Inputs:  map[string]string{"video": src.Path},
			Name:    one.Name,
			Ext:     ".mp4",
			Params:  map[string]any{"channel": p.Channel, "aspect": p.Aspect.String(), "fit": *fit, "loudness_lufs": params["loudness_lufs"]},
			Build:   montage.ExportBuild(src.Path, e, cache),
		})
		if err != nil {
			if len(man.Files) > 0 {
				fmt.Fprintf(os.Stderr, "giv export: già scritti %d file prima dell'errore (%s)\n", len(man.Files), man.Files[0].Path)
			}
			return err
		}
		if cache.Muted {
			item.LoudnessLUFS = nil
			if !mutedWarned {
				mutedWarned = true
				w := "audio muto: esportato senza normalizzare il volume"
				fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
				man.Warnings = append(man.Warnings, w)
			}
		}
		man.Inputs = m.Inputs
		man.Files = append(man.Files, m.Files...)
		if lf.DryRun {
			item.Output = m.Output
		} else if len(m.Files) > 0 {
			item.File = m.Files[0].Path
			item.Width, item.Height, item.FPS, item.DurationS = m.Width, m.Height, m.FPS, m.DurationS
			item.VideoCodec, item.AudioCodec = m.VideoCodec, m.AudioCodec
		}
		man.Exports = append(man.Exports, item)
	}
	return output.PrintJSON(man)
}
