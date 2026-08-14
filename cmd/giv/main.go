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
	"gen-image-video-cli/internal/provider/openaicompat"
)

const version = "0.4.0"

var defaultImageModels = map[string]string{
	"gemini":     "gemini-2.5-flash-image",
	"openai":     "gpt-image-1",
	"xai":        "grok-imagine-image-2.0",
	"openrouter": "google/gemini-2.5-flash-image",
}

var defaultVideoModels = map[string]string{
	"gemini":     "veo-3.0-fast-generate-001",
	"openai":     "sora-2",
	"openrouter": "google/veo-3.1",
}

type manifest struct {
	Provider string            `json:"provider"`
	Model    string            `json:"model"`
	Prompt   string            `json:"prompt"`
	Files    []output.FileInfo `json:"files"`
	CostUSD  float64           `json:"cost_usd,omitempty"`
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
  --provider <p>     gemini (default) | openai | xai | openrouter
  --model <nome>     modello; default image — gemini: gemini-2.5-flash-image,
                     openai: gpt-image-1, xai: grok-imagine-image-2.0,
                     openrouter: google/gemini-2.5-flash-image
                     default video — gemini: Veo, openai: sora-2, openrouter: google/veo-3.1
  --out <dir>        directory di output (default: out)
  --name <slug>      base dei nomi file (default: derivato dal prompt)

giv image:
  -n <num>           numero di immagini (default 1)
  --aspect <ratio>   1:1, 16:9, 9:16, 4:3, 3:4
  --seed <n>         seed deterministico (gemini, openrouter; altrove ignorato con avviso)
  --input <file>     immagine di input/riferimento, ripetibile (non supportato da xai e Imagen)

giv video:
  --aspect <ratio>   16:9, 9:16
  --resolution <r>   720p, 1080p
  --negative "<t>"   negative prompt
  --image <file>     frame iniziale (image-to-video)
  --duration <s>     durata in secondi (Veo 3.1: 4, 6, 8)

Credenziali: GEMINI_API_KEY / OPENAI_API_KEY / XAI_API_KEY / OPENROUTER_API_KEY
             da env di processo, ./credentials.env o ./.env
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

var envKeys = map[string]string{
	"gemini":     "GEMINI_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"xai":        "XAI_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
}

func newProvider(name string) (provider.Provider, error) {
	envKey, ok := envKeys[name]
	if !ok {
		return nil, fmt.Errorf("provider sconosciuto: %s (validi: gemini, openai, xai, openrouter)", name)
	}
	key := config.Get(envKey)
	if key == "" {
		return nil, fmt.Errorf("%s mancante: mettila in .env o credentials.env nella directory corrente, oppure esportala", envKey)
	}
	switch name {
	case "gemini":
		return gemini.New(key), nil
	case "openai":
		return openaicompat.New("openai", "https://api.openai.com/v1", key,
			openaicompat.Options{UseSize: true, Sora: true}), nil
	case "xai":
		return openaicompat.New("xai", "https://api.x.ai/v1", key,
			openaicompat.Options{B64Param: true}), nil
	default: // openrouter
		return openaicompat.New("openrouter", "https://openrouter.ai/api/v1", key,
			openaicompat.Options{Router: true}), nil
	}
}

func resolveModel(flagValue, providerName string, defaults map[string]string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if m := defaults[providerName]; m != "" {
		return m, nil
	}
	return "", fmt.Errorf("nessun modello di default per %s: specifica --model", providerName)
}

func cmdModels(args []string) error {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	prov := fs.String("provider", "gemini", "provider")
	all := fs.Bool("all", false, "includi anche i modelli non image/video")
	asJSON := fs.Bool("json", false, "output JSON")
	fs.Parse(args)
	c, err := newProvider(*prov)
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
	prov := fs.String("provider", "gemini", "provider")
	model := fs.String("model", "", "modello (default: dipende dal provider)")
	n := fs.Int("n", 1, "numero di immagini")
	aspect := fs.String("aspect", "", "aspect ratio")
	out := fs.String("out", "out", "directory di output")
	name := fs.String("name", "", "slug per i nomi file")
	seed := fs.Int("seed", 0, "seed deterministico (gemini, openrouter; altrove ignorato)")
	var inputs stringList
	fs.Var(&inputs, "input", "immagine di input (ripetibile)")
	prompt, err := parsePrompt(fs, args)
	if err != nil {
		return err
	}
	c, err := newProvider(*prov)
	if err != nil {
		return err
	}
	m, err := resolveModel(*model, *prov, defaultImageModels)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generazione immagine con %s/%s…\n", *prov, m)
	res, err := c.GenerateImage(context.Background(), provider.ImageRequest{
		Prompt: prompt, Model: m, N: *n, Aspect: *aspect, Seed: *seed, Inputs: inputs,
	})
	if err != nil {
		return err
	}
	slug := *name
	if slug == "" {
		slug = output.Slug(prompt)
	}
	files, err := output.SaveAll(*out, slug, res.Media)
	if err != nil {
		return err
	}
	return output.PrintJSON(manifest{Provider: c.Name(), Model: m, Prompt: prompt, Files: files, CostUSD: res.CostUSD})
}

func cmdVideo(args []string) error {
	fs := flag.NewFlagSet("video", flag.ExitOnError)
	prov := fs.String("provider", "gemini", "provider")
	model := fs.String("model", "", "modello (default: dipende dal provider)")
	aspect := fs.String("aspect", "", "aspect ratio")
	resolution := fs.String("resolution", "", "720p o 1080p")
	negative := fs.String("negative", "", "negative prompt")
	image := fs.String("image", "", "frame iniziale (image-to-video)")
	duration := fs.Int("duration", 0, "durata in secondi (Veo 3.1: 4, 6 o 8; 0 = default modello)")
	out := fs.String("out", "out", "directory di output")
	name := fs.String("name", "", "slug per i nomi file")
	prompt, err := parsePrompt(fs, args)
	if err != nil {
		return err
	}
	c, err := newProvider(*prov)
	if err != nil {
		return err
	}
	m, err := resolveModel(*model, *prov, defaultVideoModels)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generazione video con %s/%s…\n", *prov, m)
	res, err := c.GenerateVideo(context.Background(), provider.VideoRequest{
		Prompt: prompt, Model: m, Aspect: *aspect,
		Resolution: *resolution, Negative: *negative, Image: *image,
		Duration: *duration,
	})
	if err != nil {
		return err
	}
	slug := *name
	if slug == "" {
		slug = output.Slug(prompt)
	}
	files, err := output.SaveAll(*out, slug, res.Media)
	if err != nil {
		return err
	}
	return output.PrintJSON(manifest{Provider: c.Name(), Model: m, Prompt: prompt, Files: files, CostUSD: res.CostUSD})
}
