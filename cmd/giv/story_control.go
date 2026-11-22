package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/story"
)

// Controllo dei file nello storyboard (blocco controllo, V-T8): con
// controllo.tecnico giv story run, a fine giro, e giv story cut, dopo il
// montaggio, controllano come giv qc le clip e i file in uso, le voci e i
// montati, con il risultato in <nome>.controllo.json e nel JSON del comando.
// Il controllo avvisa e basta (K6): non rigenera, non scarta e non spende;
// con strict nello storyboard un difetto fa uscire giv story run con 1.

// storyControl è il controllo completo dello storyboard: una riga per scena,
// una per voce e una per montato, come giv qc.
type storyControl struct {
	Command    string         `json:"command"`
	Storyboard string         `json:"storyboard"`
	CheckedAt  time.Time      `json:"checked_at"`
	Result     string         `json:"result"` // il peggiore delle righe controllate; nessuna: assente
	Counts     map[string]int `json:"counts"`
	Scenes     []qcRow        `json:"scenes"`
	Voices     []qcRow        `json:"voices"`
	Outputs    []qcRow        `json:"outputs"`
	Warnings   []string       `json:"warnings,omitempty"`
	// File è il file del controllo scritto (solo nel JSON del comando)
	File string `json:"file,omitempty"`
}

// controlOn dice se lo storyboard chiede il controllo tecnico dei file.
func controlOn(s *story.Storyboard) bool {
	return s.Controllo != nil && s.Controllo.Tecnico
}

// controlStory controlla lo storyboard già letto (path è il suo file) per il
// comando command e scrive <nome>.controllo.json. Un controllo che non si può
// fare (ffmpeg assente, stato illeggibile) è un avviso su stderr: il comando
// non cambia esito; nil se non c'è niente da riportare.
func controlStory(s *story.Storyboard, path, command string) *storyControl {
	ctl, err := controlRows(s, path, command)
	if err != nil {
		fmt.Fprintf(os.Stderr, "avviso: controllo dei file non fatto: %v\n", err)
		return nil
	}
	for _, rows := range [][]qcRow{ctl.Scenes, ctl.Voices, ctl.Outputs} {
		for _, r := range rows {
			if r.Result == montage.QCWarning || r.Result == montage.QCDefect {
				what := r.ID
				if r.Format != "" {
					what = "montato " + r.Format
				}
				fmt.Fprintf(os.Stderr, "controllo: %s: %s (%s)\n", what, r.Result, r.File)
			}
		}
	}
	file := story.ControlPath(path)
	if slices.ContainsFunc(storyFiles(s), func(f string) bool { return samePath(f, file) }) {
		msg := fmt.Sprintf("%s è un file dello storyboard: controllo non scritto", file)
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

// controlRows sono le righe del controllo: scene e montati come giv qc sullo
// storyboard (qcStory), più le voci in uso (file della scena o voce generata
// da giv story run), tutte nella cartella dello storyboard.
func controlRows(s *story.Storyboard, path, command string) (*storyControl, error) {
	q, err := qcStory(s, path, montage.QCThresholds{}, nil)
	if err != nil {
		return nil, err
	}
	ctl := &storyControl{Command: command, Storyboard: path, CheckedAt: time.Now().UTC().Truncate(time.Second),
		Counts: q.Counts, Scenes: q.Scenes, Voices: []qcRow{}, Outputs: q.Outputs, Warnings: q.Warnings}
	st, err := story.LoadState(story.StatePath(path))
	if err != nil {
		return nil, err
	}
	on := true
	for i := range s.Scene {
		sc := &s.Scene[i]
		row := qcRow{ID: sc.ID, Tipo: sc.Tipo}
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
		if err := qcRowCheck(&row, file, montage.QCOptions{ExpectAudio: &on}); err != nil {
			return nil, err
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
