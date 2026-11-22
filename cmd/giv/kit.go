package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strings"
	"unicode/utf8"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/kit"
	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/provider"
	"gen-image-video-cli/internal/speech"
)

// kitTool legge con ffprobe le misure dei file del kit.
var kitTool = montage.Tool{}

// kitHelp è l'aiuto di giv kit (giv kit --help), su stderr come quello di giv.
const kitHelp = `giv kit — kit di marca e personaggi (formato nel README, sezione Kit di marca e personaggi)

Uso:
  giv kit check <kit.json>   controlla il kit ed elenca i file con le misure: nessuna
                             generazione, nessuna rete, gratuito; non scrive niente

Controlla forma e valori del kit (campi sconosciuti rifiutati) e che ogni file stia nella
sua cartella: percorsi relativi, dentro la cartella anche seguendo i link, solo file
normali, niente URL. Legge logo, riferimenti dei personaggi e musica (misure e durata con
ffprobe; senza ffprobe, un avviso) e la famiglia del font. Avvisa per un logo senza
trasparenza, un font non leggibile, più riferimenti di quanti i modelli video ne accettano,
una descrizione lunga. Un kit non valido esce con codice 2, un problema per riga.
Uscita: su stdout solo il JSON del risultato; avvisi ed errori su stderr.

Flag:
  -h, --help   questo aiuto
`

// cmdKit smista i sottocomandi di giv kit.
func cmdKit(args []string) error {
	if len(args) == 0 {
		return usageError("uso: giv kit check <kit.json>")
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprint(os.Stderr, kitHelp)
		return nil
	case "check":
		return cmdKitCheck(args[1:])
	}
	return usageError("giv kit %s: sottocomando sconosciuto (check)", args[0])
}

// kitCheckOut è l'uscita di giv kit check.
type kitCheckOut struct {
	Kit            string         `json:"kit"`
	Files          []kitFile      `json:"files"`
	Characters     []kitCharacter `json:"characters,omitempty"`
	Voice          *kit.Voce      `json:"voice,omitempty"`
	Pronunciations int            `json:"pronunciations,omitempty"` // parole del dizionario
	Warnings       []string       `json:"warnings,omitempty"`
}

// kitFile è un file del kit con le misure lette.
type kitFile struct {
	Field      string  `json:"field"` // campo del kit che lo nomina
	File       string  `json:"file"`  // relativo alla cartella del kit
	Kind       string  `json:"kind"`  // logo, font, musica, riferimento
	Bytes      int64   `json:"bytes"`
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	Alpha      *bool   `json:"alpha,omitempty"`       // logo: con trasparenza
	FontFamily string  `json:"font_family,omitempty"` // font: la famiglia usata nei testi
	DurationS  float64 `json:"duration_s,omitempty"`  // musica
	Codec      string  `json:"codec,omitempty"`       // musica
}

// kitCharacter è un personaggio del kit.
type kitCharacter struct {
	Name             string `json:"name"`
	References       int    `json:"references"`
	Tutela           string `json:"tutela,omitempty"`
	DescriptionChars int    `json:"description_chars,omitempty"`
}

func cmdKitCheck(args []string) error {
	fl := flag.NewFlagSet("kit check", flag.ExitOnError)
	fl.Usage = func() { fmt.Fprint(os.Stderr, kitHelp) }
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() != 1 {
		return usageError("uso: giv kit check <kit.json>")
	}
	path := fl.Arg(0)
	k, err := loadKit(path)
	if err != nil {
		return err
	}
	out, err := checkKit(k, path)
	if err != nil {
		return err
	}
	for _, w := range out.Warnings {
		fmt.Fprintln(os.Stderr, "avviso:", w)
	}
	return output.PrintJSON(out)
}

// loadKit legge e controlla il kit in path (codice 2 con l'elenco dei problemi).
func loadKit(path string) (*kit.Kit, error) {
	k, err := kit.Load(path)
	if err == nil {
		return k, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, usageError("%s: kit non trovato", path)
	}
	return nil, storyError(path, err)
}

