package main

import "gen-image-video-cli/internal/caps"

// giv upscale ingrandisce una clip esistente col motore comune dei comandi
// sulle clip (clip.go): --video file mp4 o URL https, --factor, --mode, prompt
// facoltativo. Senza --provider vale openrouter, non GIV_PROVIDER.
var upscaleSpec = clipSpec{Op: "upscale", Providers: []string{"openrouter"}}

func init() {
	clipOpTables["openrouter"] = caps.OpenRouterOps
}

func cmdUpscale(args []string) error { return cmdClip(upscaleSpec, args) }
