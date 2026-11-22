package montage

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Strumenti di servizio (giv sheet, giv frame, giv gif): foglio di contatto,
// fotogramma singolo e GIF animata, con le regole delle operazioni locali
// (op.go). I costruttori degli argomenti (SheetCellArgs, SheetArgs, FrameArgs,
// GIFPaletteArgs, GIFArgs) sono funzioni pure; i Build, da usare come
// Op.Build, scrivono solo in staged e nella cartella privata del lavoro. Le
// etichette passano solo dal file .ass generato da ASS.

// Limiti del foglio di contatto.
const (
	SheetCellSide  = 320 // lato lungo di un riquadro, in pixel
	MaxSheetCols   = 12  // colonne della griglia
	MaxSheetCells  = 200 // riquadri del foglio
	MaxSheetFrames = 100 // fotogrammi di un video
)

// labelEnd è la fine, in secondi, dell'etichetta nel file .ass di un
// riquadro: copre l'unico fotogramma, che parte da 0.
const labelEnd = 3600

// endMargin è quanto si resta prima della fine misurata di un video: la
// durata del contenitore può superare quella della traccia video (audio più
// lungo), e un istante oltre l'ultimo fotogramma non dà nessuna immagine.
const endMargin = 0.25

// SheetCell è un riquadro del foglio di contatto.
type SheetCell struct {
	Path   string  // video o immagine
	Image  bool    // immagine ferma: se ne legge l'unico fotogramma
	At     float64 // video: istante del fotogramma, in secondi
	Label  string  // etichetta resa alla lettera nel riquadro; vuota: nessuna
	NewRow bool    // il riquadro comincia una riga nuova (una riga per file o per scena)
}

// Sheet è un foglio di contatto: riquadri tutti di CellWidth×CellHeight
// pixel in una griglia di al più Cols colonne, nell'ordine di Cells; un
// riquadro con NewRow, o oltre l'ultima colonna, va a capo. I posti vuoti
// restano neri.
type Sheet struct {
	Cells      []SheetCell
	Cols       int
	CellWidth  int // pari
	CellHeight int // pari
}

// SheetCellSize è la misura dei riquadri per un video w×h: il lato lungo è
// SheetCellSide, l'altro in proporzione (pari, almeno 16); misure ignote:
// 16:9.
func SheetCellSize(w, h int) (cw, ch int) {
	if w <= 0 || h <= 0 {
		w, h = 16, 9
	}
	short := func(a, b int) int {
		return max(int(math.Round(float64(SheetCellSide)*float64(a)/float64(b)/2))*2, 16)
	}
	if w >= h {
		return SheetCellSide, short(h, w)
	}
	return short(w, h), SheetCellSide
}

// SheetTimes sono gli istanti dei fotogrammi di un video lungo duration
// secondi a fps fotogrammi al secondo (0: 25): frames fotogrammi al centro di
// intervalli uguali oppure, con every > 0, uno ogni every secondi da 0. Gli
// istanti restano prima dell'ultimo fotogramma; quelli ripetuti (video molto
// corti) si contano una volta.
func SheetTimes(duration, fps, every float64, frames int) ([]float64, error) {
	if !finite(duration) || duration <= 0 || duration > 24*3600 {
		return nil, invalid("durata del video non misurata")
	}
	if !finite(fps) || fps <= 0 {
		fps = 25
	}
	last := math.Max(0, duration-math.Max(2/fps, endMargin))
	var ts []float64
	add := func(t float64) {
		t = math.Round(math.Min(t, last)*1000) / 1000
		if len(ts) == 0 || ts[len(ts)-1] != t {
			ts = append(ts, t)
		}
	}
	switch {
	case every != 0 && frames != 0:
		return nil, invalid("fotogrammi: un intervallo oppure un numero, non entrambi")
	case every != 0:
		if !finite(every) || every <= 0 || every > 3600 {
			return nil, invalid("intervallo di %v s: da più di 0 a 3600 secondi", every)
		}
		if duration/every >= MaxSheetFrames {
			return nil, invalid("un fotogramma ogni %s s su %s s sono più di %d fotogrammi: allunga l'intervallo",
				num(every), num(duration), MaxSheetFrames)
		}
		for k := 0; float64(k)*every < duration; k++ {
			add(float64(k) * every)
		}
	case frames > 0:
		if frames > MaxSheetFrames {
			return nil, invalid("%d fotogrammi: al più %d", frames, MaxSheetFrames)
		}
		for i := range frames {
			add(duration * (float64(i) + 0.5) / float64(frames))
		}
	default:
		return nil, invalid("fotogrammi: serve un intervallo o un numero")
	}
	return ts, nil
}

