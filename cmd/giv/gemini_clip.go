package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"gen-image-video-cli/internal/caps"
	"gen-image-video-cli/internal/provider"
	"gen-image-video-cli/internal/provider/gemini"
)

// giv extend e giv edit con Gemini: solo su video generati da Gemini (--from
// <job_id>); le misure del video di partenza vengono dal registro giv-log.jsonl.

func init() {
	clipOpTables["gemini"] = caps.GeminiOps
	clipFromSource["gemini"] = geminiFromSource
}

// geminiFromSource controlla il formato di --from e legge dal registro durata e
// risoluzione del video di partenza; se il job non c'è restano non note (avviso).
func geminiFromSource(req *provider.ClipRequest) ([]string, error) {
	if !gemini.ValidID(req.From) {
		return nil, usageError("--from %q: non è un id di job gemini", req.From)
	}
	dur, res, ok := loggedGeminiVideo(req.From)
	if !ok {
		return []string{fmt.Sprintf("job %s non trovato fra i video gemini di %s: misure del video di partenza non note",
			req.From, logFile)}, nil
	}
	req.Source.Duration, req.Source.Height = dur, caps.GeminiHeight(res)
	return nil, nil
}

// loggedJob è quanto serve di una riga del registro.
type loggedJob struct {
	Provider  string         `json:"provider"`
	JobID     string         `json:"job_id"`
	Status    string         `json:"status"`
	DurationS float64        `json:"duration_s"`
	Params    map[string]any `json:"params"`
	Inputs    *struct {
		From string `json:"from"`
	} `json:"inputs"`
}

// loggedGeminiVideo cerca nel registro il video gemini riuscito del job id:
// durata reale e risoluzione, che extend ed edit ereditano dal video da cui
// partono (si risale la catena dei --from). ok false se il job non c'è o la
// durata non è nota.
func loggedGeminiVideo(id string) (dur float64, res string, ok bool) {
	rows := geminiLogJobs()
	r, found := rows[id]
	if !found || r.DurationS <= 0 {
		return 0, "", false
	}
	dur = r.DurationS
	for seen := map[string]bool{}; found && !seen[r.JobID]; r, found = rows[r.from()] {
		seen[r.JobID] = true
		if s, _ := r.Params["resolution"].(string); s != "" {
			return dur, s, true
		}
	}
	return dur, "", true
}

func (r loggedJob) from() string {
	if r.Inputs == nil {
		return ""
	}
	return r.Inputs.From
}

// geminiLogJobs sono le righe riuscite di gemini nel registro, per id del job
// (l'ultima vince); le righe illeggibili si saltano.
func geminiLogJobs() map[string]loggedJob {
	rows := map[string]loggedJob{}
	f, err := os.Open(logFile)
	if err != nil {
		return rows
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r loggedJob
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Provider != "gemini" || r.Status != "" || !gemini.ValidID(r.JobID) {
			continue
		}
		rows[r.JobID] = r
	}
	return rows
}
