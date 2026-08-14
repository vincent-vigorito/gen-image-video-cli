package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"gen-image-video-cli/internal/config"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/provider"
	"gen-image-video-cli/internal/provider/gemini"
)

const (
	version           = "0.1.0"
	defaultImageModel = "gemini-2.5-flash-image"
	defaultVideoModel = "veo-3.0-fast-generate-001"
)

type manifest struct {
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Prompt   string            `json:"prompt"`
	Files    []output.FileInfo `json:"files"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "models":
		err = cmdModels(os.Args[2:])
	case "image":
		err = cmdImage(os.Args[2:])
	case "video":
		err = cmdVideo(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("giv " + version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "comando sconosciuto: %s\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "errore:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `giv — generazione immagini e video multi-provider (per ora: gemini)

Uso:
  giv models [--all] [--json]      elenca i modelli image/video disponibili
  giv image  [flag] "<prompt>"     genera immagini
  giv video  [flag] "<prompt>"     genera un video (Veo, job asincrono ~1-5 min)
  giv version                      stampa la versione

Flag comuni:
  --model <nome>     modello (default image: `+defaultImageModel+`, video: `+defaultVideoModel+`)
  --out <dir>        directory di output (default: out)
  --name <slug>      base dei nomi file (default: derivato dal prompt)

giv image:
  -n <num>           numero di immagini (default 1)
  --aspect <ratio>   1:1, 16:9, 9:16, 4:3, 3:4
  --input <file>     immagine di input/riferimento, ripetibile (editing — solo modelli gemini-*-image)

giv video:
  --aspect <ratio>   16:9, 9:16
  --resolution <r>   720p, 1080p
  --negative "<t>"   negative prompt
  --image <file>     frame iniziale (image-to-video)

Credenziali: GEMINI_API_KEY da env di processo, ./credentials.env o ./.env
Output: file salvati in --out, manifest JSON su stdout (i log vanno su stderr)
`)
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// parsePrompt accetta i flag sia prima che dopo il prompt (quotato).
func parsePrompt(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return "", fmt.Errorf("manca il prompt")
	}
	prompt := rest[0]
	if len(rest) > 1 {
		if err := fs.Parse(rest[1:]); err != nil {
			return "", err
		}
		if len(fs.Args()) > 0 {
			return "", fmt.Errorf("passa il prompt come singolo argomento tra virgolette")
		}
	}
	return prompt, nil
}

func geminiClient() (*gemini.Client, error) {
	key := config.Get("GEMINI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY mancante: mettila in .env o credentials.env nella directory corrente, oppure esportala")
	}
	return gemini.New(key), nil
}

func cmdModels(args []string) error {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	all := fs.Bool("all", false, "includi anche i modelli non image/video")
	asJSON := fs.Bool("json", false, "output JSON")
	fs.Parse(args)
	c, err := geminiClient()
	if err != nil {
		return err
	}
	models, err := c.Models(context.Background())
	if err != nil {
		return err
	}
	if !*all {
		var f []provider.ModelInfo
		for _, m := range models {
			if m.Kind == "image" || m.Kind == "video" {
				f = append(f, m)
			}
		}
		models = f
	}
	if *asJSON {
		return output.PrintJSON(models)
	}
	for _, m := range models {
		fmt.Printf("%-50s %-6s %s\n", m.Name, m.Kind, m.DisplayName)
	}
	return nil
}

func cmdImage(args []string) error {
	fs := flag.NewFlagSet("image", flag.ExitOnError)
	model := fs.String("model", defaultImageModel, "modello")
	n := fs.Int("n", 1, "numero di immagini")
	aspect := fs.String("aspect", "", "aspect ratio")
	out := fs.String("out", "out", "directory di output")
	name := fs.String("name", "", "slug per i nomi file")
	var inputs stringList
	fs.Var(&inputs, "input", "immagine di input (ripetibile)")
	prompt, err := parsePrompt(fs, args)
	if err != nil {
		return err
	}
	c, err := geminiClient()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generazione immagine con %s…\n", *model)
	media, err := c.GenerateImage(context.Background(), provider.ImageRequest{
		Prompt: prompt, Model: *model, N: *n, Aspect: *aspect, Inputs: inputs,
	})
	if err != nil {
		return err
	}
	slug := *name
	if slug == "" {
		slug = output.Slug(prompt)
	}
	files, err := output.SaveAll(*out, slug, media)
	if err != nil {
		return err
	}
	return output.PrintJSON(manifest{Provider: c.Name(), Model: *model, Prompt: prompt, Files: files})
}

func cmdVideo(args []string) error {
	fs := flag.NewFlagSet("video", flag.ExitOnError)
	model := fs.String("model", defaultVideoModel, "modello")
	aspect := fs.String("aspect", "", "aspect ratio")
	resolution := fs.String("resolution", "", "720p o 1080p")
	negative := fs.String("negative", "", "negative prompt")
	image := fs.String("image", "", "frame iniziale (image-to-video)")
	out := fs.String("out", "out", "directory di output")
	name := fs.String("name", "", "slug per i nomi file")
	prompt, err := parsePrompt(fs, args)
	if err != nil {
		return err
	}
	c, err := geminiClient()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generazione video con %s…\n", *model)
	media, err := c.GenerateVideo(context.Background(), provider.VideoRequest{
		Prompt: prompt, Model: *model, Aspect: *aspect,
		Resolution: *resolution, Negative: *negative, Image: *image,
	})
	if err != nil {
		return err
	}
	slug := *name
	if slug == "" {
		slug = output.Slug(prompt)
	}
	files, err := output.SaveAll(*out, slug, media)
	if err != nil {
		return err
	}
	return output.PrintJSON(manifest{Provider: c.Name(), Model: *model, Prompt: prompt, Files: files})
}
