package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"gen-image-video-cli/internal/montage"
)

// giv post overlay: logo, titolo e cartello finale su un video, in locale con
// ffmpeg (montage.Overlay). I testi passano solo dal file .ass generato.

func init() {
	registerPost(postOp{Name: "overlay", Usage: "logo, titolo e cartello finale (--logo, --title, --end-card)", Run: cmdPostOverlay})
}

const overlayHelp = `giv post overlay — logo, titolo e cartello finale su un video, in locale con ffmpeg

Uso:
  giv post overlay --video <file> [--logo <png> …] [--title "<testo>" …] [--end-card <immagine> …]

Logo sul video:
  --logo <file>            immagine .png, .jpg o .webp (la trasparenza resta)
  --logo-pos <pos>         posizione dentro i margini sicuri (default alto-destra)
  --logo-width <f>         larghezza in frazione di quella del video, da 0 a 1 (default 0.12)
  --logo-opacity <f>       opacità da 0 a 1 (default 1)
  --logo-from <s>          da quando si vede, secondi (default 0)
  --logo-to <s>            fino a quando, secondi (default: fino alla fine del video)
Titolo:
  --title "<testo>"        testo reso alla lettera; un a capo va a riga nuova
  --from <s>               inizio, secondi (default 0)
  --duration <s>           durata, secondi (default 3), dentro la durata del video
  --pos <pos>              posizione dentro i margini sicuri (default basso)
Cartello finale, accodato dopo il video (sotto il cartello l'audio è muto):
  --end-card <file>        immagine di sfondo .png, .jpg o .webp, contenuta nel quadro
  --end-color <#RRGGBB>    colore dello sfondo (default #000000)
  --end-logo <file>        logo del cartello: al centro, o sopra il testo
  --end-text "<testo>"     testo al centro
  --end-subtext "<testo>"  testo più piccolo in basso
  --end-duration <s>       durata, secondi (default 3, al più 60)
Testi (titolo e cartello):
  --font <file>            font .ttf, .otf o .ttc (default DejaVu Sans, se installato)
  --color <#RRGGBB>        colore dei testi (default #FFFFFF), col bordo nero

Posizioni: alto-sinistra, alto, alto-destra, sinistra, centro, destra, basso-sinistra,
basso, basso-destra. Margini sicuri: 14% in alto, 35% in basso e 6% ai lati per i 9:16
(guida annunci Meta), 5% per lato per gli altri formati. Serve almeno --logo, --title o
un flag del cartello (--end-card, --end-color, --end-logo, --end-text, --end-subtext).
`

