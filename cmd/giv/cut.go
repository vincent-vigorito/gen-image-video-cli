package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/story"
)

// Valori del montaggio quando lo storyboard non li indica.
const (
	cutAspect      = "16:9"
	cutResolution  = "720p"
	cutFPS         = 30
	cutFadeSeconds = 0.5  // durata di ogni dissolvenza
	cutMusicVolume = 0.25 // musica sotto la voce
	cutVoiceSlack  = 0.05 // secondi di voce oltre la scena tollerati senza avviso
)

// storyCutResult è l'uscita di giv story cut.
type storyCutResult struct {
	Storyboard string   `json:"storyboard"`
	Output     string   `json:"output"`
	Scenes     int      `json:"scenes"`
	DurationS  float64  `json:"duration_s"`          // letta dal file scritto
	ExpectedS  float64  `json:"expected_duration_s"` // dal piano: somma delle scene meno le dissolvenze
	Width      int      `json:"width"`
	Height     int      `json:"height"`
	FPS        float64  `json:"fps"`
	VideoCodec string   `json:"video_codec"`
	AudioCodec string   `json:"audio_codec,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// runStoryCut è giv story cut: monta con ffmpeg le clip già presenti, senza
// generazione e senza rete; il file d'uscita dello storyboard si riscrive a
// ogni montaggio.
func runStoryCut(args []string) error {
	path, err := storyArg("cut", args)
	if err != nil {
		return err
	}
	s, err := loadStory(path)
	if err != nil {
		return err
	}
	plan, err := cutPlan(s, path)
	if err != nil {
		return err
	}
	tool := montage.Tool{}
	if err := tool.Check(); err != nil {
		return &cliError{code: exitError, msg: err.Error()}
	}
	// una sola scadenza per le letture delle clip e per il montaggio
	ctx, cancel := context.WithTimeout(context.Background(), montage.DefaultTimeout)
	defer cancel()
	if err := keepAudio(ctx, tool, plan.Clips); err != nil {
		if ctx.Err() != nil {
			return err
		}
		return usageError("%s: %v", path, err)
	}
	fmt.Fprintf(os.Stderr, "montaggio di %d scene in %s\n", len(plan.Clips), plan.Output)
	res, err := tool.Montage(ctx, plan)
	if err != nil {
		if errors.Is(err, montage.ErrInvalid) {
			return usageError("%s: %v", path, err)
		}
		return err
	}
	out := storyCutResult{Storyboard: path, Output: res.Output, Scenes: len(plan.Clips),
		ExpectedS: round3(res.Duration), Warnings: res.Warnings}
	if i := res.Info; i != nil {
		out.DurationS = round3(i.Duration)
		if v := i.Video; v != nil {
			out.Width, out.Height, out.FPS, out.VideoCodec = v.Width, v.Height, round3(v.FPS), v.Codec
		}
		if a := i.Audio; a != nil {
			out.AudioCodec = a.Codec
		}
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "avviso: %s\n", w)
	}
	return output.PrintJSON(out)
}

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

// cutPlan traduce lo storyboard nel piano di montaggio: formato, una clip per
// scena (file della scena, o la clip generata da giv story run), voce,
// transizione, musica, sottotitoli e uscita. Ogni problema è un errore d'uso.
func cutPlan(s *story.Storyboard, path string) (montage.Plan, error) {
	var p montage.Plan
	w, h, fps, err := cutFormat(s)
	if err != nil {
		return p, usageError("%s: %v", path, err)
	}
	p.Width, p.Height, p.FPS = w, h, fps
	m := s.Montaggio
	if m == nil {
		m = &story.Montaggio{}
	}
	trans := montage.Transition{Kind: montage.Cut}
	if m.Transizione == "dissolvenza" {
		trans = montage.Transition{Kind: montage.Fade, Duration: cutFadeSeconds}
	}
	var missing []string
	for i := range s.Scene {
		sc := &s.Scene[i]
		c := montage.Clip{Duration: float64(sc.Durata), Transition: trans}
		switch sc.Tipo {
		case story.Clip:
			c.Path = s.Path(sc.File)
		case story.Immagine:
			c.Path, c.Image = s.Path(sc.File), true
		case story.Video:
			clip, err := sceneClip(s, path, sc)
			if err != nil {
				missing = append(missing, fmt.Sprintf("scena %s: %v", sc.ID, err))
				continue
			}
			c.Path = clip
		default:
			return p, usageError("%s: scena %s: tipo %q non montabile", path, sc.ID, sc.Tipo)
		}
		if sc.Voce != "" {
			c.Voice = s.Path(sc.Voce)
		}
		p.Clips = append(p.Clips, c)
	}
	if len(missing) > 0 {
		return p, usageError("%s: clip mancanti (genera le scene con giv story run):\n  %s", path, strings.Join(missing, "\n  "))
	}
	if mu := m.Musica; mu != nil {
		vol := cutMusicVolume
		if mu.Volume != nil {
			vol = *mu.Volume
		}
		// volume 0: musica muta, quindi niente musica (per montage 0 vale 1)
		if vol > 0 {
			p.Music = &montage.Music{Path: s.Path(mu.File), Volume: vol}
		}
	}
	if m.Sottotitoli != "" {
		p.Subtitles = s.Path(m.Sottotitoli)
	}
	out := m.Uscita
	if out == "" {
		out = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + ".mp4"
	}
	p.Output, p.Root = s.Path(out), s.Dir
	// controllo anticipato, per un errore d'uso prima del montaggio; la
	// pubblicazione resta comunque vincolata a Root
	if err := insideDir(s.Dir, filepath.Dir(p.Output)); err != nil {
		return p, usageError("%s: montaggio.uscita: %v", path, err)
	}
	// il montato è il prodotto dello storyboard: rifare cut lo riscrive
	p.Overwrite = true
	return p, nil
}

// keepAudio tiene l'audio delle clip che ne hanno uno (riprese vere, video
// generati con audio on); le altre scene sono mute sotto voce e musica.
func keepAudio(ctx context.Context, tool montage.Tool, clips []montage.Clip) error {
	for i := range clips {
		c := &clips[i]
		if c.Image {
			continue
		}
		info, err := tool.Probe(ctx, c.Path)
		if err != nil {
			return fmt.Errorf("scena n. %d: %v", i+1, err)
		}
		c.KeepAudio = info.Audio != nil
	}
	return nil
}

// cutFormat ricava risoluzione e fps del montato: formato dello storyboard,
// poi blocco default, poi 16:9 720p 30 fps. La risoluzione è il lato corto.
func cutFormat(s *story.Storyboard) (w, h, fps int, err error) {
	var f story.Formato
	if s.Formato != nil {
		f = *s.Formato
	}
	aspect := firstOf(f.Aspect, s.Default.Aspect, cutAspect)
	res := firstOf(f.Risoluzione, s.Default.Risoluzione, cutResolution)
	fps = f.FPS
	if fps == 0 {
		fps = cutFPS
	}
	short, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(res), "p"))
	if err != nil || !strings.HasSuffix(strings.ToLower(res), "p") || short < 144 || short > 2160 {
		return 0, 0, 0, fmt.Errorf("formato.risoluzione %q: serve un valore come 720p o 1080p", res)
	}
	a, b, ok := strings.Cut(aspect, ":")
	aw, err1 := strconv.Atoi(a)
	ah, err2 := strconv.Atoi(b)
	if !ok || err1 != nil || err2 != nil || aw < 1 || ah < 1 || aw > 100 || ah > 100 {
		return 0, 0, 0, fmt.Errorf("formato.aspect %q: serve un rapporto come 16:9 o 9:16", aspect)
	}
	even := func(v float64) int { return int(v/2+0.5) * 2 }
	if aw >= ah {
		w, h = even(float64(short)*float64(aw)/float64(ah)), even(float64(short))
	} else {
		w, h = even(float64(short)), even(float64(short)*float64(ah)/float64(aw))
	}
	if fps < 1 || fps > 120 {
		return 0, 0, 0, fmt.Errorf("formato.fps %d: da 1 a 120", fps)
	}
	return w, h, fps, nil
}

func firstOf(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}

// insideDir controlla che dir (o la sua parte che esiste già) resti nella
// cartella dello storyboard anche seguendo i link.
func insideDir(root, dir string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("cartella dello storyboard non leggibile: %v", err)
	}
	d := dir
	for {
		if _, err := os.Lstat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	real, err := filepath.EvalSymlinks(d)
	if err != nil {
		return fmt.Errorf("%s: percorso non leggibile", dir)
	}
	if rel, err := filepath.Rel(realRoot, real); err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("la cartella d'uscita porta fuori dalla cartella dello storyboard")
	}
	return nil
}

// voiceWarning confronta la durata della voce con quella della scena (0 =
// ignota, nessun confronto); restituisce l'avviso o "". Le letture con
// ffprobe di tutto lo story check stanno entro montage.ProbeTimeout, dalla
// prima.
func (chk *storyChecker) voiceWarning(sc *story.Scene, sceneSeconds float64) string {
	if sc.Voce == "" {
		return ""
	}
	s := chk.s
	tool := montage.Tool{}
	if err := tool.Check(); err != nil {
		return "durata della voce non controllata: ffprobe non trovato"
	}
	if chk.probeUntil.IsZero() {
		chk.probeUntil = time.Now().Add(montage.ProbeTimeout)
	}
	ctx, cancel := context.WithDeadline(context.Background(), chk.probeUntil)
	defer cancel()
	late := "durata della voce non controllata: letture oltre il tempo massimo di " + montage.ProbeTimeout.String()
	if ctx.Err() != nil {
		return late
	}
	voice, err := tool.Probe(ctx, s.Path(sc.Voce))
	if ctx.Err() != nil {
		return late
	}
	if err != nil || voice.Audio == nil {
		return fmt.Sprintf("voce %s illeggibile come audio: giv story cut la rifiuterà", sc.Voce)
	}
	if sceneSeconds == 0 && sc.Tipo == story.Clip {
		if info, err := tool.Probe(ctx, s.Path(sc.File)); err == nil {
			sceneSeconds = info.Duration
		}
	}
	if sceneSeconds > 0 && voice.Duration > sceneSeconds+cutVoiceSlack {
		return fmt.Sprintf("la voce dura %s s, più della scena (%s s): continua sulla scena dopo o viene tagliata a fine montaggio",
			strconv.FormatFloat(round3(voice.Duration), 'f', -1, 64), strconv.FormatFloat(round3(sceneSeconds), 'f', -1, 64))
	}
	return ""
}

// sceneClip è la clip di una scena video generata da giv story run.
// Provvisorio fino al merge di CLI-876: il file di stato di story run
// (story.LoadState, ClipFile) non c'è ancora, quindi la scena risulta da generare.
func sceneClip(s *story.Storyboard, path string, sc *story.Scene) (string, error) {
	return "", errors.New("clip non ancora generata")
}
