package montage

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Esportazione per canale (giv export; da riusare in giv story cut --export):
// un file per canale col profilo fissato qui dalle specifiche ufficiali
// (verifiche V4 di CLI-933): misura, fps, limiti di durata, H.264 con moov in
// testa, AAC a 48 kHz, volume normalizzato in due passate (audio.go). I
// valori che le specifiche non danno sono convenzioni, elencate nel profilo
// (Profile.Convention) e impostabili dove ha senso (formato, volume).

// Canali.
const (
	ChannelInstagram = "instagram"
	ChannelTikTok    = "tiktok"
	ChannelYouTube   = "youtube"
	ChannelLinkedIn  = "linkedin"
)

// Channels sono i canali ammessi, nell'ordine dell'aiuto.
func Channels() []string {
	return []string{ChannelInstagram, ChannelTikTok, ChannelYouTube, ChannelLinkedIn}
}

// Codifica comune delle esportazioni.
const (
	exportFallbackFPS = 30    // fps quando il profilo non tiene quelli del video
	exportShortSide   = 1080  // lato corto di ogni formato (1080x1920, 1920x1080…)
	exportMaxrate     = "20M" // sotto i 25 Mbps di Instagram e i 30 di LinkedIn
	exportBufsize     = "40M"
)

// Profile è il profilo d'uscita di un canale in un formato.
type Profile struct {
	Channel string // ChannelInstagram…
	Aspect  Aspect // formato
	Width   int    // misura d'uscita, pari
	Height  int
	// FPS sono i fotogrammi al secondo d'uscita; 0: quelli del video se sono
	// da MinFPS a MaxFPS, altrimenti 30.
	FPS       int
	MinFPS    int
	MaxFPS    int
	MinS      float64 // durata minima del video in secondi (0: nessuna)
	MaxS      float64 // durata massima in secondi
	AudioKbps int     // AAC, 48 kHz stereo
	// Convention sono i valori del profilo che le specifiche ufficiali non
	// danno (scelte nostre), da mostrare all'utente.
	Convention []string
}

// ChannelProfile è il profilo del canale nel formato aspect (nil: il formato
// predefinito del canale: 9:16 per Instagram e TikTok, 16:9 per YouTube e
// LinkedIn). Canale o formato non ammessi: ErrInvalid.
func ChannelProfile(channel string, aspect *Aspect) (Profile, error) {
	p := Profile{Channel: channel, MinFPS: 10, MaxFPS: 60, AudioKbps: 192}
	switch channel {
	case ChannelInstagram: // Reels: 9:16, 3 s-15 min, AAC ≤128 kbps, 23-60 fps
		p.Aspect, p.FPS, p.MinS, p.MaxS, p.AudioKbps = Aspect9x16, 30, 3, 900, 128
		if aspect != nil && *aspect != Aspect9x16 {
			p.Convention = append(p.Convention, "misure del feed di Instagram ("+aspect.String()+"): non lette nelle specifiche ufficiali")
		}
	case ChannelTikTok: // ≤10 min, 23-60 fps; formato non indicato
		p.Aspect, p.FPS, p.MaxS, p.AudioKbps = Aspect9x16, 30, 600, 128
		p.Convention = append(p.Convention, "formato di TikTok: le specifiche non lo indicano (9:16 come negli annunci)")
	case ChannelYouTube: // 16:9 fino a 15 min (account non verificato); Shorts quadrati o verticali ≤3 min
		p.Aspect, p.MinFPS, p.MaxS = Aspect16x9, 1, 900
		if aspect != nil && *aspect != Aspect16x9 {
			p.MaxS = 180
		}
	case ChannelLinkedIn: // da 1:2,4 a 2,4:1, 3 s-10 min, 10-60 fps
		p.Aspect, p.MinS, p.MaxS = Aspect16x9, 3, 600
	default:
		return Profile{}, invalid("canale %q: ammessi %s", channel, strings.Join(Channels(), ", "))
	}
	if aspect != nil {
		if _, err := ParseAspect(aspect.String()); err != nil {
			return Profile{}, err
		}
		p.Aspect = *aspect
	}
	w, h, err := p.Aspect.Size(exportShortSide, exportShortSide)
	if err != nil {
		return Profile{}, err
	}
	p.Width, p.Height = w, h
	return p, nil
}

