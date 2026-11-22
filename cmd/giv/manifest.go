package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"slices"
	"strings"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/presets"
	"gen-image-video-cli/internal/provider"
)

// inputFile è un file in ingresso nel manifest: il percorso come l'ha passato
// l'utente (gli URL ridotti a schema e host) e l'impronta breve del contenuto.
type inputFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"` // primi 12 caratteri esadecimali
}

// manifestInputs sono i file in ingresso di un video, per ruolo.
type manifestInputs struct {
	Image     *inputFile  `json:"image,omitempty"`
	LastFrame *inputFile  `json:"last_frame,omitempty"`
	Refs      []inputFile `json:"refs,omitempty"`
	Keyframes []inputFile `json:"keyframes,omitempty"` // fotogrammi intermedi

	// comandi sulle clip (giv extend, edit, upscale, avatar)
	Video *inputFile `json:"video,omitempty"`
	Audio *inputFile `json:"audio,omitempty"`
	From  string     `json:"from,omitempty"` // job di partenza
}

// newManifestInputs prende gli ingressi di req prima dei download; nil se non ce ne sono.
func newManifestInputs(req provider.VideoRequest) *manifestInputs {
	in := &manifestInputs{}
	if req.Image != "" {
		in.Image = &inputFile{Path: shownInput(req.Image)}
	}
	if req.LastFrame != "" {
		in.LastFrame = &inputFile{Path: shownInput(req.LastFrame)}
	}
	for _, r := range req.Refs {
		in.Refs = append(in.Refs, inputFile{Path: shownInput(r)})
	}
	for _, k := range req.Keyframes {
		in.Keyframes = append(in.Keyframes, inputFile{Path: shownInput(k)})
	}
	if in.Image == nil && in.LastFrame == nil && len(in.Refs) == 0 && len(in.Keyframes) == 0 {
		return nil
	}
	return in
}

// fingerprint aggiunge le impronte leggendo i file locali di req (dopo i download).
func (in *manifestInputs) fingerprint(req provider.VideoRequest) {
	if in == nil {
		return
	}
	if in.Image != nil {
		in.Image.SHA256 = shortHash(req.Image)
	}
	if in.LastFrame != nil {
		in.LastFrame.SHA256 = shortHash(req.LastFrame)
	}
	for i := range in.Refs {
		if i < len(req.Refs) {
			in.Refs[i].SHA256 = shortHash(req.Refs[i])
		}
	}
	for i := range in.Keyframes {
		if i < len(req.Keyframes) {
			in.Keyframes[i].SHA256 = clipHash(req.Keyframes[i], provider.MaxInputBytes)
		}
	}
}

// shortHash è l'impronta breve di un file; "" se non si legge.
func shortHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// videoManifest prepara il manifest di giv video prima della generazione: serve
// anche alla riga di registro di un job fallito dopo l'avvio.
func videoManifest(prov, prompt string, pres presets.Result, plan *videoPlan, in *manifestInputs, req provider.VideoRequest) manifest {
	man := manifest{Provider: prov, Model: req.Model, Prompt: prompt, Presets: pres.Presets, Inputs: in}
	if req.Prompt != prompt {
		man.PromptFinal = req.Prompt
	}
	in.fingerprint(req)
	if plan != nil {
		man.Params = plan.Params
		if plan.CostEstUSD != nil {
			man.CostEstUSD, man.CostSource = *plan.CostEstUSD, plan.CostSource
		}
		man.Warnings = append(slices.Clone(pres.Warnings), plan.Warnings...)
	} else {
		man.Warnings = slices.Clone(pres.Warnings)
	}
	return man
}

// done completa il manifest dopo la generazione: file, job, durata reale e costo; il
// costo riportato dal provider prende il posto della stima.
func (man *manifest) done(files []output.FileInfo, res *provider.Result) {
	man.Files, man.JobID = files, res.JobID
	switch {
	case res.CostUSD > 0:
		man.CostUSD, man.CostEstUSD, man.CostSource = res.CostUSD, 0, ""
	case res.CostEstUSD > 0: // stima dai consumi riportati: accanto a quella da listino, non al suo posto
		man.CostUsageUSD, man.CostUsageSource = math.Round(res.CostEstUSD*1e4)/1e4, res.CostSource
		man.usageEstUSD = res.CostEstUSD
	}
	for _, m := range res.Media {
		if strings.HasPrefix(m.Mime, "video/") {
			if d, ok := output.MP4Duration(m.Data); ok {
				man.DurationS = d
				break
			}
		}
	}
}

// logFailedJob scrive nel registro una riga per un job partito e poi fallito o
// scaduto (può essere stato addebitato); gli altri errori non lasciano righe.
func logFailedJob(man manifest, err error, timedOut bool) {
	var je *httpx.JobError
	if !errors.As(err, &je) {
		return
	}
	if man.Command == "" {
		man.Command = "video"
	}
	man.JobID, man.Status = je.ID, "failed"
	if timedOut {
		man.Status = "timeout"
	}
	man.Files = []output.FileInfo{}
	logRun(man)
}

// logTotals sono i totali di giv log --sum-cost. L'ordine dei campi lascia
// identica l'uscita sui registri di prima: {"cost_usd", "generations"}.
type logTotals struct {
	Key              string       `json:"key,omitempty"`
	CostUSD          float64      `json:"cost_usd"`               // riportato dai provider
	CostEstUSD       float64      `json:"cost_est_usd,omitempty"` // stimato, generazioni senza costo riportato
	Generations      int          `json:"generations"`
	FailedJobs       int          `json:"failed_jobs,omitempty"`         // job partiti e poi falliti o scaduti
	FailedCostEstUSD float64      `json:"failed_cost_est_usd,omitempty"` // loro costo stimato (forse addebitato)
	By               string       `json:"by,omitempty"`
	Groups           []*logTotals `json:"groups,omitempty"`
}

type logRow struct {
	Provider   string  `json:"provider"`
	Model      string  `json:"model"`
	Status     string  `json:"status"`
	CostUSD    float64 `json:"cost_usd"`
	CostEstUSD float64 `json:"cost_est_usd"`
}

func (t *logTotals) add(r logRow) {
	t.CostUSD += r.CostUSD
	if r.Status != "" {
		t.FailedJobs++
		t.FailedCostEstUSD += r.CostEstUSD
		return
	}
	t.Generations++
	if r.CostUSD == 0 { // la stima accanto a un costo riportato (comandi sulle clip) non si somma
		t.CostEstUSD += r.CostEstUSD
	}
}

// sumLog somma le righe del registro; by ("provider" o "model") aggiunge i
// totali per gruppo. Le righe illeggibili contano come generazioni senza costo.
func sumLog(lines []string, by string) *logTotals {
	total := &logTotals{By: by}
	groups := map[string]*logTotals{}
	for _, l := range lines {
		var r logRow
		if json.Unmarshal([]byte(l), &r) != nil {
			r = logRow{}
		}
		total.add(r)
		if by == "" {
			continue
		}
		key := r.Provider
		if by == "model" && r.Provider != "" {
			key = r.Provider + "/" + r.Model
		}
		if key == "" {
			key = "?"
		}
		g, ok := groups[key]
		if !ok {
			g = &logTotals{Key: key}
			groups[key] = g
			total.Groups = append(total.Groups, g)
		}
		g.add(r)
	}
	slices.SortFunc(total.Groups, func(a, b *logTotals) int { return strings.Compare(a.Key, b.Key) })
	return total
}
