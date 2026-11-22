package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/provider"
	"gen-image-video-cli/internal/story"
)

// Scene avatar di giv story run: come giv avatar (Kling AI Avatar v2 su fal),
// con la stessa riserva, lo stesso tetto e lo stesso recupero dei job delle
// scene video.

// storyCommand è il command del registro per la clip di una scena generata.
func storyCommand(scene *story.Scene) string {
	if scene.Tipo == story.Avatar {
		return "story avatar"
	}
	return "story video"
}

// launchAvatar genera la clip di una scena avatar: immagine e voce sono lette
// solo dentro la cartella dello storyboard e copiate in una cartella privata,
// da cui le carica il provider; poi riserva e avvio come le scene video.
func (r *storyRunner) launchAvatar(ctx context.Context, i int, scene *story.Scene, ss *story.SceneState, fail func(error) error) error {
	job := r.sc.clips[scene.ID]
	work, err := montage.TempDir("giv-story-") // rifiuta una TMPDIR condivisa senza sticky bit
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	req := job.req
	if req.Image, err = r.privateCopy(scene.Immagine, work); err != nil {
		return fail(fmt.Errorf("immagine: %w", err))
	}
	if req.Audio, err = r.privateAudio(scene.Voce, work); err != nil {
		return fail(fmt.Errorf("voce: %w", err))
	}
	if want := req.Source.AudioDuration; want > 0 {
		// la stima e il tetto valgono per la voce misurata nel controllo: si
		// rimisura la copia che parte, se è cambiata la scena non parte
		info, err := mediaTool.Probe(ctx, req.Audio)
		if err != nil || info.Audio == nil {
			return fail(usageError("voce: %s non leggibile con ffprobe dopo la copia", scene.Voce))
		}
		if math.Abs(info.Duration-want) > 0.01 {
			return fail(usageError("voce: %s cambiata dopo il controllo (%.2f s invece di %.2f s): rilancia giv story run", scene.Voce, info.Duration, want))
		}
	}
	c, err := r.sc.client(job.provider)
	if err != nil {
		return fail(err)
	}
	g, ok := c.Provider.(provider.ClipGenerator)
	if !ok {
		return fail(fmt.Errorf("giv avatar non disponibile con %s", job.provider))
	}
	run := func(ctx context.Context) (manifest, error) {
		man := job.manifest(req) // ingressi come nello storyboard, impronte delle copie
		fmt.Fprintf(os.Stderr, "giv avatar con %s/%s…\n", job.provider, req.Model)
		start := time.Now()
		res, err := g.GenerateClip(ctx, req)
		if err != nil {
			logFailedJob(man, err, time.Since(start) >= req.Timeout)
			return manifest{}, err
		}
		files, err := r.saveFiles(r.sc.names[scene.ID], res.Media)
		if err != nil {
			return manifest{}, err
		}
		man.done(files, res)
		return man, nil
	}
	return r.startClip(ctx, i, scene, ss, job.plan.costExact, run, fail, storyCommand(scene))
}

// privateAudio legge la voce rel (relativa alla cartella dello storyboard) solo
// dentro la cartella, anche seguendo i link, controlla che sia un audio wav o
// mp3 entro i limiti e ne scrive una copia privata in work.
func (r *storyRunner) privateAudio(rel, work string) (string, error) {
	f, err := r.root.OpenFile(filepath.FromSlash(rel), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("%s: file non leggibile dentro la cartella dello storyboard: %w", rel, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: non è un file regolare", rel)
	}
	tooBig := fmt.Errorf("%s: oltre il limite di %d MiB per audio", rel, provider.MaxAudioBytes>>20)
	if fi.Size() > provider.MaxAudioBytes {
		return "", tooBig
	}
	data, err := io.ReadAll(io.LimitReader(f, provider.MaxAudioBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > provider.MaxAudioBytes {
		return "", tooBig
	}
	ext := map[string]string{"audio/wav": ".wav", "audio/mpeg": ".mp3"}[provider.AudioMime(data)]
	if ext == "" {
		return "", fmt.Errorf("%s: non è un audio wav o mp3", rel)
	}
	out, err := os.CreateTemp(work, "voce-*"+ext)
	if err != nil {
		return "", err
	}
	if _, err := out.Write(data); err != nil {
		out.Close()
		return "", err
	}
	return out.Name(), out.Close()
}