// Check controlla il foglio: colonne, misure dei riquadri, numero di
// riquadri, percorsi, istanti e misure finali (ErrInvalid).
func (s Sheet) Check() error {
	if s.Cols < 1 || s.Cols > MaxSheetCols {
		return invalid("foglio: %d colonne, da 1 a %d", s.Cols, MaxSheetCols)
	}
	for _, v := range []int{s.CellWidth, s.CellHeight} {
		if v < 16 || v > maxSide || v%2 != 0 {
			return invalid("foglio: riquadri di %dx%d pixel (pari, da 16 a %d)", s.CellWidth, s.CellHeight, maxSide)
		}
	}
	if len(s.Cells) == 0 {
		return invalid("foglio: nessun riquadro")
	}
	if len(s.Cells) > MaxSheetCells {
		return invalid("foglio: %d riquadri, al più %d: chiedi meno fotogrammi", len(s.Cells), MaxSheetCells)
	}
	for i, c := range s.Cells {
		if err := checkPath(c.Path); err != nil {
			return invalid("riquadro %d: %v", i+1, err)
		}
		if !c.Image && (!finite(c.At) || c.At < 0 || c.At > 24*3600) {
			return invalid("riquadro %d: istante %v non valido", i+1, c.At)
		}
	}
	if _, _, w, h := s.layout(); w > maxSide || h > maxSide {
		return invalid("foglio di %dx%d pixel, oltre %d per lato: chiedi meno fotogrammi o meno colonne", w, h, maxSide)
	}
	return nil
}

// Size sono le misure del foglio finito, in pixel.
func (s Sheet) Size() (w, h int) {
	_, _, w, h = s.layout()
	return w, h
}

// Rows è il numero di righe del foglio.
func (s Sheet) Rows() int {
	_, h := s.Size()
	if s.CellHeight <= 0 {
		return 0
	}
	return h / s.CellHeight
}

// Inputs sono i file letti dal foglio, ognuno una volta, nell'ordine (per Op.Inputs).
func (s Sheet) Inputs() []string {
	seen := map[string]bool{}
	var in []string
	for _, c := range s.Cells {
		if !seen[c.Path] {
			seen[c.Path] = true
			in = append(in, c.Path)
		}
	}
	return in
}

// layout è la posizione in pixel di ogni riquadro e la misura del foglio.
func (s Sheet) layout() (xs, ys []int, w, h int) {
	cols := max(s.Cols, 1)
	col, row := 0, 0
	for i, c := range s.Cells {
		if i > 0 && (c.NewRow || col == cols) {
			row, col = row+1, 0
		}
		x, y := col*s.CellWidth, row*s.CellHeight
		xs, ys = append(xs, x), append(ys, y)
		w, h = max(w, x+s.CellWidth), max(h, y+s.CellHeight)
		col++
	}
	return xs, ys, w, h
}

// sheetLabel è il file .ass dell'etichetta di un riquadro cw×ch: in basso a
// sinistra, testo bianco con bordo nero, reso alla lettera.
func sheetLabel(cw, ch int, label string) ([]byte, error) {
	size := max(min(cw, ch)/9, 12)
	st := TextStyle{Size: size, Color: White, Outline: Black, Border: math.Max(1, math.Round(float64(size)/12))}
	m := Margins{Top: 0.04, Bottom: 0.04, Left: 0.04, Right: 0.04}
	return ASS(cw, ch, m, []Text{{Text: label, Start: 0, End: labelEnd, Pos: BottomLeft, Style: st}})
}

