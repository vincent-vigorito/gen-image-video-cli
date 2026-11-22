package main

import "gen-image-video-cli/internal/caps"

// giv extend: allunga una clip esistente col motore comune dei comandi sulle
// clip (clip.go).
var extendSpec = clipSpec{Op: "extend", Providers: []string{"xai", "gemini"}}

func init() {
	clipOpTables["xai"] = caps.XAIOps // extend ed edit
}

func cmdExtend(args []string) error { return cmdClip(extendSpec, args) }
