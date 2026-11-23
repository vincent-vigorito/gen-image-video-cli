package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/story"
)

// Approvazione delle scene (0.13): con approvazione nello storyboard il
// definitivo di una scena video parte solo dopo giv story approve, che
// registra nello stato la ripresa vista (la bozza se default.bozza c'è,
// altrimenti il fotogramma chiave) e la ricetta del definitivo
// (story.RecipeKey). Una nuova ripresa, un altro fotogramma chiave, un cambio
// della ricetta o del materiale da vedere (default.bozza aggiunto o tolto) la
// rendono vecchia (checkApproval); un cambio in
// un'altra scena indipendente no. Le scene avatar non hanno né bozza né
// fotogramma chiave: l'approvazione non le riguarda e partono come prima.
// Il comando è locale: nessuna rete, nessuna spesa.

// Stati di approvazione di una scena in giv story check (approval).
const (
	apprPrepare = "da_preparare" // niente da vedere: bozza o fotogramma chiave da generare
	apprPending = "da_approvare" // c'è il materiale, manca l'approvazione o non vale più
	apprReady   = "pronta"       // approvata: giv story run può avviarne il definitivo
	apprRunning = "in_corso"     // bozza o definitivo partiti, da recuperare
	apprDone    = "fatta"        // definitivo già fatto
)

// storyApproveResult è l'uscita di giv story approve.
type storyApproveResult struct {
	Command    string          `json:"command"`
	Storyboard string          `json:"storyboard"`
	State      string          `json:"state"`
	Material   string          `json:"material"` // cosa si approva in questo storyboard: bozza o fotogramma
	Approved   []approvedScene `json:"approved"`
	Skipped    []skippedScene  `json:"skipped,omitempty"`
}

// approvedScene è una scena approvata: il materiale visto, col suo contenuto.
type approvedScene struct {
	ID       string `json:"id"`
	Take     string `json:"take"` // bozza o fotogramma
	File     string `json:"file"`
	SHA256   string `json:"sha256"`
	Keyframe string `json:"keyframe,omitempty"` // fotogramma chiave in uso
	Already  bool   `json:"already,omitempty"`  // era già approvata così: stato non cambiato
}

// skippedScene è una scena che --all salta, col motivo.
type skippedScene struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// approvalTake è il materiale che si approva nello storyboard: la bozza con
// default.bozza, altrimenti il fotogramma chiave.
func approvalTake(s *story.Storyboard) string {
	if s.Default.Bozza != nil {
		return story.TakeDraft
	}
	return story.TakeKeyframe
}

// approvalGated: con approvazione nello storyboard il definitivo della scena
// aspetta l'approvazione (scene video; le avatar no).
func approvalGated(s *story.Storyboard, scene *story.Scene) bool {
	return s.Approvazione && scene.Tipo == story.Video
}

// definitiveGate dice se giv story run può avviare il definitivo della scena:
// nil se lo storyboard non chiede l'approvazione, se la scena non la prevede
// o se la sua approvazione vale ancora; altrimenti il motivo
// (story.ErrNotApproved, story.ErrStaleApproval o la ricetta che non si
// calcola). Locale: legge solo file dello storyboard e del kit.
func definitiveGate(s *story.Storyboard, st *story.State, scene *story.Scene) error {
	if !approvalGated(s, scene) {
		return nil
	}
	recipe, err := s.RecipeKey(scene.ID, envProvider())
	if err != nil {
		return fmt.Errorf("%w: %v", story.ErrStaleApproval, err)
	}
	return checkApproval(s, st, scene.ID, recipe)
}

// checkApproval è la verifica condivisa da gate, approve e foglio: vale
// story.CheckApproval e in più il materiale approvato deve essere quello che
// lo storyboard chiede oggi (approvalTake): un fotogramma approvato prima di
// aggiungere default.bozza, o una bozza approvata prima di toglierla, non vale
// più (la ricetta del definitivo non cambia, il materiale da vedere sì).
func checkApproval(s *story.Storyboard, st *story.State, id, recipe string) error {
	if err := st.CheckApproval(s, id, recipe); err != nil {
		return err
	}
	if a, take := st.Scenes[id].Approval, approvalTake(s); a.Take != take {
		return fmt.Errorf("scena %s: %w: approvato il materiale %s, lo storyboard ora chiede %s", id, story.ErrStaleApproval, a.Take, take)
	}
	return nil
}