// imageOut sono codifica e formato di un'immagine d'uscita (.png o .jpg).
func imageOut(out string) ([]string, error) {
	o, err := outputArg(out)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(out)) {
	case ".png":
		return []string{"-update", "1", "-c:v", "png", "-f", "image2", o}, nil
	case ".jpg", ".jpeg":
		return []string{"-update", "1", "-c:v", "mjpeg", "-q:v", "3", "-f", "image2", o}, nil
	}
	return nil, invalid("uscita %s: serve un .png o un .jpg", out)
}

// SheetCellArgs sono gli argomenti di ffmpeg che scrivono in out (.png) il
// riquadro i: il fotogramma (l'istante At del video, o l'immagine) ridotto
// dentro il riquadro con bande nere e, se assPath non è vuoto, l'etichetta
// incisa dal file .ass (scritto da sheetLabel).
func SheetCellArgs(s Sheet, i int, assPath, out string) ([]string, error) {
	if err := s.Check(); err != nil {
		return nil, err
	}
	if i < 0 || i >= len(s.Cells) {
		return nil, invalid("riquadro %d inesistente", i+1)
	}
	c := s.Cells[i]
	var in []string
	var err error
	if c.Image {
		in, err = inputArgs(c.Path, imageFormats)
	} else {
		in, err = inputArgs(c.Path, videoFormats, "-ss", num(c.At))
	}
	if err != nil {
		return nil, err
	}
	cw, ch := s.CellWidth, s.CellHeight
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease:force_divisible_by=2,"+
		"pad=%d:%d:(ow-iw)/2:(oh-ih)/2:color=black,setsar=1,setpts=PTS-STARTPTS", cw, ch, cw, ch)
	if assPath != "" {
		f, err := ASSFilter(assPath, "")
		if err != nil {
			return nil, err
		}
		vf += "," + f
	}
	if strings.ToLower(filepath.Ext(out)) != ".png" {
		return nil, invalid("riquadro %s: serve un .png", out)
	}
	o, err := imageOut(out)
	if err != nil {
		return nil, err
	}
	args := append(ffArgs(), in...)
	args = append(args, "-map", "0:v:0", "-vf", vf, "-frames:v", "1")
	return append(args, o...), nil
}

// SheetArgs sono gli argomenti di ffmpeg che compongono in out (.jpg o .png)
// i riquadri già scritti (cells, uno per riquadro di s, nell'ordine) nella
// griglia del foglio.
func SheetArgs(s Sheet, cells []string, out string) ([]string, error) {
	if err := s.Check(); err != nil {
		return nil, err
	}
	if len(cells) != len(s.Cells) {
		return nil, invalid("foglio: %d file per %d riquadri", len(cells), len(s.Cells))
	}
	args := ffArgs()
	for _, f := range cells {
		in, err := inputArgs(f, imageFormats)
		if err != nil {
			return nil, err
		}
		args = append(args, in...)
	}
	var fc strings.Builder
	if len(cells) == 1 {
		fc.WriteString("[0:v]null[v]")
	} else {
		xs, ys, _, _ := s.layout()
		var layout []string
		for i := range cells {
			fmt.Fprintf(&fc, "[%d:v]", i)
			layout = append(layout, strconv.Itoa(xs[i])+"_"+strconv.Itoa(ys[i]))
		}
		fmt.Fprintf(&fc, "xstack=inputs=%d:layout=%s:fill=black[v]", len(cells), strings.Join(layout, "|"))
	}
	o, err := imageOut(out)
	if err != nil {
		return nil, err
	}
	args = append(args, "-filter_complex", fc.String(), "-map", "[v]", "-frames:v", "1")
	return append(args, o...), nil
}

