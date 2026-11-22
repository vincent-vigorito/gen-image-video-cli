package main

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gen-image-video-cli/internal/provider"
)

// CLI-931 (O-T11): correzioni dalle prove a pagamento. Nessuna rete.

// fakeClip è un provider che riporta il costo reale, come xAI.
type fakeClip struct{ cost float64 }

func (fakeClip) Name() string { return "xai" }
func (fakeClip) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (fakeClip) GenerateImage(context.Context, provider.ImageRequest) (*provider.Result, error) {
	return nil, nil
}
func (fakeClip) GenerateVideo(context.Context, provider.VideoRequest) (*provider.Result, error) {
	return nil, nil
}
func (f fakeClip) GenerateClip(context.Context, provider.ClipRequest) (*provider.Result, error) {
	return &provider.Result{Media: []provider.Media{{Mime: "video/mp4", Data: []byte("MP4")}}, CostUSD: f.cost, JobID: "job-x"}, nil
}

func otClip(t *testing.T) string {
	cutNeedFF(t)
	p := filepath.Join(t.TempDir(), "in.mp4")
	cutClip(t, p, "1280x720", 24, "5.5")
	return p
}

// caso 2: con il costo reale del provider il manifest tiene anche stima e fonte.
func TestManifestClipStimaEReale(t *testing.T) {
	t.Chdir(t.TempDir())
	clip := otClip(t)
	for _, c := range []struct {
		spec clipSpec
		o    clipOptions
	}{
		{extendSpec, clipOptions{Provider: "xai", Video: clip, Duration: 3, Prompt: "continua", Set: []string{"prompt", "video", "duration"}}},
		{editSpec, clipOptions{Provider: "xai", Video: clip, Prompt: "nebbia", Set: []string{"prompt", "video"}}},
	} {
		o := c.o
		o.Timeout, o.Out, o.client = time.Minute, filepath.Join(t.TempDir(), "o"), fakeClip{cost: 0.27}
		job, err := prepareClip(c.spec, o)
		if err != nil {
			t.Fatal(err)
		}
		man, err := job.run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(man)
		var m map[string]any
		json.Unmarshal(b, &m)
		if m["cost_usd"] != 0.27 || m["cost_est_usd"] == nil || m["cost_est_usd"] == 0.0 || m["cost_source"] == nil || m["cost_source"] == "" {
			t.Errorf("%s: manifest senza stima e costo reale: %s", c.spec.Op, b)
		}
	}
}

// il registro non somma due volte: con cost_usd la stima non entra nei totali.
func TestTotaliSenzaDoppioConteggio(t *testing.T) {
	var tot logTotals
	tot.add(logRow{CostUSD: 0.27, CostEstUSD: 0.5})
	if math.Abs(tot.CostUSD-0.27) > 1e-9 || tot.CostEstUSD != 0 {
		t.Errorf("totali %+v", tot)
	}
	tot = logTotals{}
	tot.add(logRow{CostEstUSD: 0.5})
	if tot.CostEstUSD != 0.5 {
		t.Errorf("senza costo reale: %+v", tot)
	}
}

func otPlan(t *testing.T, spec clipSpec, args ...string) (est, dur float64) {
	t.Helper()
	o, err := parseClipFlags(spec, append([]string{"--provider", "xai", "--dry-run"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	job, err := prepareClip(spec, o)
	if err != nil {
		t.Fatal(err)
	}
	if job.plan.CostEstUSD == nil || job.plan.Source == nil {
		t.Fatalf("piano senza stima: %+v", job.plan)
	}
	return *job.plan.CostEstUSD, job.plan.Source.DurationS
}

// caso 3: extend = secondi aggiunti x 0,07 + ceil(sorgente) x 0,01; edit invariata.
func TestStimaExtendXAI(t *testing.T) {
	clip := otClip(t)
	est, dur := otPlan(t, extendSpec, "--video", clip, "--duration", "3", "continua")
	want := 3*0.07 + math.Ceil(dur)*0.01 // sorgente 5,5 s -> 6 s: 0,27 $
	if math.Abs(est-want) > 0.0051 {
		t.Errorf("extend stima %v, attesa %v", est, want)
	}
	if est < 0.27-1e-9 || est >= 0.69 {
		t.Errorf("extend stima %v: deve stare fra l'addebito misurato 0,27 e la stima vecchia 0,69", est)
	}
	e, d := otPlan(t, editSpec, "--video", clip, "nebbia")
	if math.Abs(e-d*0.08) > 0.0051 {
		t.Errorf("edit stima %v, attesa %v (sorgente x 0,08)", e, d*0.08)
	}
	// il tetto si applica alla stima nuova: 0,30 passa, 0,20 no
	if _, err := os.Stat(clip); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		max  string
		code int
	}{{"0.30", 0}, {"0.20", 2}} {
		o, _ := parseClipFlags(extendSpec, []string{"--provider", "xai", "--dry-run", "--video", clip, "--duration", "3", "--max-cost", c.max, "continua"})
		_, err := prepareClip(extendSpec, o)
		if got, _ := exitCode(err); err != nil && got != c.code || err == nil && c.code != 0 {
			t.Errorf("--max-cost %s: err=%v", c.max, err)
		}
	}
}
