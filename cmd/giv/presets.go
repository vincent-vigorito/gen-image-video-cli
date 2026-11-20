package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/presets"
)

// loadPresets carica la libreria dei preset; un file dell'utente malformato è un errore d'uso.
func loadPresets() (*presets.Library, error) {
	lib, err := presets.Load()
	if err != nil {
		return nil, usageError("%v", err)
	}
	return lib, nil
}

// cmdPresets: "giv presets [--json]" elenca i preset, "giv presets show ..." espande.
func cmdPresets(args []string) error {
	if len(args) > 0 && args[0] == "show" {
		return cmdPresetsShow(args[1:])
	}
	fs := flag.NewFlagSet("presets", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output JSON")
	fs.Parse(args)
	if fs.NArg() > 0 {
		return usageError("argomento inatteso: %s (per una frase usa giv presets show <nome>)", fs.Arg(0))
	}
	lib, err := loadPresets()
	if err != nil {
		return err
	}
	list := lib.List()
	if *asJSON {
		type view struct {
			*presets.Preset
			Source string `json:"origine"`
		}
		out := make([]view, len(list))
		for i, p := range list {
			out[i] = view{p, p.Source}
		}
		return output.PrintJSON(out)
	}
	for _, p := range list {
		src := ""
		if p.Source != presets.Builtin {
			src = "  [" + p.Source + "]"
		}
		fmt.Printf("%-14s %-7s %s%s\n", p.ID, p.Type, p.Description, src)
	}
	return nil
}

// applyPresets espande prompt e negative di giv video coi preset scelti per il
// modello m. Senza preset né --tutela restituisce prompt e negative identici e non
// legge la libreria. Coi preset e --image la tutela di default è "prodotto";
// "--tutela no" la toglie. Gli avvisi vanno su stderr; gli errori sono d'uso (codice 2).
func applyPresets(names []string, protect, prompt, negative, m string, in presets.Inputs) (string, string, error) {
	off := protect == "no"
	if off {
		protect = ""
	}
	if len(names) == 0 && protect == "" {
		return prompt, negative, nil
	}
	if protect == "" && !off && in.FirstFrame {
		protect = "prodotto"
	}
	lib, err := loadPresets()
	if err != nil {
		return "", "", err
	}
	res, err := lib.Expand(presets.Request{
		Prompt: prompt, Negative: negative, Presets: names,
		Model: m, Protect: protect, Inputs: &in,
	})
	if err != nil {
		return "", "", usageError("%v", err)
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(os.Stderr, "avviso:", w)
	}
	return res.Prompt, res.Negative, nil
}

// presetShow è l'uscita di giv presets show: gli stessi campi che finiranno nel
// manifest di giv video (prompt_final, presets[]).
type presetShow struct {
	Model       string            `json:"model,omitempty"`
	Family      string            `json:"famiglia,omitempty"`
	Prompt      string            `json:"prompt,omitempty"`
	PromptFinal string            `json:"prompt_final"`
	Negative    string            `json:"negative,omitempty"`
	Presets     []presets.Applied `json:"presets"`
	Warnings    []string          `json:"warnings,omitempty"`
}

// cmdPresetsShow: giv presets show <nome> [<nome>] [--model M] [--prompt "<t>"]
// [--negative "<t>"] [--tutela T] [--strict]. Nessuna chiamata ai provider.
func cmdPresetsShow(args []string) error {
	fs := flag.NewFlagSet("presets show", flag.ExitOnError)
	model := fs.String("model", "", "modello di destinazione (variante della sua famiglia)")
	prompt := fs.String("prompt", "", "prompt dell'utente a cui aggiungere i preset")
	negative := fs.String("negative", "", "negative dell'utente")
	protect := fs.String("tutela", "", "clausola di tutela: prodotto, illustrazione")
	strict := fs.Bool("strict", false, "variante mancante = errore")
	// i flag valgono prima e dopo i nomi dei preset
	var names []string
	rest := args
	for {
		fs.Parse(rest)
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		names = append(names, rest[0])
		rest = rest[1:]
	}
	if len(names) == 0 {
		return usageError("manca il nome del preset (elenco con giv presets)")
	}
	lib, err := loadPresets()
	if err != nil {
		return err
	}
	res, err := lib.Expand(presets.Request{
		Prompt:   *prompt,
		Negative: *negative,
		Presets:  names,
		Model:    *model,
		Protect:  *protect,
		Strict:   *strict,
	})
	if err != nil {
		return usageError("%v", err)
	}
	for _, w := range res.Warnings {
		fmt.Fprintln(os.Stderr, "avviso:", w)
	}
	return output.PrintJSON(presetShow{
		Model:       strings.TrimSpace(*model),
		Family:      res.Family,
		Prompt:      *prompt,
		PromptFinal: res.Prompt,
		Negative:    res.Negative,
		Presets:     res.Presets,
		Warnings:    res.Warnings,
	})
}