// Build scrive il foglio in staged (Op.Build): un riquadro alla volta nella
// cartella privata (etichette comprese), poi la griglia.
func (s Sheet) Build(w *Work, staged string) error {
	if err := s.Check(); err != nil {
		return err
	}
	files := make([]string, len(s.Cells))
	for i, c := range s.Cells {
		ass := ""
		if c.Label != "" {
			data, err := sheetLabel(s.CellWidth, s.CellHeight, c.Label)
			if err != nil {
				return err
			}
			if ass, err = w.WriteFile("etichetta-"+strconv.Itoa(i+1)+".ass", data); err != nil {
				return err
			}
		}
		out, err := w.Path("riquadro-" + strconv.Itoa(i+1) + ".png")
		if err != nil {
			return err
		}
		args, err := SheetCellArgs(s, i, ass, out)
		if err != nil {
			return err
		}
		if err := w.FFmpeg(args); err != nil {
			return fmt.Errorf("riquadro %d (%s): %w", i+1, filepath.Base(c.Path), err)
		}
		if !written(out) {
			if c.Image {
				return fmt.Errorf("riquadro %d: nessuna immagine da %s", i+1, c.Path)
			}
			return fmt.Errorf("riquadro %d: nessun fotogramma di %s a %s s", i+1, c.Path, num(c.At))
		}
		files[i] = out
	}
	args, err := SheetArgs(s, files, staged)
	if err != nil {
		return err
	}
	return w.FFmpeg(args)
}

// written: path è un file normale non vuoto.
func written(path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode().IsRegular() && st.Size() > 0
}

// Frame è un fotogramma di un video, in .png o .jpg.
type Frame struct {
	Video string
	At    float64 // secondi dall'inizio (senza Last)
	Last  bool    // l'ultimo fotogramma
	Width int     // larghezza in pixel (la miniatura), altezza in proporzione; 0: quella del video
}

// Check controlla istante e larghezza (ErrInvalid).
func (f Frame) Check() error {
	if err := checkPath(f.Video); err != nil {
		return invalid("video: %v", err)
	}
	if !f.Last && (!finite(f.At) || f.At < 0 || f.At > 24*3600) {
		return invalid("istante %v non valido", f.At)
	}
	if f.Width != 0 && (f.Width < 16 || f.Width > maxSide) {
		return invalid("larghezza %d: da 16 a %d pixel", f.Width, maxSide)
	}
	return nil
}

// FrameArgs sono gli argomenti di ffmpeg che scrivono in out il fotogramma:
// all'istante At (il primo da lì) o, con Last, l'ultimo (dall'ultimo
// secondo, ogni fotogramma riscrive il file: resta l'ultimo).
func FrameArgs(f Frame, out string) ([]string, error) {
	if err := f.Check(); err != nil {
		return nil, err
	}
	seek := []string{"-ss", num(f.At)}
	if f.Last {
		seek = []string{"-sseof", "-1"}
	}
	in, err := inputArgs(f.Video, videoFormats, seek...)
	if err != nil {
		return nil, err
	}
	o, err := imageOut(out)
	if err != nil {
		return nil, err
	}
	args := append(ffArgs(), in...)
	args = append(args, "-map", "0:v:0")
	if f.Width > 0 {
		args = append(args, "-vf", "scale="+strconv.Itoa(f.Width)+":-1:flags=lanczos")
	}
	if !f.Last {
		args = append(args, "-frames:v", "1")
	}
	return append(args, o...), nil
}

// Build scrive il fotogramma in staged (Op.Build).
func (f Frame) Build(w *Work, staged string) error {
	args, err := FrameArgs(f, staged)
	if err != nil {
		return err
	}
	if err := w.FFmpeg(args); err != nil {
		return err
	}
	if !written(staged) {
		if f.Last {
			return fmt.Errorf("%s: nessun fotogramma letto", f.Video)
		}
		return fmt.Errorf("%s: nessun fotogramma a %s s (oltre l'ultimo?)", f.Video, num(f.At))
	}
	return nil
}

// Limiti della GIF.
const (
	MaxGIFSeconds = 60   // durata
	MaxGIFWidth   = 1920 // larghezza in pixel
	MaxGIFFPS     = 30   // fotogrammi al secondo
)

// GIF è una GIF animata da un tratto di video, con la palette calcolata in
// una prima passata.
type GIF struct {
	Video string
	Start float64 // secondi dall'inizio del video
	End   float64 // secondi, oltre Start
	Width int     // larghezza in pixel, altezza in proporzione
	FPS   int     // fotogrammi al secondo
}