// approvalMissing è il motivo per cui la scena non ha materiale da approvare.
func approvalMissing(s *story.Storyboard, scene *story.Scene) string {
	switch {
	case approvalTake(s) == story.TakeDraft:
		return "da preparare: nessuna bozza (giv story run --bozza)"
	case scene.FotogrammaChiave == nil:
		return "da preparare: niente da vedere (serve fotogramma_chiave nella scena, o default.bozza)"
	}
	return "da preparare: fotogramma chiave non ancora generato (giv story run --keyframes)"
}

// approvalStatus è lo stato di approvazione della scena per giv story check
// (apprPrepare…apprDone) e il motivo, se serve; have dice se un file dello
// stato c'è.
func approvalStatus(s *story.Storyboard, st *story.State, scene *story.Scene, have func(string) bool) (status, note string) {
	ss := st.Scenes[scene.ID]
	switch {
	case ss != nil && ss.Status == story.Fatto && ss.Clip != nil && have(ss.Clip.File):
		return apprDone, ""
	case ss != nil && ss.Job != nil:
		return apprRunning, "definitivo partito, da recuperare"
	}
	err := definitiveGate(s, st, scene)
	if err == nil {
		return apprReady, ""
	}
	take := approvalTake(s)
	if ss == nil || ss.Current(take) == nil {
		if take == story.TakeDraft && ss != nil && ss.Draft != nil && ss.Draft.Job != nil {
			return apprRunning, "bozza partita, da recuperare"
		}
		return apprPrepare, approvalMissing(s, scene)
	}
	if errors.Is(err, story.ErrNotApproved) {
		return apprPending, "da approvare: " + take + " " + ss.Current(take).File
	}
	return apprPending, strings.TrimPrefix(err.Error(), "scena "+scene.ID+": ")
}