// cmdPostOverlay è giv post overlay.
func cmdPostOverlay(args []string) error {
	fs, lf := newLocalFlags("post overlay", overlayHelp)
	var (
		logo, logoPos                            string
		logoWidth, logoOpacity, logoFrom, logoTo float64
		title, pos                               string
		from, dur                                float64
		endCard, endColor, endLogo               string
		endText, endSub                          string
		endDur                                   float64
		font, color                              string
	)
	fs.StringVar(&logo, "logo", "", "logo")
	fs.StringVar(&logoPos, "logo-pos", string(montage.TopRight), "posizione del logo")
	fs.Float64Var(&logoWidth, "logo-width", montage.DefaultLogoWidth, "larghezza del logo")
	fs.Float64Var(&logoOpacity, "logo-opacity", 1, "opacità del logo")
	fs.Float64Var(&logoFrom, "logo-from", 0, "inizio del logo")
	fs.Float64Var(&logoTo, "logo-to", 0, "fine del logo")
	fs.StringVar(&title, "title", "", "titolo")
	fs.Float64Var(&from, "from", 0, "inizio del titolo")
	fs.Float64Var(&dur, "duration", montage.DefaultTitleSeconds, "durata del titolo")
	fs.StringVar(&pos, "pos", string(montage.Bottom), "posizione del titolo")
	fs.StringVar(&endCard, "end-card", "", "immagine del cartello")
	fs.StringVar(&endColor, "end-color", "#000000", "colore del cartello")
	fs.StringVar(&endLogo, "end-logo", "", "logo del cartello")
	fs.StringVar(&endText, "end-text", "", "testo del cartello")
	fs.StringVar(&endSub, "end-subtext", "", "sottotesto del cartello")
	fs.Float64Var(&endDur, "end-duration", montage.DefaultCardSeconds, "durata del cartello")
	fs.StringVar(&font, "font", "", "font dei testi")
	fs.StringVar(&color, "color", "#FFFFFF", "colore dei testi")
	if err := parseLocal(fs, args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	// Flag vuoti: un valore vuoto vale mancante, e allora è un errore.
	for _, f := range []struct{ name, val string }{
		{"logo", logo}, {"title", title}, {"end-card", endCard}, {"end-logo", endLogo},
		{"end-text", endText}, {"end-subtext", endSub}, {"font", font}, {"end-color", endColor}, {"color", color},
		{"logo-pos", logoPos}, {"pos", pos},
	} {
		if set[f.name] && strings.TrimSpace(f.val) == "" {
			return usageError("--%s vuoto", f.name)
		}
	}
	hasLogo, hasTitle := logo != "", title != ""
	hasCard := endCard != "" || endLogo != "" || endText != "" || endSub != "" || set["end-color"]
	hasText := hasTitle || endText != "" || endSub != ""
	for _, dep := range []struct {
		ok    bool
		need  string
		flags []string
	}{
		{hasLogo, "--logo", []string{"logo-pos", "logo-width", "logo-opacity", "logo-from", "logo-to"}},
		{hasTitle, "--title", []string{"from", "duration", "pos"}},
		{hasCard, "un flag del cartello (--end-card, --end-color, --end-logo, --end-text, --end-subtext)", []string{"end-duration"}},
		{hasText, "un testo (--title, --end-text o --end-subtext)", []string{"font", "color"}},
	} {
		for _, f := range dep.flags {
			if set[f] && !dep.ok {
				return usageError("--%s senza %s", f, dep.need)
			}
		}
	}
	if !hasLogo && !hasTitle && !hasCard {
		return usageError("giv post overlay: niente da posare: indica --logo, --title o un flag del cartello (--end-card, --end-text…); aiuto: giv post overlay --help")
	}

	// Valori che non dipendono dal video, prima di lanciare ffprobe.
	var lpos, tpos montage.Position
	var err error
	if hasLogo {
		if lpos, err = montage.ParsePosition(logoPos); err != nil {
			return usageError("--logo-pos: %v", err)
		}
		if err := checkFrac("--logo-width", logoWidth); err != nil {
			return err
		}
		if err := checkFrac("--logo-opacity", logoOpacity); err != nil {
			return err
		}
		if err := checkSeconds("--logo-from", logoFrom, true); err != nil {
			return err
		}
		if err := checkSeconds("--logo-to", logoTo, true); err != nil {
			return err
		}
		if logoTo > 0 && logoTo <= logoFrom {
			return usageError("--logo-to %v: dopo --logo-from (%v); senza --logo-to il logo resta fino alla fine", logoTo, logoFrom)
		}
	}
	if hasTitle {
		if tpos, err = montage.ParsePosition(pos); err != nil {
			return usageError("--pos: %v", err)
		}
		if err := checkSeconds("--from", from, true); err != nil {
			return err
		}
		if err := checkSeconds("--duration", dur, false); err != nil {
			return err
		}
	}
	if hasCard {
		if err := checkSeconds("--end-duration", endDur, false); err != nil {
			return err
		}
		if endDur > 60 {
			return usageError("--end-duration %v: al più 60 secondi", endDur)
		}
	}
	for _, t := range []struct{ name, val string }{{"title", title}, {"end-text", endText}, {"end-subtext", endSub}} {
		if len(t.val) > 2000 {
			return usageError("--%s: al più 2000 byte", t.name)
		}
	}
	var bg, fg montage.Color
	if hasCard {
		if bg, err = montage.ParseColor(endColor); err != nil {
			return usageError("--end-color: %v", err)
		}
	}
	if hasText {
		if fg, err = montage.ParseColor(color); err != nil {
			return usageError("--color: %v", err)
		}
	}

	src, err := lf.source()
	if err != nil {
		return err
	}
	v := src.Info.Video
	o := montage.Overlay{Video: src.Path, Width: v.Width, Height: v.Height, FPS: v.FPS,
		Duration: src.Info.Duration, Audio: src.Info.Audio != nil, Margins: montage.SafeMargins(v.Width, v.Height)}
	inputs := map[string]string{"video": src.Path}
	params := map[string]any{"margins": map[string]float64{
		"top": o.Margins.Top, "bottom": o.Margins.Bottom, "left": o.Margins.Left, "right": o.Margins.Right}}

	style := montage.DefaultTextStyle(v.Height)
	cardStyle := montage.CardTextStyle(v.Height)
	if hasText {
		style.Color, cardStyle.Color = fg, fg
		params["color"] = hexColor(fg)
	}
	if font != "" {
		family, err := montage.FontFamily(font)
		if err != nil {
			return usageError("--font %s: %s", font, localErrMsg(err))
		}
		style.Font, cardStyle.Font = family, family
		o.Fonts = []string{font}
		inputs["font"] = font
		params["font"] = family
	}
	if hasLogo {
		l, err := overlayImage("--logo", logo)
		if err != nil {
			return err
		}
		l.Pos, l.Width, l.Opacity, l.From, l.To = lpos, logoWidth, logoOpacity, logoFrom, logoTo
		o.Logo = l
		inputs["logo"] = logo
	}
	if hasTitle {
		o.Titles = []montage.Text{{Text: title, Start: from, End: from + dur, Pos: tpos, Style: style, Fade: montage.TextFade(dur)}}
		params["title"] = map[string]any{"text": title, "from": from, "duration": dur, "pos": string(tpos)}
	}
	if hasCard {
		c := &montage.EndCard{Duration: endDur, Color: bg, Text: endText, Subtext: endSub, Style: cardStyle}
		cp := map[string]any{"start": round3(o.Duration), "duration": endDur, "color": hexColor(bg)}
		if endCard != "" {
			if _, err := overlayImage("--end-card", endCard); err != nil {
				return err
			}
			c.Image = endCard
			inputs["end_card"] = endCard
		}
		if endLogo != "" {
			l, err := overlayImage("--end-logo", endLogo)
			if err != nil {
				return err
			}
			c.Logo = l
			inputs["end_logo"] = endLogo
		}
		if endText != "" {
			cp["text"] = endText
		}
		if endSub != "" {
			cp["subtext"] = endSub
		}
		o.EndCard = c
		params["end_card"] = cp
	}
	if err := o.Check(); err != nil {
		if errors.Is(err, montage.ErrInvalid) {
			return usageError("giv post overlay: %s", localErrMsg(err))
		}
		return err
	}
	if o.Logo != nil {
		box, _ := o.Logo.Box(o.Width, o.Height, o.Margins)
		to := o.Logo.To
		if to == 0 {
			to = round3(o.Duration)
		}
		params["logo"] = map[string]any{"pos": string(o.Logo.Pos), "width": o.Logo.Width, "opacity": o.Logo.Opacity,
			"from": o.Logo.From, "to": to, "box": rectParam(box)}
	}
	if c := o.EndCard; c != nil && c.Logo != nil {
		box, _ := c.LogoBox(o.Width, o.Height, o.Margins)
		params["end_card"].(map[string]any)["logo_box"] = rectParam(box)
	}
	params["duration_out"] = round3(o.TotalDuration())
	return runLocal(lf, localOp{
		Command: "post overlay",
		Inputs:  inputs,
		Name:    localName(src.Path, "overlay"),
		Ext:     ".mp4",
		Params:  params,
		Build:   o.Build,
	})
}

// overlayImage controlla un'immagine da posare (--logo, --end-card,
// --end-logo) e ne legge le misure.
func overlayImage(flagName, path string) (*montage.Logo, error) {
	info, err := localMedia(flagName, path)
	if err != nil {
		return nil, err
	}
	if err := montage.CheckImage(path, info); err != nil {
		return nil, usageError("%s %s", flagName, localErrMsg(err))
	}
	return &montage.Logo{Path: path, SrcW: info.Video.Width, SrcH: info.Video.Height}, nil
}

// checkFrac controlla una frazione: un numero finito da 0 (escluso) a 1.
func checkFrac(name string, v float64) error {
	if v != v || v <= 0 || v > 1 {
		return usageError("%s %v: un numero da 0 (escluso) a 1", name, v)
	}
	return nil
}

// checkSeconds controlla dei secondi: un numero finito, positivo (o anche 0
// con zero true), al più 24 ore.
func checkSeconds(name string, v float64, zero bool) error {
	if v != v || v < 0 || !zero && v == 0 || v > 24*3600 {
		if zero {
			return usageError("%s %v: secondi, da 0 a 86400", name, v)
		}
		return usageError("%s %v: secondi, da 0 (escluso) a 86400", name, v)
	}
	return nil
}

// hexColor è il colore nella forma #RRGGBB, per il manifest.
func hexColor(c montage.Color) string { return fmt.Sprintf("#%02X%02X%02X", c.R, c.G, c.B) }

// rectParam è un riquadro nel manifest.
func rectParam(r montage.Rect) map[string]int {
	return map[string]int{"x": r.X, "y": r.Y, "w": r.W, "h": r.H}
}