// Check controlla tratto, larghezza e fps (ErrInvalid).
func (g GIF) Check() error {
	if err := checkPath(g.Video); err != nil {
		return invalid("video: %v", err)
	}
	if !finite(g.Start) || !finite(g.End) || g.Start < 0 || g.End <= g.Start || g.End > 24*3600 {
		return invalid("tratto da %v a %v s non valido", g.Start, g.End)
	}
	if g.End-g.Start > MaxGIFSeconds {
		return invalid("tratto di %s s: una GIF dura al più %d s", num(g.End-g.Start), MaxGIFSeconds)
	}
	if g.Width < 16 || g.Width > MaxGIFWidth {
		return invalid("larghezza %d: da 16 a %d pixel", g.Width, MaxGIFWidth)
	}
	if g.FPS < 1 || g.FPS > MaxGIFFPS {
		return invalid("%d fotogrammi al secondo: da 1 a %d", g.FPS, MaxGIFFPS)
	}
	return nil
}

// gifScale è il filtro comune alle due passate: fotogrammi al secondo e larghezza.
func (g GIF) gifScale() string {
	return "fps=" + strconv.Itoa(g.FPS) + ",scale=" + strconv.Itoa(g.Width) + ":-1:flags=lanczos"
}

// gifInput è il video in ingresso, limitato al tratto.
func (g GIF) gifInput() ([]string, error) {
	return inputArgs(g.Video, videoFormats, "-ss", num(g.Start), "-t", num(g.End-g.Start))
}

// GIFPaletteArgs sono gli argomenti di ffmpeg della prima passata: la
// palette del tratto in palette (.png).
func GIFPaletteArgs(g GIF, palette string) ([]string, error) {
	if err := g.Check(); err != nil {
		return nil, err
	}
	if strings.ToLower(filepath.Ext(palette)) != ".png" {
		return nil, invalid("palette %s: serve un .png", palette)
	}
	in, err := g.gifInput()
	if err != nil {
		return nil, err
	}
	o, err := imageOut(palette)
	if err != nil {
		return nil, err
	}
	args := append(ffArgs(), in...)
	args = append(args, "-map", "0:v:0", "-vf", g.gifScale()+",palettegen=stats_mode=diff", "-frames:v", "1")
	return append(args, o...), nil
}

// GIFArgs sono gli argomenti di ffmpeg della seconda passata: la GIF in out
// (.gif) con la palette della prima, ripetuta all'infinito.
func GIFArgs(g GIF, palette, out string) ([]string, error) {
	if err := g.Check(); err != nil {
		return nil, err
	}
	if strings.ToLower(filepath.Ext(out)) != ".gif" {
		return nil, invalid("uscita %s: serve un .gif", out)
	}
	in, err := g.gifInput()
	if err != nil {
		return nil, err
	}
	pal, err := inputArgs(palette, imageFormats)
	if err != nil {
		return nil, err
	}
	o, err := outputArg(out)
	if err != nil {
		return nil, err
	}
	args := append(ffArgs(), in...)
	args = append(args, pal...)
	args = append(args, "-filter_complex",
		"[0:v]"+g.gifScale()+"[x];[x][1:v]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle[v]",
		"-map", "[v]", "-loop", "0", "-f", "gif", o)
	return args, nil
}

// Build scrive la GIF in staged (Op.Build): palette nella cartella privata,
// poi la GIF.
func (g GIF) Build(w *Work, staged string) error {
	palette, err := w.Path("palette.png")
	if err != nil {
		return err
	}
	args, err := GIFPaletteArgs(g, palette)
	if err != nil {
		return err
	}
	if err := w.FFmpeg(args); err != nil {
		return fmt.Errorf("palette: %w", err)
	}
	if !written(palette) {
		return fmt.Errorf("%s: nessun fotogramma fra %s e %s s", g.Video, num(g.Start), num(g.End))
	}
	args, err = GIFArgs(g, palette, staged)
	if err != nil {
		return err
	}
	return w.FFmpeg(args)
}
