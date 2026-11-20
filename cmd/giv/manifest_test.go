package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/presets"
	"gen-image-video-cli/internal/provider"
)

func keysOf(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

// Caso 1: un registro nel formato di oggi si legge e dà la stessa somma.
func TestLogVecchioFormatoStessaSomma(t *testing.T) {
	dir := t.TempDir()
	old := `{"ts":"2026-01-01T10:00:00Z","command":"image","provider":"openai","model":"gpt-image-1","prompt":"a","files":[{"path":"a.png"}],"cost_usd":0.25}
{"ts":"2026-01-01T11:00:00Z","command":"video","provider":"openrouter","model":"kwaivgi/kling-v3.0-pro","prompt":"b","files":[{"path":"b.mp4"}],"cost_usd":1.5}
{"ts":"2026-01-01T12:00:00Z","command":"image","provider":"gemini","model":"x","prompt":"c","files":[{"path":"c.png"}]}
`
	if err := os.WriteFile(dir+"/giv-log.jsonl", []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		cmd := exec.Command(binPath, args...)
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
		return string(out)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(run("log", "--sum-cost")), &got); err != nil {
		t.Fatal(err)
	}
	if got["cost_usd"] != 1.75 || got["generations"] != float64(3) {
		t.Errorf("somma=%v", got)
	}
	for _, k := range []string{"cost_est_usd", "failed_jobs", "failed_cost_est_usd"} {
		if _, ok := got[k]; ok {
			t.Errorf("campo nuovo %s presente su registro vecchio: %v", k, got)
		}
	}
	if out := run("log", "-n", "5"); !strings.Contains(out, "b.mp4") || !strings.Contains(out, "$1.5000") {
		t.Errorf("lista=%s", out)
	}
}

// Caso 2: xAI non riporta il costo: cost_est_usd e non cost_usd; col costo del provider il contrario.
func TestManifestCostoStimatoXAIeRiportato(t *testing.T) {
	est := 0.5
	plan := &videoPlan{CostEstUSD: &est, CostSource: "listino"}
	req := provider.VideoRequest{Model: "grok-imagine-video", Prompt: "p"}
	man := videoManifest("xai", "p", presets.Result{}, plan, nil, req)
	man.done([]output.FileInfo{{Path: "a.mp4"}}, &provider.Result{})
	m := keysOf(t, man)
	if m["cost_est_usd"] != 0.5 || m["cost_source"] != "listino" {
		t.Errorf("manca la stima: %v", m)
	}
	if _, ok := m["cost_usd"]; ok {
		t.Errorf("cost_usd presente senza costo riportato: %v", m)
	}
	man = videoManifest("openrouter", "p", presets.Result{}, plan, nil, req)
	man.done([]output.FileInfo{{Path: "a.mp4"}}, &provider.Result{CostUSD: 0.4})
	m = keysOf(t, man)
	if m["cost_usd"] != 0.4 {
		t.Errorf("cost_usd atteso: %v", m)
	}
	if _, ok := m["cost_est_usd"]; ok {
		t.Errorf("cost_est_usd resta accanto al costo riportato: %v", m)
	}
}

// Caso 3: job fallito dopo l'avvio = riga di registro con job_id; errore senza job = nessuna riga.
func TestJobFallitoLasciaRigaConJobID(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("HOME", t.TempDir())
	req := provider.VideoRequest{Model: "kwaivgi/kling-v3.0-pro", Prompt: "p"}
	man := videoManifest("openrouter", "p", presets.Result{}, nil, nil, req)
	logFailedJob(man, errors.New("rete"), false)
	if _, err := os.Stat(logFile); err == nil {
		t.Fatal("errore senza job: nessuna riga attesa")
	}
	logFailedJob(man, &httpx.JobError{ID: "job-42", Err: errors.New("generazione fallita")}, false)
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row); err != nil {
		t.Fatal(err, string(data))
	}
	if row["job_id"] != "job-42" || row["status"] != "failed" || row["command"] != "video" {
		t.Errorf("riga=%v", row)
	}
	var tot map[string]any
	lines := []string{strings.TrimSpace(string(data))}
	b, _ := json.Marshal(sumLog(lines, ""))
	json.Unmarshal(b, &tot)
	if tot["failed_jobs"] != float64(1) || tot["generations"] != float64(0) {
		t.Errorf("somma=%v", tot)
	}
}

// Caso 4: un comando di oggi non produce campi nuovi vuoti.
func TestManifestDiOggiSenzaCampiNuovi(t *testing.T) {
	req := provider.VideoRequest{Model: "m", Prompt: "p"}
	man := videoManifest("openrouter", "p", presets.Result{}, nil, newManifestInputs(req), req)
	man.done([]output.FileInfo{{Path: "a.mp4"}}, &provider.Result{})
	man.Command = "video"
	m := keysOf(t, logEntry{Ts: "t", manifest: man})
	allowed := map[string]bool{"ts": true, "command": true, "provider": true, "model": true, "prompt": true, "files": true}
	for k := range m {
		if !allowed[k] {
			t.Errorf("campo nuovo %q in un manifest di oggi: %v", k, m)
		}
	}
	m = keysOf(t, manifest{Provider: "gemini", Model: "x", Prompt: "p", Files: []output.FileInfo{{Path: "a.png"}}})
	if len(m) != 4 {
		t.Errorf("manifest immagine: %v", m)
	}
}

// Un job riuscito mette il job_id nel manifest; senza job_id il campo manca.
func TestManifestJobIDSuJobRiuscito(t *testing.T) {
	req := provider.VideoRequest{Model: "m", Prompt: "p"}
	man := videoManifest("xai", "p", presets.Result{}, nil, nil, req)
	man.done(nil, &provider.Result{JobID: "req-7"})
	if m := keysOf(t, man); m["job_id"] != "req-7" {
		t.Errorf("job_id atteso: %v", m)
	}
	man = videoManifest("xai", "p", presets.Result{}, nil, nil, req)
	man.done(nil, &provider.Result{})
	if _, ok := keysOf(t, man)["job_id"]; ok {
		t.Error("job_id vuoto non va emesso")
	}
}
