package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/speech"
	"gen-image-video-cli/internal/story"
)

// Controllo dei file nello storyboard (blocco controllo, V-T8): con
// controllo.tecnico giv story run, a fine giro, e giv story cut, dopo il
// montaggio, controllano come giv qc le clip e i file in uso, le voci e i
// montati, con il risultato in <nome>.controllo.json e nel JSON del comando;
// con controllo.voce le righe delle voci generate portano anche la
// trascrizione confrontata col testo (story_voicecheck.go); con
// controllo.aderenza le righe delle scene video e avatar portano il giudizio
// di aderenza della clip in uso registrato nello stato (story_judge.go). Il controllo
// avvisa e basta (K6): non rigenera, non scarta e non spende oltre la
// trascrizione; con strict nello storyboard un difetto fa uscire giv story
// run con 1.

// storyControl è il controllo completo dello storyboard: una riga per scena,
// una per voce e una per montato, come giv qc.
type storyControl struct {
	Command    string         `json:"command"`
	Storyboard string         `json:"storyboard"`
	CheckedAt  time.Time      `json:"checked_at"`
	Result     string         `json:"result"` // il peggiore delle righe controllate; nessuna: assente
	Counts     map[string]int `json:"counts"`
	Scenes     []qcRow        `json:"scenes"`
	Voices     []voiceRow     `json:"voices"`
	Outputs    []qcRow        `json:"outputs"`
	Warnings   []string       `json:"warnings,omitempty"`
	// File è il file del controllo scritto (solo nel JSON del comando)
	File string `json:"file,omitempty"`
}

// voiceRow è la riga di una voce: controllo tecnico del file e, con
// controllo.voce, il controllo voice con la trascrizione di questa esecuzione.
type voiceRow struct {
	qcRow
	Transcript *qcTranscript      `json:"transcript,omitempty"`
	Comparison *speech.Comparison `json:"comparison,omitempty"`
}

// controlOn dice se lo storyboard chiede il controllo tecnico dei file.
func controlOn(s *story.Storyboard) bool {
	return s.Controllo != nil && s.Controllo.Tecnico
}

// controlStory controlla lo storyboard già letto (path è il suo file) per il
// comando command e scrive <nome>.controllo.json; checks sono i controlli
// della voce fatti da giv story run in questa esecuzione (nil: quelli dello
// stato). Un controllo che non si può fare (ffmpeg assente, stato
// illeggibile) è un avviso su stderr: il comando non cambia esito; nil se non
// c'è niente da riportare.
func controlStory(s *story.Storyboard, path, command string, checks map[string]*storyVoiceCheck) *storyControl {
	ctl, err := controlRows(s, path, command, checks)
	if err != nil {
		fmt.Fprintf(os.Stderr, "avviso: controllo dei file non fatto: %v\n", err)
		return nil
	}
	rows := slices.Concat(ctl.Scenes, ctl.Outputs)
	for _, v := range ctl.Voices {
		rows = append(rows, v.qcRow)
	}
	for _, r := range rows {
		if r.Result == montage.QCWarning || r.Result == montage.QCDefect {
			what := r.ID
			if r.Format != "" {
				what = "montato " + r.Format
			}
			fmt.Fprintf(os.Stderr, "controllo: %s: %s (%s)\n", what, r.Result, r.File)
		}
	}
	file := story.ControlPath(path)
	if slices.ContainsFunc(storyInputs(s, path), func(f string) bool { return samePath(f, file) }) {
		msg := fmt.Sprintf("%s è un file dello storyboard, del kit o dello stato: controllo non scritto", file)
		fmt.Fprintf(os.Stderr, "avviso: %s\n", msg)
		ctl.Warnings = append(ctl.Warnings, msg)
		return ctl
	}
	data, err := json.MarshalIndent(ctl, "", "  ")
	if err == nil {
		err = story.WriteControl(file, append(data, '\n'))
	}
	if err != nil {
		msg := fmt.Sprintf("controllo non scritto in %s: %v", file, err)
		fmt.Fprintf(os.Stderr, "avviso: %s\n", msg)
		ctl.Warnings = append(ctl.Warnings, msg)
		return ctl
	}
	ctl.File = file
	return ctl
}

