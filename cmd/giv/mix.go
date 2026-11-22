package main

import (
	"flag"
	"fmt"
	"math"
	"strconv"
	"strings"

	"gen-image-video-cli/internal/montage"
)

// giv post mix: musica (anche abbassata sotto la voce), voce fuori campo e
// volume finale di un video, in locale con ffmpeg.

func init() {
	registerPost(postOp{Name: "mix", Usage: "musica, voce fuori campo e volume finale (--music, --voice, --loudness)", Run: cmdPostMix})
}

// mixMusicVolume è il volume della musica quando --music-volume manca (come story cut).
const mixMusicVolume = 0.25

// mixVoiceSlack sono i secondi di voce oltre la fine del video tollerati senza avviso.
const mixVoiceSlack = 0.05

const mixHelp = `giv post mix — musica, voce fuori campo e volume finale di un video, in locale con ffmpeg

Uso:
  giv post mix --video <file> [--music <file> [--music-volume 0.25] [--duck]]
               [--voice <file> [--voice-at <s>]] [--loudness -14|off]

Flag dell'operazione:
  --music <file>       musica (wav, mp3, m4a, ogg, flac…): ripetuta da capo se è più corta
                       del video, tagliata alla fine
  --music-volume <n>   volume della musica, moltiplicatore oltre 0 e fino a 4 (default 0.25)
  --duck               abbassa la musica mentre parla la voce (servono --music e --voice)
  --voice <file>       voce fuori campo, sommata all'audio del video
  --voice-at <s>       secondi dall'inizio del video in cui parte la voce (default 0);
                       la voce oltre la fine del video si taglia
  --loudness <LUFS>    volume finale in LUFS integrati, da -70 a -5, misurato e corretto
                       in due passate (loudnorm); default -14, la convenzione dei social
                       (nessuna fonte ufficiale); off: il volume resta com'è

L'audio del video resta e si somma a voce e musica; senza audio si parte dal silenzio.
Il video si copia senza ricodifica se è H.264, altrimenti si ricodifica in H.264.
Serve almeno uno fra --music, --voice e un --loudness diverso da off.
`

// parseLoudnessFlag legge --loudness: un numero in LUFS o off (0: nessuna normalizzazione).
func parseLoudnessFlag(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "off" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || montage.CheckLoudness(v) != nil {
		return 0, usageError("--loudness %q: un numero da %v a %v (LUFS, es. %v) oppure off", s, montage.MinLoudness, montage.MaxLoudness, montage.DefaultLoudness)
	}
	return v, nil
}

// cmdPostMix è giv post mix.
func cmdPostMix(args []string) error {
	fs, lf := newLocalFlags("post mix", mixHelp)
	music := fs.String("music", "", "musica")
	musicVolume := fs.Float64("music-volume", mixMusicVolume, "volume della musica")
	duck := fs.Bool("duck", false, "musica abbassata sotto la voce")
	voice := fs.String("voice", "", "voce fuori campo")
	voiceAt := fs.Float64("voice-at", 0, "inizio della voce, secondi")
	loudness := fs.String("loudness", strconv.FormatFloat(montage.DefaultLoudness, 'f', -1, 64), "volume finale in LUFS o off")
	if err := parseLocal(fs, args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	// controlli dei soli valori, prima di leggere i file
	if set["music"] && strings.TrimSpace(*music) == "" {
		return usageError("--music vuoto")
	}
	if set["voice"] && strings.TrimSpace(*voice) == "" {
		return usageError("--voice vuoto")
	}
	hasMusic, hasVoice := *music != "", *voice != ""
	if set["music-volume"] && !hasMusic {
		return usageError("--music-volume senza --music")
	}
	if v := *musicVolume; math.IsNaN(v) || v <= 0 || v > 4 {
		return usageError("--music-volume %v: un numero oltre 0 e fino a 4 (es. 0.25)", v)
	}
	if *duck && !(hasMusic && hasVoice) {
		return usageError("--duck abbassa la musica sotto la voce: servono --music e --voice")
	}
	if set["voice-at"] && !hasVoice {
		return usageError("--voice-at senza --voice")
	}
	if v := *voiceAt; math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return usageError("--voice-at %v: secondi da 0 in su", v)
	}
	lufs, err := parseLoudnessFlag(*loudness)
	if err != nil {
		return err
	}
	if !hasMusic && !hasVoice && lufs == 0 {
		return usageError("giv post mix: niente da fare: indica --music, --voice o un --loudness diverso da off")
	}

	src, err := lf.source()
	if err != nil {
		return err
	}
	dur := src.Info.Duration
	if math.IsNaN(dur) || math.IsInf(dur, 0) || dur <= 0 {
		return usageError("--video %s: durata non misurata", src.Path)
	}
	op := localOp{
		Command: "post mix",
		Inputs:  map[string]string{"video": src.Path},
		Name:    localName(src.Path, "mix"),
		Ext:     ".mp4",
		Params:  map[string]any{},
	}
	m := montage.Mix{Loudness: lufs, Duck: *duck}
	if hasMusic {
		info, err := localMedia("--music", *music)
		if err != nil {
			return err
		}
		if info.Audio == nil {
			return usageError("--music %s: nessuna traccia audio", *music)
		}
		if info.Duration > 0 && info.Duration < dur {
			op.Warnings = append(op.Warnings, fmt.Sprintf("la musica (%.2f s) è più corta del video (%.2f s): ricomincia da capo", info.Duration, dur))
		}
		m.Music = &montage.Music{Path: *music, Volume: *musicVolume}
		op.Inputs["music"] = *music
		op.Params["music_volume"] = *musicVolume
		if *duck {
			op.Params["duck"] = true
		}
	}
	if hasVoice {
		info, err := localMedia("--voice", *voice)
		if err != nil {
			return err
		}
		if info.Audio == nil {
			return usageError("--voice %s: nessuna traccia audio", *voice)
		}
		if *voiceAt >= dur {
			return usageError("--voice-at %v: oltre la fine del video (%.2f s)", *voiceAt, dur)
		}
		if end := *voiceAt + info.Duration; info.Duration > 0 && end > dur+mixVoiceSlack {
			op.Warnings = append(op.Warnings, fmt.Sprintf("la voce finisce a %.2f s, oltre la fine del video (%.2f s): la parte in più si taglia", end, dur))
		}
		m.Voices = []montage.Voice{{Path: *voice, At: *voiceAt}}
		op.Inputs["voice"] = *voice
		op.Params["voice_at_s"] = *voiceAt
	}
	if src.Info.Audio == nil && !hasMusic && !hasVoice {
		return usageError("--video %s: nessuna traccia audio, niente da normalizzare (aggiungi --music o --voice)", src.Path)
	}
	if lufs != 0 {
		op.Params["loudness_lufs"] = lufs
	}
	if src.Info.Video.Codec != "h264" {
		op.Warnings = append(op.Warnings, fmt.Sprintf("il video è %s: si ricodifica in H.264", src.Info.Video.Codec))
	}
	if err := m.Check(dur); err != nil {
		return usageError("giv post mix: %s", localErrMsg(err))
	}
	op.Build = func(w *montage.Work, staged string) error {
		measured, err := w.RunMix(src.Path, src.Info, m, staged)
		if measured != nil {
			// volume prima della normalizzazione, nel manifest
			op.Params["measured_lufs"] = measured.I
		}
		return err
	}
	return runLocal(lf, op)
}
