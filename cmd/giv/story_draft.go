package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gen-image-video-cli/internal/montage"
	"gen-image-video-cli/internal/story"
)

// Bozze video (default.bozza, 0.13): stessa richiesta, durata e riferimenti
// del definitivo con provider, modello e risoluzione economici della bozza;
// file in clip/bozza/ (story.DraftDir), stato in SceneState.Draft, spesa in
// Charges col tipo story.ChargeDraft. giv story cut --bozza le monta con la
// scritta BOZZA; il foglio delle approvazioni mostra fotogrammi chiave e
// bozze da vedere.

// draftCheck è la bozza di una scena video in giv story check.
type draftCheck struct {
	Provider   string   `json:"provider"`
	Model      string   `json:"model"`
	Resolution string   `json:"resolution,omitempty"`
	Status     string   `json:"status"` // da_generare, fatto, partito, incerto, fallito, mancante
	File       string   `json:"file,omitempty"`
	CostEstUSD *float64 `json:"cost_est_usd,omitempty"`
	CostSource string   `json:"cost_source,omitempty"`
}

// draftChecker è un controllo delle scene col profilo della bozza
// (default.bozza), sugli stessi client e cataloghi di chk: videoOptions e
// prepareVideo danno la richiesta della bozza. Per giv story run --bozza va
// impostata anche la cartella: out = <cartella dello storyboard>/clip/bozza.
func (chk *storyChecker) draftChecker() *storyChecker {
	d := newStoryChecker(chk.s)
	d.profile, d.clients, d.state = story.TakeDraft, chk.clients, chk.state
	return d
}

// draftName è il nome (senza estensione) della prossima bozza della scena:
// <id>-b<N>, in story.DraftDir.
func draftName(id string, ss *story.SceneState) string {
	return fmt.Sprintf("%s-b%d", id, draftTakes(ss)+1)
}

// draftStatus è lo stato della bozza della scena nello stato (da_generare,
// fatto, partito, incerto, fallito, mancante) e il suo file in uso.
func draftStatus(ss *story.SceneState, have func(string) bool) (status, file string) {
	if ss == nil || ss.Draft == nil {
		return runToMake, ""
	}
	t := ss.Draft
	switch {
	case t.Job != nil:
		return story.Partito, ""
	case t.Status == story.Incerto || t.Status == story.Fallito:
		return t.Status, ""
	case t.Clip != nil && have(t.Clip.File):
		return story.Fatto, t.Clip.File
	case t.Clip != nil:
		return runMissing, t.Clip.File
	}
	return runToMake, ""
}

// drafts porta in giv story check la bozza di ogni scena video (profilo,
// stima, stato) e quanto resta da spendere per le bozze ancora da fare
// (drafts_cost_est_usd): una bozza incerta o fallita non si rifà da sola e
// non conta. Modello e capacità della bozza si controllano sul catalogo come
// quelli del definitivo.
func (chk *storyChecker) drafts(st *story.State, out *storyCheck) error {
	s := chk.s
	d := chk.draftChecker()
	have := func(f string) bool {
		_, err := s.CheckFile(f)
		return err == nil
	}
	var total float64
	var rejects []error
	for i := range out.Scenes {
		c := &out.Scenes[i]
		scene := s.Scena(c.ID)
		if scene == nil || scene.Tipo != story.Video {
			continue
		}
		o, err := d.videoOptions(scene)
		var job *videoJob
		if err == nil {
			fmt.Fprintf(os.Stderr, "scena %s: bozza %s/%s\n", scene.ID, o.Provider, o.Model)
			job, err = prepareVideo(o)
		}
		if err != nil {
			if code, _ := exitCode(err); code != exitUsage {
				return fmt.Errorf("scena %s: bozza: %w", scene.ID, err)
			}
			rejects = append(rejects, fmt.Errorf("scena %s: default.bozza: %w", scene.ID, err))
			continue
		}
		dc := &draftCheck{Provider: o.Provider, Model: job.req.Model, Resolution: o.Resolution}
		if p := job.plan; p.CostEstUSD != nil {
			dc.CostEstUSD, dc.CostSource = p.CostEstUSD, p.CostSource
		}
		dc.Status, dc.File = draftStatus(st.Scenes[scene.ID], have)
		c.Draft = dc
		if dc.Status != runToMake {
			continue
		}
		switch p := job.plan; {
		case p.costExact != nil:
			total += *p.costExact
		case p.CostEstUSD != nil:
			total += *p.CostEstUSD
		default:
			out.DraftsNotEstimated = append(out.DraftsNotEstimated, scene.ID)
		}
	}
	if len(rejects) > 0 {
		return storyError(out.Storyboard, errors.Join(rejects...))
	}
	v := round4(total)
	out.DraftsCostEstUSD = &v
	fmt.Fprintf(os.Stderr, "bozze ancora da fare: stima %.4f $\n", v)
	if len(out.DraftsNotEstimated) > 0 {
		fmt.Fprintf(os.Stderr, "avviso: bozze senza stima del costo (non contate): %s\n", strings.Join(out.DraftsNotEstimated, ", "))
	}
	return nil
}

