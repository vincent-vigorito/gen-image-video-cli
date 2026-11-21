package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"gen-image-video-cli/internal/jobs"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/provider"
)

// jobsFile tiene lo stato dei job video avviati (id e indirizzo di attesa),
// nella directory corrente come il registro.
const jobsFile = "giv-jobs.jsonl"

func init() {
	// ogni job video avviato (giv video e chi usa GenerateVideo) lascia qui il
	// suo stato, così giv job fetch lo recupera senza un nuovo avvio
	provider.OnJobStart = recordJob
}

// recordJob salva lo stato di un job appena avviato; un errore è solo un avviso.
func recordJob(prov string, job provider.Job, req provider.VideoRequest) {
	st := jobs.State{Provider: prov, ID: job.ID, PollingURL: job.PollingURL,
		Model: req.Model, Prompt: req.Prompt, Started: time.Now()}
	if err := jobs.Append(jobsFile, st); err != nil {
		fmt.Fprintf(os.Stderr, "avviso: stato del job %s non salvato in %s: %v\n", job.ID, jobsFile, err)
	}
}

func cmdJob(args []string) error {
	if len(args) == 0 || args[0] != "fetch" {
		return usageError("uso: giv job fetch --provider <p> [--out <dir>] [--name <slug>] [--timeout <durata>] <id>")
	}
	return cmdJobFetch(args[1:])
}

// cmdJobFetch riprende l'attesa di un job video già partito (per esempio dopo
// un'uscita con codice 5) e ne scarica il risultato, senza un nuovo avvio:
// manifest e riga di registro come giv video.
func cmdJobFetch(args []string) error {
	fs := flag.NewFlagSet("job fetch", flag.ExitOnError)
	prov := fs.String("provider", defaultProvider(), "provider del job")
	out := fs.String("out", "out", "directory di output")
	name := fs.String("name", "", "slug per i nomi file")
	timeout := fs.Duration("timeout", 15*time.Minute, "tetto di attesa del job (es. 20m)")
	id, err := parseJobID(fs, args)
	if err != nil {
		return err
	}
	if err := checkProvider(*prov); err != nil {
		return err
	}
	// solo i provider con un'API video hanno job da recuperare
	if _, ok := defaultVideoModels[*prov]; !ok {
		return usageError("%s non ha job video da recuperare (provider con job: xai, openrouter)", *prov)
	}
	if *timeout <= 0 {
		return usageError("--timeout deve essere una durata positiva (es. 20m)")
	}
	st, err := jobs.Find(jobsFile, *prov, id)
	if errors.Is(err, jobs.ErrNotFound) {
		return usageError("nessun job %s di %s in %s: lancia giv job fetch dalla directory in cui è partito il job", id, *prov, jobsFile)
	}
	if err != nil {
		return err
	}
	c, err := newProvider(*prov)
	if err != nil {
		return err
	}
	f, ok := c.(provider.JobFetcher)
	if !ok {
		return usageError("%s non sa recuperare i job video", *prov)
	}
	// indirizzo di attesa letto da file: controllato prima di mandare la chiave
	job := provider.Job{ID: st.ID, PollingURL: st.PollingURL}
	if err := f.CheckJob(job); err != nil {
		return usageError("%v", err)
	}
	res, err := f.FetchVideo(context.Background(), job, *timeout)
	if err != nil {
		return err
	}
	slug := output.Slug(*name)
	if *name == "" {
		slug = output.Slug(st.Prompt)
		if st.Prompt == "" {
			slug = output.Slug("job " + st.ID)
		}
	}
	files, err := output.SaveAll(*out, slug, res.Media)
	if err != nil {
		return err
	}
	man := manifest{Provider: c.Name(), Model: st.Model, Prompt: st.Prompt}
	man.done(files, res)
	return emit("job fetch", man)
}

// parseJobID accetta i flag sia prima che dopo l'id del job.
func parseJobID(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", usageError("%v", err)
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return "", usageError("manca l'id del job: giv job fetch --provider <p> <id>")
	}
	id := rest[0]
	if len(rest) > 1 {
		if err := fs.Parse(rest[1:]); err != nil {
			return "", usageError("%v", err)
		}
		if len(fs.Args()) > 0 {
			return "", usageError("passa un solo id di job")
		}
	}
	if !validJobID(id) {
		return "", usageError("id del job non valido: %q", id)
	}
	return id, nil
}

// validJobID: da 1 a 256 caratteri ASCII stampabili, senza spazi.
func validJobID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r >= 0x7f {
			return false
		}
	}
	return true
}
