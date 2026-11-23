package montage

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"math"
	"strconv"
)

// Fotogrammi per il giudizio di aderenza (giv qc --judge, controllo.aderenza
// di giv story): pochi fotogrammi del video e l'eventuale immagine di
// riferimento, ricodificati da ffmpeg in JPEG col lato lungo entro un
// massimo, senza metadati (niente EXIF né posizione: al provider vanno solo i
// pixel), in memoria. Nessun file scritto.

const (
	// JudgeFrameCount sono i fotogrammi estratti dal video.
	JudgeFrameCount = 4
	// JudgeMaxSide è il lato lungo massimo, in pixel, delle immagini inviate.
	JudgeMaxSide = 768
	// judgeMaxBytes è il massimo di un'immagine ricodificata.
	judgeMaxBytes = 1 << 20
)

// Still è un'immagine pronta per l'invio: JPEG senza metadati.
type Still struct {
	TimeS  float64 // istante del fotogramma nel video (0 per un'immagine)
	Width  int
	Height int
	Data   []byte
}

// judgeScale riduce l'immagine entro max pixel sul lato lungo (mai
// ingrandita), con misure pari, in un formato che il codificatore JPEG accetta.
func judgeScale(max int) string {
	m := strconv.Itoa(max)
	return "scale=w='min(" + m + ",iw)':h='min(" + m + ",ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,format=yuvj420p"
}

// judgeArgs sono gli argomenti di ffmpeg che scrivono su stdout un solo
// fotogramma JPEG di path (alla posizione at in secondi se at >= 0), senza
// metadati, letto coi formati indicati.
func judgeArgs(path, formats string, at float64, max int) ([]string, error) {
	var opts []string
	if at >= 0 {
		opts = []string{"-ss", num(at)}
	}
	in, err := inputArgs(path, formats, opts...)
	if err != nil {
		return nil, err
	}
	args := append(ffArgs(), in...)
	return append(args, "-map", "0:v:0", "-frames:v", "1", "-an", "-sn", "-dn",
		"-map_metadata", "-1", "-map_chapters", "-1", "-vf", judgeScale(max),
		"-c:v", "mjpeg", "-q:v", "3", "-f", "image2pipe", "pipe:1"), nil
}

// judgeTimes sono gli istanti dei fotogrammi: al centro di n parti uguali
// della durata (né il primo né l'ultimo fotogramma, spesso in dissolvenza).
func judgeTimes(duration float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.Round(duration*(float64(i)+0.5)/float64(n)*1000) / 1000
	}
	return out
}

// JudgeFrames estrae dal video path (info: la sua lettura con Probe)
// JudgeFrameCount fotogrammi a istanti equidistanti, in JPEG col lato lungo
// entro JudgeMaxSide. Ogni estrazione entro ProbeTimeout.
func (t Tool) JudgeFrames(ctx context.Context, path string, info *Info) ([]Still, error) {
	if info == nil || info.Video == nil {
		return nil, invalid("%s: nessuna traccia video da giudicare", path)
	}
	if info.Duration <= 0 || math.IsNaN(info.Duration) || math.IsInf(info.Duration, 0) {
		return nil, invalid("%s: durata del video non letta: fotogrammi non estratti", path)
	}
	if err := regularFile(path); err != nil {
		return nil, invalid("%v", err)
	}
	bin, err := t.bin("ffmpeg", t.FFmpeg)
	if err != nil {
		return nil, err
	}
	var out []Still
	for _, at := range judgeTimes(info.Duration, JudgeFrameCount) {
		args, err := judgeArgs(path, videoFormats, at, JudgeMaxSide)
		if err != nil {
			return nil, err
		}
		st, err := t.judgeStill(ctx, bin, args)
		if err != nil {
			return nil, fmt.Errorf("fotogramma a %s s di %s: %w", num(at), path, err)
		}
		st.TimeS = at
		out = append(out, *st)
	}
	return out, nil
}

// JudgeImage ricodifica l'immagine path (png, jpg, webp) come i fotogrammi:
// JPEG col lato lungo entro JudgeMaxSide, senza metadati.
func (t Tool) JudgeImage(ctx context.Context, path string) (*Still, error) {
	if !imagePath(path) {
		return nil, invalid("%s: il riferimento è un'immagine .png, .jpg o .webp", path)
	}
	if err := regularFile(path); err != nil {
		return nil, invalid("%v", err)
	}
	bin, err := t.bin("ffmpeg", t.FFmpeg)
	if err != nil {
		return nil, err
	}
	args, err := judgeArgs(path, imageFormats, -1, JudgeMaxSide)
	if err != nil {
		return nil, err
	}
	st, err := t.judgeStill(ctx, bin, args)
	if err != nil {
		return nil, fmt.Errorf("riferimento %s: %w", path, err)
	}
	return st, nil
}

// judgeStill lancia ffmpeg e controlla che lo stdout sia un solo JPEG con
// misure entro JudgeMaxSide.
func (t Tool) judgeStill(ctx context.Context, bin string, args []string) (*Still, error) {
	data, err := run(ctx, bin, t.probeTimeout(), args)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, invalid("nessun fotogramma video")
	}
	if len(data) > judgeMaxBytes {
		return nil, invalid("immagine ricodificata oltre %d KiB", judgeMaxBytes>>10)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, invalid("immagine ricodificata non leggibile: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
		return nil, invalid("immagine ricodificata non leggibile: %v", err)
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > JudgeMaxSide || cfg.Height > JudgeMaxSide {
		return nil, invalid("immagine ricodificata di %dx%d: fuori misura", cfg.Width, cfg.Height)
	}
	return &Still{Width: cfg.Width, Height: cfg.Height, Data: data}, nil
}