// approvalSheetName è il foglio delle approvazioni accanto allo storyboard.
const approvalSheetName = "foglio-approvazione.jpg"

// approvalSheet scrive (o riscrive) foglio-approvazione.jpg accanto allo
// storyboard: per ogni scena video il fotogramma chiave e la bozza in uso,
// i file che giv story approve approva, con id, materiale e approvazione
// nelle etichette. Riceve solo i file già noti allo stato (controllati come
// quelli dello storyboard), non cerca nelle cartelle; salvo foglio_contatto
// false. È un di più, come il foglio di contatto: un problema è un avviso.
func approvalSheet(s *story.Storyboard, path string) {
	if s.FoglioContatto != nil && !*s.FoglioContatto {
		return
	}
	warn := func(err error) {
		fmt.Fprintf(os.Stderr, "avviso: %s non scritto: %s\n", approvalSheetName, strings.ReplaceAll(localErrMsg(err), "\n", " "))
	}
	st, err := story.LoadState(story.StatePath(path))
	if err != nil {
		warn(err)
		return
	}
	o := sheetOptions{Cols: 4, Labels: true}
	p := newSheetPlan(o)
	prov := envProvider()
	for i := range s.Scene {
		sc := &s.Scene[i]
		ss := st.Scenes[sc.ID]
		if sc.Tipo != story.Video || ss == nil {
			continue
		}
		mark := ""
		if recipe, err := s.RecipeKey(sc.ID, prov); err == nil && checkApproval(s, st, sc.ID, recipe) == nil {
			mark = " · approvata"
		}
		newRow, used := true, 0
		for _, m := range []struct {
			take, label, role string
		}{
			{story.TakeKeyframe, "fotogramma chiave", "fotogramma-"},
			{story.TakeDraft, "bozza " + strconv.Itoa(draftTakes(ss)), "bozza-"},
		} {
			t := ss.Current(m.take)
			if t == nil {
				continue
			}
			where := "scena " + sc.ID + ": " + m.label
			file, err := s.CheckFile(t.File)
			var info *montage.Info
			if err == nil {
				info, err = localMedia(where, file)
			}
			if err == nil {
				label := sc.ID + " · " + m.label
				if ss.Approval != nil && ss.Approval.File == t.File {
					label += mark
				}
				if err = p.add(file, info, o, label, newRow, used); err != nil {
					err = usageError("%v", err)
				}
			}
			if err != nil {
				if err := p.skip(where, err); err != nil {
					warn(err)
					return
				}
				continue
			}
			p.Inputs[m.role+sc.ID] = file
			newRow, used = false, used+1
		}
		if !newRow {
			p.Scenes = append(p.Scenes, sc.ID)
		}
	}
	if len(p.Sheet.Cells) == 0 {
		return // niente da vedere: nessun foglio
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(os.Stderr, "avviso: %s: %s\n", approvalSheetName, w)
	}
	if err := p.Sheet.Check(); err != nil {
		warn(err)
		return
	}
	inputs := cutProtected(s, path)
	for _, k := range sortedKeys(p.Inputs) {
		inputs = append(inputs, p.Inputs[k])
	}
	res, err := localTool.RunOp(context.Background(), montage.Op{Inputs: inputs, Output: filepath.Join(s.Dir, approvalSheetName),
		Root: s.Dir, Replace: true, Build: p.Sheet.Build})
	if err != nil {
		warn(err)
		return
	}
	fmt.Fprintf(os.Stderr, "foglio delle approvazioni: %s\n", res.Output)
}

// draftTakes sono le bozze generate della scena: sostituite più quella in uso.
func draftTakes(ss *story.SceneState) int {
	if ss == nil || ss.Draft == nil {
		return 0
	}
	n := 0
	for _, t := range ss.Draft.Previous {
		if t.Kind == "" {
			n++
		}
	}
	if ss.Draft.Clip != nil {
		n++
	}
	return n
}
