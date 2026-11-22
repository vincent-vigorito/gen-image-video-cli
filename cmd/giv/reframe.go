package main

import (
	"math"
	"strings"

	"gen-image-video-cli/internal/montage"
)

// giv post reframe: cambio formato in locale (ritaglio col fuoco, bande nere o
// sfondo sfocato), calcolato da internal/montage (Aspect, Focus, Reframe).

func init() {
	registerPost(postOp{
		Name:  "reframe",
		Usage: "--aspect 9:16|1:1|4:5|16:9 [--fit crop|pad|blur] [--focus <x>[,<y>]]: cambio formato",
		Run:   cmdPostReframe,
	})
}

const reframeHelp = `giv post reframe — cambio formato di un video in locale con ffmpeg, gratuito

Uso:
  giv post reframe --video <file> --aspect 9:16|1:1|4:5|16:9 [--fit crop|pad|blur] [--focus <x>[,<y>]]
                   [--out <dir>] [--name <nome>] [--dry-run]

Flag:
  --aspect <r>     formato d'uscita, obbligatorio: 9:16, 1:1, 4:5 o 16:9. Il lato corto resta
                   quello del video (da un 1920x1080: 1080x1920, 1080x1080, 1080x1350, 1920x1080)
  --fit <modo>     crop (default): riempie il quadro tagliando i bordi;
                   pad: video intero con bande nere; blur: video intero sopra lo stesso video sfocato
  --focus <x>[,<y>]
                   solo con crop: il punto da tenere, da 0 a 1 con i decimali col punto
                   (0 = bordo sinistro/alto, 1 = destro/basso; default 0.5,0.5 = centro; senza y: 0.5).
                   L'inquadratura è fissa: un soggetto che si sposta può uscire dal quadro

Nome di default: <nome del video>-<formato> (es. spot-9x16.mp4). Audio della prima traccia
ricodificato in AAC; il video in H.264.
`

func cmdPostReframe(args []string) error {
	fs, lf := newLocalFlags("post reframe", reframeHelp)
	aspectFlag := fs.String("aspect", "", "formato d'uscita: 9:16, 1:1, 4:5, 16:9")
	fit := fs.String("fit", montage.FitCrop, "crop, pad o blur")
	focusFlag := fs.String("focus", "", "solo con crop: <x>[,<y>] da 0 a 1")
	if err := parseLocal(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*aspectFlag) == "" {
		return usageError("giv post reframe: manca --aspect (9:16, 1:1, 4:5 o 16:9)")
	}
	aspect, err := montage.ParseAspect(*aspectFlag)
	if err != nil {
		return usageError("--aspect: %s", localErrMsg(err))
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
	src, err := lf.source()
	if err != nil {
		return err
	}
	v := src.Info.Video
	w, h, err := aspect.Size(v.Width, v.Height)
	if err != nil {
		return usageError("--video %s: %s", src.Path, localErrMsg(err))
	}
	r := montage.Reframe{Width: w, Height: h, Fit: *fit}
	params := map[string]any{"aspect": aspect.String(), "fit": *fit, "width": w, "height": h}
	if *fit == montage.FitCrop {
		r.Focus = &focus
		params["focus"] = map[string]float64{"x": focus.X, "y": focus.Y}
	}
	if err := r.Check(); err != nil {
		return usageError("giv post reframe: %s", localErrMsg(err))
	}
	if v.Height > 0 && math.Abs(float64(v.Width)/float64(v.Height)-float64(aspect.W)/float64(aspect.H)) < 0.01*float64(aspect.W)/float64(aspect.H) {
		warnings = append(warnings, "il video è già "+aspect.String()+": cambia solo la codifica")
	}
	return runLocal(lf, localOp{
		Command:  "post reframe",
		Inputs:   map[string]string{"video": src.Path},
		Name:     localName(src.Path, aspect.Tag()),
		Ext:      ".mp4",
		Params:   params,
		Warnings: warnings,
		Build: func(work *montage.Work, staged string) error {
			a, err := montage.ReframeArgs(src.Path, staged, r)
			if err != nil {
				return err
			}
			return work.FFmpeg(a)
		},
	})
}
