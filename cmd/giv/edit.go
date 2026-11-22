package main

// giv edit: modifica una clip esistente col motore comune dei comandi sulle
// clip (clip.go); la tabella xAI è registrata in extend.go.
var editSpec = clipSpec{Op: "edit", Providers: []string{"xai", "gemini"}}

func cmdEdit(args []string) error { return cmdClip(editSpec, args) }