// cmdStoryApprove è giv story approve [--all] <storyboard.json> [<scena>…].
func cmdStoryApprove(args []string) error {
	fl := flag.NewFlagSet("story approve", flag.ExitOnError)
	fl.Usage = func() { fmt.Fprint(os.Stderr, storySubHelp["approve"]) }
	all := fl.Bool("all", false, "approva ogni scena con materiale da vedere, elenca le altre")
	kitFlag := fl.String("kit", "", "kit di marca e personaggi al posto del campo kit")
	const use = "uso: giv story approve [--all] [--kit <kit.json>] <storyboard.json> [<scena>…]"
	// flag anche dopo il file e fra le scene
	var pos []string
	for rest := args; ; {
		if err := fl.Parse(rest); err != nil {
			return err
		}
		if fl.NArg() == 0 {
			break
		}
		pos = append(pos, fl.Arg(0))
		rest = fl.Args()[1:]
	}
	if err := checkKitFlag(fl, *kitFlag); err != nil {
		return err
	}
	if len(pos) == 0 {
		return usageError("%s", use)
	}
	path := pos[0]
	var ids []string
	for _, id := range pos[1:] {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	switch {
	case *all && len(ids) > 0:
		return usageError("giv story approve: --all oppure le scene, non tutti e due (%s)", use)
	case !*all && len(ids) == 0:
		return usageError("giv story approve: indica le scene da approvare o --all (%s)", use)
	}
	res, err := approveStory(path, *kitFlag, ids, *all)
	if err != nil {
		return err
	}
	return output.PrintJSON(res)
}

// approveStory approva le scene ids (o con all ogni scena che si può
// approvare) col blocco dello stato condiviso con giv story run. Con le scene
// indicate è tutto o niente: una scena che non si può approvare è un errore
// d'uso e lo stato non cambia; con all le scene senza materiale si saltano e
// si elencano.
func approveStory(path, kitFile string, ids []string, all bool) (*storyApproveResult, error) {
	s, err := loadStoryKit(path, kitFile)
	if err != nil {
		return nil, err
	}
	var targets []*story.Scene
	if all {
		for i := range s.Scene {
			if s.Scene[i].Generata() {
				targets = append(targets, &s.Scene[i])
			}
		}
	} else {
		var bad []string
		for _, id := range ids {
			sc := s.Scena(id)
			switch {
			case sc == nil:
				bad = append(bad, fmt.Sprintf("scena %s: inesistente", id))
			case sc.Tipo != story.Video:
				bad = append(bad, fmt.Sprintf("scena %s: scena %s, l'approvazione vale per le scene video", id, sc.Tipo))
			default:
				targets = append(targets, sc)
			}
		}
		if len(bad) > 0 {
			return nil, storyError(path, errors.New(strings.Join(bad, "\n")))
		}
	}
	st, unlock, err := story.OpenLockedState(path, "approve")
	if err != nil {
		return nil, usageError("%v", err)
	}
	defer unlock()
	if !s.Approvazione {
		fmt.Fprintf(os.Stderr, "avviso: %s non ha approvazione: true, giv story run non aspetta l'approvazione\n", path)
	}
	take := approvalTake(s)
	res := &storyApproveResult{Command: "story approve", Storyboard: path, State: st.Path(), Material: take, Approved: []approvedScene{}}
	prov := envProvider()
	var bad []string
	changed := false
	skip := func(sc *story.Scene, reason string) {
		if all {
			res.Skipped = append(res.Skipped, skippedScene{ID: sc.ID, Reason: reason})
		} else {
			bad = append(bad, fmt.Sprintf("scena %s: %s", sc.ID, reason))
		}
	}
	for _, sc := range targets {
		ss := st.Scenes[sc.ID]
		if sc.Tipo != story.Video {
			skip(sc, "scena "+sc.Tipo+": l'approvazione vale per le scene video")
			continue
		}
		if all && ss != nil && ss.Status == story.Fatto && ss.Clip != nil {
			skip(sc, "definitivo già fatto ("+ss.Clip.File+")")
			continue
		}
		recipe, err := s.RecipeKey(sc.ID, prov)
		if err != nil {
			skip(sc, strings.TrimPrefix(err.Error(), "scena "+sc.ID+": "))
			continue
		}
		if checkApproval(s, st, sc.ID, recipe) == nil {
			a := ss.Approval
			res.Approved = append(res.Approved, approvedScene{ID: sc.ID, Take: a.Take, File: a.File, SHA256: a.SHA256, Keyframe: a.Keyframe, Already: true})
			continue
		}
		if ss == nil || ss.Current(take) == nil {
			skip(sc, approvalMissing(s, sc))
			continue
		}
		a, err := st.Approve(s, sc.ID, take, recipe)
		if err != nil {
			skip(sc, strings.TrimPrefix(err.Error(), "scena "+sc.ID+": "))
			continue
		}
		changed = true
		res.Approved = append(res.Approved, approvedScene{ID: sc.ID, Take: a.Take, File: a.File, SHA256: a.SHA256, Keyframe: a.Keyframe})
	}
	if len(bad) > 0 {
		return nil, usageError("%s: niente approvato:\n  %s", path, strings.Join(bad, "\n  "))
	}
	if changed {
		if err := st.Save(); err != nil {
			return nil, fmt.Errorf("approvazioni non salvate: %w", err)
		}
	}
	for _, a := range res.Approved {
		what := "approvata"
		if a.Already {
			what = "già approvata"
		}
		fmt.Fprintf(os.Stderr, "scena %s: %s (%s %s, sha256 %s)\n", a.ID, what, a.Take, a.File, a.SHA256[:12])
	}
	for _, sk := range res.Skipped {
		fmt.Fprintf(os.Stderr, "scena %s: saltata: %s\n", sk.ID, sk.Reason)
	}
	return res, nil
}
