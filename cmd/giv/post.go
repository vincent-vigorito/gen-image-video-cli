package main

import (
	"gen-image-video-cli/internal/montage"
)

// giv post trim, speed e freeze: operazioni sul tempo di un video, in locale
// con ffmpeg (argomenti in internal/montage/time.go).

func init() {
	registerPost(postOp{Name: "trim", Usage: "[--start <s>] [--end <s>]   tiene solo il tratto fra i due istanti", Run: cmdPostTrim})
	registerPost(postOp{Name: "speed", Usage: "--factor <0.25-4>   più veloce o più lento, audio compreso", Run: cmdPostSpeed})
	registerPost(postOp{Name: "freeze", Usage: "[--at <s>] --duration <s>   ferma un fotogramma, poi il video riprende", Run: cmdPostFreeze})
}

const postTrimHelp = `giv post trim — tiene solo un tratto del video, in locale con ffmpeg (gratuito)

Uso:
  giv post trim --video <file> [--start <s>] [--end <s>] [--out <dir>] [--name <nome>] [--dry-run]

Flag:
  --start <s>   inizio del tratto da tenere, in secondi (default 0)
  --end <s>     fine del tratto, in secondi (default: la fine del video)

Video e audio ricodificati (H.264, AAC); nome di default <video>-trim.mp4.
`

const postSpeedHelp = `giv post speed — cambia la velocità del video, in locale con ffmpeg (gratuito)

Uso:
  giv post speed --video <file> --factor <n> [--out <dir>] [--name <nome>] [--dry-run]

Flag:
  --factor <n>   da 0.25 a 4: 2 = due volte più veloce (metà durata), 0.5 = rallentato
                 (doppia durata); l'audio segue alla stessa velocità, senza cambiare tono

Stessa frequenza dei fotogrammi del video; nome di default <video>-speed.mp4.
`

const postFreezeHelp = `giv post freeze — ferma un fotogramma per qualche secondo, poi il video riprende
(in locale con ffmpeg, gratuito)

Uso:
  giv post freeze --video <file> [--at <s>] --duration <s> [--out <dir>] [--name <nome>] [--dry-run]

Flag:
  --at <s>         istante del fotogramma da fermare, in secondi (default 0, il primo)
  --duration <s>   quanto resta fermo, in secondi (più di 0, al più 60)

L'audio fa una pausa di silenzio della stessa durata; tempi arrotondati al
fotogramma; nome di default <video>-freeze.mp4.
`

// cmdPostTrim è giv post trim.
func cmdPostTrim(args []string) error {
	fs, lf := newLocalFlags("post trim", postTrimHelp)
	start := fs.Float64("start", 0, "inizio in secondi")
	end := fs.Float64("end", 0, "fine in secondi")
	if err := parseLocal(fs, args); err != nil {
		return err
	}
	src, err := lf.source()
	if err != nil {
		return err
	}
	t := montage.Trim{Start: *start, End: *end}
	if t.End == 0 {
		t.End = src.Info.Duration
	}
	if err := t.Check(src.Info.Duration); err != nil {
		return usageError("giv post trim: %s", localErrMsg(err))
	}
	return runLocal(lf, localOp{
		Command: "post trim",
		Inputs:  map[string]string{"video": src.Path},
		Name:    localName(src.Path, "trim"),
		Ext:     ".mp4",
		Params:  map[string]any{"start_s": round3(t.Start), "end_s": round3(t.End)},
		Build: func(w *montage.Work, staged string) error {
			a, err := t.Args(src.Path, src.Info, staged)
			if err != nil {
				return err
			}
			return w.FFmpeg(a)
		},
	})
}

// cmdPostSpeed è giv post speed.
func cmdPostSpeed(args []string) error {
	fs, lf := newLocalFlags("post speed", postSpeedHelp)
	factor := fs.Float64("factor", 0, "fattore di velocità")
	if err := parseLocal(fs, args); err != nil {
		return err
	}
	if *factor == 0 {
		return usageError("giv post speed: manca --factor <n> (da %g a %g)", montage.MinSpeed, float64(montage.MaxSpeed))
	}
	s := montage.Speed{Factor: *factor}
	if err := s.Check(); err != nil {
		return usageError("giv post speed: %s", localErrMsg(err))
	}
	src, err := lf.source()
	if err != nil {
		return err
	}
	return runLocal(lf, localOp{
		Command: "post speed",
		Inputs:  map[string]string{"video": src.Path},
		Name:    localName(src.Path, "speed"),
		Ext:     ".mp4",
		Params:  map[string]any{"factor": s.Factor},
		Build: func(w *montage.Work, staged string) error {
			a, err := s.Args(src.Path, src.Info, staged)
			if err != nil {
				return err
			}
			return w.FFmpeg(a)
		},
	})
}

// cmdPostFreeze è giv post freeze.
func cmdPostFreeze(args []string) error {
	fs, lf := newLocalFlags("post freeze", postFreezeHelp)
	at := fs.Float64("at", 0, "istante in secondi")
	dur := fs.Float64("duration", 0, "durata del fermo in secondi")
	if err := parseLocal(fs, args); err != nil {
		return err
	}
	if *dur == 0 {
		return usageError("giv post freeze: manca --duration <s>")
	}
	src, err := lf.source()
	if err != nil {
		return err
	}
	f := montage.Freeze{At: *at, Duration: *dur}
	if err := f.Check(src.Info.Duration); err != nil {
		return usageError("giv post freeze: %s", localErrMsg(err))
	}
	if src.Info.Video.FPS <= 0 {
		return usageError("giv post freeze: --video %s: frequenza dei fotogrammi ignota", src.Path)
	}
	return runLocal(lf, localOp{
		Command: "post freeze",
		Inputs:  map[string]string{"video": src.Path},
		Name:    localName(src.Path, "freeze"),
		Ext:     ".mp4",
		Params:  map[string]any{"at_s": round3(f.At), "duration_s": round3(f.Duration)},
		Build: func(w *montage.Work, staged string) error {
			a, err := f.Args(src.Path, src.Info, staged)
			if err != nil {
				return err
			}
			return w.FFmpeg(a)
		},
	})
}
