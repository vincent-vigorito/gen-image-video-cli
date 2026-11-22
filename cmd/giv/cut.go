package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	cutVoiceSlack  = 0.05 // secondi di sovrapposizione della voce tollerati senza avviso
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
	var st *story.State // stato di giv story run, letto alla prima scena video
	for i := range s.Scene {
		sc := &s.Scene[i]
		c := montage.Clip{Duration: float64(sc.Durata), Transition: trans}
		switch sc.Tipo {
		case story.Clip:
			c.Path = s.Path(sc.File)
		case story.Immagine:
			c.Path, c.Image = s.Path(sc.File), true
		case story.Video:
			if st == nil {
				var err error
				if st, err = story.LoadState(story.StatePath(path)); err != nil {
					return p, usageError("%s: %v", path, err)
				}
			}
			clip, err := st.ClipFile(s, sc.ID)
			if err != nil {
				missing = append(missing, sceneMissing(st, sc.ID, err))
				continue
			}
			c.Path = clip
		default:
			return p, usageError("%s: scena %s: tipo %q non montabile", path, sc.ID, sc.Tipo)
		}
		if sc.Voce != "" {
			c.Voice, c.VoiceAt = s.Path(sc.Voce), sc.Ritardo()
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

// voiceOverlaps controlla la voce di ogni scena sulla linea del tempo del
// montaggio (durate delle scene, dissolvenze, ritardo_voce): deve finire prima
// che parta la voce della scena successiva che ne ha una, l'ultima prima della
// fine del montaggio. Ogni avviso va nella scena e su stderr; una voce che
// supera la sua scena senza toccarne un'altra non ne ha. out sono tutte le
// scene dello storyboard, nello stesso ordine. Le letture con ffprobe stanno
// tutte entro montage.ProbeTimeout.
func (chk *storyChecker) voiceOverlaps(out *storyCheck) {
	s := chk.s
	warn := func(i int, w string) {
		out.Scenes[i].Warnings = append(out.Scenes[i].Warnings, w)
		fmt.Fprintf(os.Stderr, "avviso: scena %s: %s\n", s.Scene[i].ID, w)
	}
	var voiced []int
	for i := range s.Scene {
		if s.Scene[i].Voce != "" {
			voiced = append(voiced, i)
		}
	}
	if len(voiced) == 0 {
		return
	}
	tool := montage.Tool{}
	if err := tool.Check(); err != nil {
		for _, i := range voiced {
			warn(i, "durata della voce non controllata: ffprobe non trovato")
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), montage.ProbeTimeout)
	defer cancel()
	n := len(s.Scene)
	voiceLen := make([]float64, n)
	for _, i := range voiced {
		voiceLen[i] = math.NaN()
		var info *montage.Info
		var err error
		if ctx.Err() == nil {
			info, err = tool.Probe(ctx, s.Path(s.Scene[i].Voce))
		}
		switch {
		case ctx.Err() != nil:
			warn(i, "durata della voce non controllata: letture oltre il tempo massimo di "+montage.ProbeTimeout.String())
		case err != nil || info.Audio == nil:
			warn(i, fmt.Sprintf("voce %s illeggibile come audio: giv story cut la rifiuterà", s.Scene[i].Voce))
		default:
			voiceLen[i] = info.Duration
		}
	}
	// inizio di ogni scena nel montaggio, starts[n] è la fine; NaN da una
	// scena di durata ignota in poi (nessun confronto)
	fade := 0.0
	if m := s.Montaggio; m != nil && m.Transizione == "dissolvenza" {
		fade = cutFadeSeconds
	}
	starts := make([]float64, n+1)
	for i := range s.Scene {
		l := out.Scenes[i].length
		if l == 0 && s.Scene[i].Tipo == story.Clip && ctx.Err() == nil {
			if info, err := tool.Probe(ctx, s.Path(s.Scene[i].File)); err == nil {
				l = info.Duration
			}
		}
		if l <= 0 {
			l = math.NaN()
		}
		starts[i+1] = starts[i] + l
		if i+1 < n {
			starts[i+1] -= fade
		}
	}
	secs := func(v float64) string { return strconv.FormatFloat(round3(v), 'f', -1, 64) }
	for k, i := range voiced {
		end := starts[i] + s.Scene[i].Ritardo() + voiceLen[i]
		if k+1 < len(voiced) {
			j := voiced[k+1]
			next := starts[j] + s.Scene[j].Ritardo()
			if d := end - next; d > cutVoiceSlack {
				warn(i, fmt.Sprintf("la voce si sovrappone di %s s a quella della scena %s: finisce a %s s del montaggio, quella di %s parte a %s s",
					secs(d), s.Scene[j].ID, secs(end), s.Scene[j].ID, secs(next)))
			}
		} else if d := end - starts[n]; d > cutVoiceSlack {
			warn(i, fmt.Sprintf("la voce supera di %s s la fine del montaggio e viene tagliata: finisce a %s s, il montaggio dura %s s",
				secs(d), secs(end), secs(starts[n])))
		}
	}
}

// sceneMissing descrive una scena video senza clip da montare: l'errore di
// ClipFile (che nomina la scena) e lo stato registrato da giv story run, se ce
// n'è uno; l'errore del provider resta nello stato, non si ristampa.
func sceneMissing(st *story.State, id string, err error) string {
	msg := err.Error()
	if sc := st.Scenes[id]; sc != nil && sc.Status != "" && sc.Status != story.Fatto {
		msg += " (stato: " + strings.ReplaceAll(sc.Status, "_", " ") + ")"
	}
	return msg
}