// ParseChannels legge l'elenco dei canali di --for: voci separate da
// virgole, ognuna "<canale>" o "<canale>:<formato>" (es.
// "instagram,tiktok,youtube:9:16"). Elenco vuoto, voce vuota, canale o
// formato non ammessi e voce ripetuta: ErrInvalid.
func ParseChannels(spec string) ([]Profile, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, invalid("nessun canale: %s, con un formato facoltativo (es. instagram:4:5)", strings.Join(Channels(), ", "))
	}
	var out []Profile
	seen := map[string]bool{}
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, invalid("canali %q: voce vuota", spec)
		}
		channel, format, hasFormat := strings.Cut(item, ":")
		var aspect *Aspect
		if hasFormat {
			a, err := ParseAspect(format)
			if err != nil {
				return nil, invalid("%s: formato %q: valori ammessi %s", channel, format, aspectNames())
			}
			aspect = &a
		}
		p, err := ChannelProfile(channel, aspect)
		if err != nil {
			return nil, err
		}
		if seen[p.Spec()] {
			return nil, invalid("canale %s ripetuto", p.Spec())
		}
		seen[p.Spec()] = true
		out = append(out, p)
	}
	return out, nil
}

// Spec è il canale col formato, come in --for ("instagram:9:16").
func (p Profile) Spec() string { return p.Channel + ":" + p.Aspect.String() }

// Tag è il canale col formato nei nomi dei file ("instagram-9x16").
func (p Profile) Tag() string { return p.Channel + "-" + p.Aspect.Tag() }

// CheckDuration controlla la durata del video (secondi, al millesimo) contro
// i limiti del canale; una durata non misurata è ErrInvalid.
func (p Profile) CheckDuration(d float64) error {
	if !finite(d) || d <= 0 {
		return invalid("%s: durata del video non misurata, non posso controllarla coi limiti del canale", p.Spec())
	}
	d = math.Round(d*1000) / 1000
	if d < p.MinS {
		return invalid("%s: il video dura %s s, il canale vuole almeno %s s", p.Spec(), num(d), num(p.MinS))
	}
	if d > p.MaxS {
		return invalid("%s: il video dura %s s, il canale ammette al più %s s", p.Spec(), num(d), num(p.MaxS))
	}
	return nil
}

// OutFPS sono gli fps d'uscita per un video a src fps: 0 vuol dire che
// restano quelli del video.
func (p Profile) OutFPS(src float64) int {
	if p.FPS > 0 {
		return p.FPS
	}
	if finite(src) && src >= float64(p.MinFPS) && src <= float64(p.MaxFPS) {
		return 0
	}
	return exportFallbackFPS
}

// Export è un'esportazione: il profilo, come adattare il formato e il volume.
type Export struct {
	Profile Profile
	Fit     string  // FitCrop, FitPad o FitBlur
	Focus   *Focus  // solo FitCrop; nil = FocusCenter
	SrcFPS  float64 // fps misurati del video (Info.Video.FPS)
	Audio   bool    // il video ha una traccia audio (Info.Audio)
	// Loudness è il volume finale in LUFS integrati (CheckLoudness); nil: il
	// volume resta com'è.
	Loudness *float64
}

// reframe è il cambio formato dell'esportazione.
func (e Export) reframe() Reframe {
	return Reframe{Width: e.Profile.Width, Height: e.Profile.Height, Fit: e.Fit, Focus: e.Focus}
}

// Check controlla profilo, adattamento, fuoco e volume.
func (e Export) Check() error {
	p := e.Profile
	if p.Channel == "" || p.MaxS <= 0 || p.AudioKbps <= 0 || p.AudioKbps > 512 || p.FPS < 0 || p.FPS > maxFPS {
		return invalid("profilo d'esportazione %q incompleto", p.Channel)
	}
	if err := e.reframe().Check(); err != nil {
		return err
	}
	if e.Loudness != nil {
		return CheckLoudness(*e.Loudness)
	}
	return nil
}

