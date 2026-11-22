package main

import (
	"context"
	"fmt"
	"os"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/output"
)

// giv avatar: un personaggio che parla, da un'immagine e da una voce (file wav
// o mp3), con Kling AI Avatar v2 su fal. Stesso giro dei comandi sulle clip
// (clip.go); la clip dura quanto l'audio e ne porta la voce.

var avatarSpec = clipSpec{Op: "avatar", Providers: []string{"fal"}}

func init() { clipOpTables["fal"] = caps.FalOps }

// avatarModels sono i nomi brevi di --model e di avatar.modello nello storyboard.
var avatarModels = map[string]string{"standard": "avatar-v2-standard", "pro": "avatar-v2-pro"}

// avatarModel traduce il nome breve nella scheda; gli altri valori restano
// come sono (le schede si controllano sulla tabella dell'operazione).
func avatarModel(m string) string {
	if id, ok := avatarModels[m]; ok {
		return id
	}
	return m
}

func cmdAvatar(args []string) error {
	o, err := parseClipFlags(avatarSpec, args)
	if err != nil {
		return err
	}
	o.Model = avatarModel(o.Model)
	job, err := prepareAvatar(o)
	if err != nil {
		return err
	}
	if o.DryRun {
		return output.PrintJSON(job.plan)
	}
	man, err := job.run(context.Background())
	if err != nil {
		return err
	}
	return emit(avatarSpec.Op, man)
}

// prepareAvatar è prepareClip con il limite prudente sulla durata dell'audio
// (non documentato da fal): oltre, un avviso; con strict un rifiuto.
func prepareAvatar(o clipOptions) (*clipJob, error) {
	job, err := prepareClip(avatarSpec, o)
	if err != nil {
		return nil, err
	}
	if d := job.req.Source.AudioDuration; d > caps.AvatarAudioWarnS {
		msg := fmt.Sprintf("audio di %.1f s: la durata massima dell'avatar su fal non è documentata, oltre %d s la richiesta può essere rifiutata", d, caps.AvatarAudioWarnS)
		if o.Strict {
			return nil, usageError("%s (--strict)", msg)
		}
		fmt.Fprintf(os.Stderr, "avviso: %s\n", msg)
		job.plan.Warnings = append(job.plan.Warnings, msg)
	}
	return job, nil
}
