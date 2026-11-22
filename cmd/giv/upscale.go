package main

import "gen-image-video-cli/internal/caps"

// giv upscale ingrandisce una clip esistente col motore comune dei comandi
// sulle clip (clip.go): --video file mp4 o URL https, --factor; con openrouter
// anche --mode e un prompt facoltativo, con fal (Topaz) solo il fattore. Senza
// --provider vale openrouter, non GIV_PROVIDER; la tabella di fal la registra
// giv avatar.
var upscaleSpec = clipSpec{Op: "upscale", Providers: []string{"openrouter", "fal"}}

func init() {
	clipOpTables["openrouter"] = caps.OpenRouterOps
}

func cmdUpscale(args []string) error { return cmdClip(upscaleSpec, args) }