// ExportArgs sono gli argomenti di ffmpeg (senza il programma)
// dell'esportazione di src in dst (.mp4): cambio formato (Reframe.Graph),
// fps del profilo, H.264 High con tetto di flusso, AAC 48 kHz stereo della
// prima traccia audio (se Audio), normalizzato con la misura measured della
// prima passata (LoudnessArgs; nil: volume com'è), moov in testa.
func ExportArgs(src, dst string, e Export, measured *Loudness) ([]string, error) {
	if err := e.Check(); err != nil {
		return nil, err
	}
	fps := e.Profile.OutFPS(e.SrcFPS)
	label := "v"
	if fps > 0 {
		label = "rf"
	}
	g, err := e.reframe().Graph("0:v:0", label)
	if err != nil {
		return nil, err
	}
	if fps > 0 {
		g += fmt.Sprintf(";[rf]fps=%d[v]", fps)
	}
	maps := []string{"-map", "[v]"}
	if e.Audio {
		if e.Loudness != nil && measured != nil {
			g += ";[0:a:0]" + LoudnormFilter(*e.Loudness, measured) + "[a]"
			maps = append(maps, "-map", "[a]")
		} else {
			maps = append(maps, "-map", "0:a:0")
		}
	}
	in, err := inputArgs(src, videoFormats)
	if err != nil {
		return nil, err
	}
	out, err := mp4Out(dst)
	if err != nil {
		return nil, err
	}
	args := append(ffArgs(), in...)
	args = append(args, "-filter_complex", g)
	args = append(args, maps...)
	args = append(args, h264Args()...)
	args = append(args, "-profile:v", "high", "-maxrate", exportMaxrate, "-bufsize", exportBufsize)
	if e.Audio {
		args = append(args, "-c:a", "aac", "-b:a", strconv.Itoa(e.Profile.AudioKbps)+"k", "-ar", strconv.Itoa(sampleRate), "-ac", "2")
	}
	return append(args, out...), nil
}

// ExportCache tiene la misura del volume fra più esportazioni dello stesso
// video (una prima passata sola per tutti i canali); la zero value è vuota.
type ExportCache struct {
	src    string
	target float64
	done   bool
	l      *Loudness
	// Muted: la misura ha trovato un audio muto, esportato senza normalizzare.
	Muted bool
}

// measure è la misura del volume di src per target, dalla cache se c'è.
func (c *ExportCache) measure(w *Work, src string, target float64) (*Loudness, error) {
	if c != nil && c.done && c.src == src && c.target == target {
		return c.l, nil
	}
	args, err := LoudnessArgs(src, target)
	if err != nil {
		return nil, err
	}
	l, err := w.MeasureLoudness(args)
	muted := false
	if errors.Is(err, ErrInvalid) { // audio muto: niente da normalizzare
		l, err, muted = nil, nil, true
	}
	if err != nil {
		return nil, err
	}
	if c != nil {
		*c = ExportCache{src: src, target: target, done: true, l: l, Muted: muted}
	}
	return l, nil
}

// ExportBuild è la Build di un'operazione (Op.Build, Tool.RunOp) che esporta
// src col profilo di e: con Loudness e una traccia audio prima misura il
// volume (una volta per tutte le esportazioni che condividono cache, che può
// essere nil), poi scrive staged con ExportArgs.
func ExportBuild(src string, e Export, cache *ExportCache) func(w *Work, staged string) error {
	return func(w *Work, staged string) error {
		if err := e.Check(); err != nil {
			return err
		}
		var measured *Loudness
		if e.Audio && e.Loudness != nil {
			l, err := cache.measure(w, src, *e.Loudness)
			if err != nil {
				return err
			}
			measured = l
		}
		args, err := ExportArgs(src, staged, e, measured)
		if err != nil {
			return err
		}
		return w.FFmpeg(args)
	}
}