// judgeRows aggiunge il controllo adherence (controllo.aderenza) alle righe
// delle scene video e avatar: il giudizio della clip in uso registrato nello
// stato, senza reinvio. Senza controllo.tecnico le righe delle scene portano
// solo il giudizio.
func judgeRows(ctl *storyControl, s *story.Storyboard, st *story.State) {
	if !controlOn(s) {
		for i := range s.Scene {
			sc := &s.Scene[i]
			if sc.Tipo != story.Video && sc.Tipo != story.Avatar {
				continue
			}
			row := qcRow{ID: sc.ID, Tipo: sc.Tipo}
			clip, err := st.ClipFile(s, sc.ID)
			if err != nil {
				msg := err.Error()
				if errors.Is(err, story.ErrNotDone) {
					msg = "clip non ancora generata (giv story run)"
				}
				row.absent("", msg)
			} else {
				row.Take = st.Scenes[sc.ID].ClipTakes()
				row.QCReport = &montage.QCReport{File: clip, Result: montage.QCOK, Checks: []montage.QCCheck{}}
			}
			ctl.Scenes = append(ctl.Scenes, row)
		}
	}
	for i := range ctl.Scenes {
		row := &ctl.Scenes[i]
		if row.QCReport == nil || (row.Tipo != story.Video && row.Tipo != story.Avatar) {
			continue
		}
		var take *story.Take
		if ss := st.Scenes[row.ID]; ss != nil {
			take = ss.Clip
		}
		n := len(row.Checks)
		judgeRowCheck(row, take)
		for _, c := range row.Checks[n:] {
			if !c.Skipped {
				ctl.Counts[c.Result]++
			}
		}
	}
}

// controlRows sono le righe del controllo: con controllo.tecnico scene e
// montati come giv qc sullo storyboard (qcStory), più le voci in uso (file
// della scena o voce generata da giv story run), tutte nella cartella dello
// storyboard; con controllo.voce le voci generate portano il controllo voice.
func controlRows(s *story.Storyboard, path, command string, checks map[string]*storyVoiceCheck) (*storyControl, error) {
	ctl := &storyControl{Command: command, Storyboard: path, CheckedAt: time.Now().UTC().Truncate(time.Second),
		Counts: map[string]int{montage.QCOK: 0, montage.QCWarning: 0, montage.QCDefect: 0},
		Scenes: []qcRow{}, Voices: []voiceRow{}, Outputs: []qcRow{}}
	if controlOn(s) {
		q, err := qcStory(s, path, montage.QCThresholds{}, nil)
		if err != nil {
			return nil, err
		}
		ctl.Counts, ctl.Scenes, ctl.Outputs, ctl.Warnings = q.Counts, q.Scenes, q.Outputs, q.Warnings
	}
	st, err := story.LoadState(story.StatePath(path))
	if err != nil {
		return nil, err
	}
	if judgeControlOn(s) {
		judgeRows(ctl, s, st)
	}
	on := true
	for i := range s.Scene {
		sc := &s.Scene[i]
		row := voiceRow{qcRow: qcRow{ID: sc.ID, Tipo: sc.Tipo}}
		var file string
		switch {
		case sc.Voce != "":
			f, err := s.CheckFile(sc.Voce)
			if err != nil {
				row.absent(sc.Voce, err.Error())
				ctl.Voices = append(ctl.Voices, row)
				continue
			}
			file = f
		case needsVoice(sc):
			f, err := st.VoiceFile(s, sc.ID)
			if err != nil {
				msg := err.Error()
				if errors.Is(err, story.ErrNotDone) {
					msg = "voce non ancora generata (giv story run --voices)"
				}
				row.absent("", msg)
				ctl.Voices = append(ctl.Voices, row)
				continue
			}
			file, row.Take = f, st.Scenes[sc.ID].VoiceTakes()
		default:
			continue
		}
		if err := qcRowCheck(&row.qcRow, file, montage.QCOptions{ExpectAudio: &on}); err != nil {
			return nil, err
		}
		if voiceControlOn(s) && needsVoice(sc) {
			voiceRowChecks(&row, st.Scenes[sc.ID], checks[sc.ID])
		}
		ctl.Voices = append(ctl.Voices, row)
	}
	var results []string
	for _, rows := range [][]qcRow{ctl.Scenes, ctl.Outputs} {
		for _, r := range rows {
			if montage.QCRank(r.Result) >= 0 {
				results = append(results, r.Result)
			}
		}
	}
	for _, r := range ctl.Voices {
		if montage.QCRank(r.Result) >= 0 {
			results = append(results, r.Result)
			r.Tally(ctl.Counts)
		}
	}
	ctl.Result = qcAbsent
	if len(results) > 0 {
		ctl.Result = montage.QCWorst(results...)
	}
	return ctl, nil
}