// checkKit legge i file del kit già controllato: misure di logo e riferimenti,
// famiglia del font, durata della musica, tutela dei personaggi. Un file che
// non si legge per quello che è (un logo che non è un'immagine, una musica
// senza audio) è un rifiuto di codice 2; il resto sono avvisi. Legge soltanto:
// nessuna scrittura, nessuna rete.
func checkKit(k *kit.Kit, path string) (*kitCheckOut, error) {
	out := &kitCheckOut{Kit: path, Files: []kitFile{}, Voice: k.Voce, Pronunciations: len(k.Pronuncia)}
	var problems []string
	problem := func(f kit.File, format string, a ...any) {
		problems = append(problems, fmt.Sprintf("%s: %s: %s", f.Campo, f.File, fmt.Sprintf(format, a...)))
	}
	warn := func(format string, a ...any) { out.Warnings = append(out.Warnings, fmt.Sprintf(format, a...)) }
	probing := kitTool.Check() == nil
	if !probing {
		warn("ffprobe non trovato: misure di logo, riferimenti e musica non lette")
	}
	probe := func(p string) (*montage.Info, error) {
		ctx, cancel := context.WithTimeout(context.Background(), montage.ProbeTimeout)
		defer cancel()
		return kitTool.Probe(ctx, p)
	}
	for _, f := range k.Files() {
		full := k.Path(f.File)
		kf := kitFile{Field: f.Campo, File: f.File, Kind: f.Tipo}
		if st, err := os.Stat(full); err == nil {
			kf.Bytes = st.Size()
		}
		switch f.Tipo {
		case kit.TipoLogo, kit.TipoRiferimento:
			if _, err := provider.CheckImage(full); err != nil {
				problem(f, "%v", err)
				break
			}
			if probing {
				info, err := probe(full)
				if err == nil && f.Tipo == kit.TipoLogo {
					err = montage.CheckImage(full, info)
				}
				if err == nil && info.Video == nil {
					err = errors.New("nessuna immagine leggibile")
				}
				if err != nil {
					problem(f, "immagine non leggibile con ffprobe (%s)", localErrMsg(err))
					break
				}
				kf.Width, kf.Height = info.Video.Width, info.Video.Height
			}
			if f.Tipo == kit.TipoLogo {
				alpha, err := kit.Transparent(full)
				if err != nil {
					warn("%s %s: trasparenza non leggibile (%v)", f.Campo, f.File, err)
					break
				}
				kf.Alpha = &alpha
				if !alpha {
					warn("%s %s: logo senza trasparenza, sul video resta il riquadro dello sfondo dell'immagine", f.Campo, f.File)
				}
			}
		case kit.TipoFont:
			fam, err := montage.FontFamily(full)
			if err != nil {
				warn("%s %s: font non leggibile (%s): giv story cut lo rifiuterà", f.Campo, f.File, localErrMsg(err))
				break
			}
			kf.FontFamily = fam
		case kit.TipoMusica:
			if !probing {
				break
			}
			info, err := probe(full)
			if err != nil {
				problem(f, "non leggibile con ffprobe (%s)", localErrMsg(err))
				break
			}
			if info.Audio == nil {
				problem(f, "nessuna traccia audio")
				break
			}
			kf.DurationS, kf.Codec = math.Round(info.Duration*1000)/1000, info.Audio.Codec
			if info.Duration == 0 {
				warn("%s %s: durata della musica non letta", f.Campo, f.File)
			}
		}
		out.Files = append(out.Files, kf)
	}
	for _, name := range sortedKeys(k.Personaggi) {
		p := k.Personaggi[name]
		c := kitCharacter{Name: name, References: len(p.Riferimenti), Tutela: p.Tutela, DescriptionChars: utf8.RuneCountInString(p.Descrizione)}
		out.Characters = append(out.Characters, c)
		if err := checkTutela("personaggi."+name+".tutela", p.Tutela); err != nil {
			problems = append(problems, err.Error())
		}
		if c.DescriptionChars > kit.DescrizioneLunga {
			warn("personaggi.%s.descrizione: %d caratteri, oltre %d: va davanti al prompt di ogni scena che usa il personaggio",
				name, c.DescriptionChars, kit.DescrizioneLunga)
		}
		if w := refLimits(c.References); w != "" {
			warn("personaggi.%s.riferimenti: %d immagini, %s", name, c.References, w)
		}
	}
	// voce e pronunce: gli stessi controlli di giv voice (speech.Check), su
	// un testo di prova; i valori non validi sono rifiuti, gli avvisi restano
	if v := k.Voce; v != nil || len(k.Pronuncia) > 0 {
		req := speech.Request{Text: "-", Dict: speech.Dictionary(k.Pronuncia)}
		if v != nil {
			req.Provider, req.Model, req.Voice, req.Language, req.Style = v.Provider, v.Modello, v.Voce, v.Lingua, v.Stile
			if v.Velocita != nil {
				req.Speed = *v.Velocita
			}
		}
		_, warns, err := speech.Check(req)
		if err != nil {
			problems = append(problems, "voce: "+err.Error())
		}
		for _, w := range warns {
			warn("voce: %s", w)
		}
	}
	if len(problems) > 0 {
		return nil, storyError(path, errors.New(strings.Join(problems, "\n")))
	}
	return out, nil
}

// refLimits dice quali modelli video con limiti noti (xAI, Gemini, fal) non
// accettano n immagini di riferimento in una scena; "" se le accettano tutti.
// Una scena che usa più personaggi li somma: lo controlla giv story check.
func refLimits(n int) string {
	if n > provider.MaxRefs {
		return fmt.Sprintf("nessun modello video ne accetta più di %d in una scena", provider.MaxRefs)
	}
	var below []string
	for _, catalog := range [][]caps.Model{caps.XAI(), caps.Gemini(), caps.Fal()} {
		for _, m := range catalog {
			if m.MaxRefs != nil && *m.MaxRefs > 0 && *m.MaxRefs < n {
				below = append(below, fmt.Sprintf("%s al più %d", m.ID, *m.MaxRefs))
			}
		}
	}
	if len(below) == 0 {
		return ""
	}
	return "oltre il limite di " + strings.Join(below, ", ")
}
