package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gen-image-video-cli/internal/story"
)

// P-C (CLI-1010): il giudizio di una ripresa si recupera dallo stato senza
// reinvio e vale solo per quella ripresa.

const judgeBoard = `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},
"controllo":{"aderenza":true},
"scene":[{"id":"s1","tipo":"video","prompt":"Anja mostra ARGO"}]}`

func TestStoryJudgeRecuperatoSenzaReinvio(t *testing.T) {
	e := newJudgeEnv(t, geminiText(judgeVerdict("aderente", `"ok"`)))
	board := filepath.Join(e.dir, "spot.json")
	if err := os.WriteFile(board, []byte(judgeBoard), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := story.Load(board)
	if err != nil {
		t.Fatal(err)
	}
	st := &story.State{Scenes: map[string]*story.SceneState{}}
	ss := &story.SceneState{}
	ss.Clip = &story.Take{File: "clip.mp4", Done: time.Now()}
	st.Scenes["s1"] = ss

	due, _, err := storyJudgeDue(s, st, "s1", story.TakeFinal)
	if err != nil || !due {
		t.Fatalf("da giudicare: due=%v err=%v", due, err)
	}
	if e.hits() != 0 {
		t.Fatalf("storyJudgeDue ha inviato: %d", e.hits())
	}

	j, err := prepareStoryJudge(s, st, "s1", story.TakeFinal)
	if err != nil {
		t.Fatal(err)
	}
	if e.hits() != 0 {
		t.Fatalf("prepare ha inviato: %d", e.hits())
	}
	res, err := j.Send(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if e.hits() != 1 || res.Judgement.Status != story.Fatto {
		t.Fatalf("hits=%d judgement=%+v", e.hits(), res.Judgement)
	}
	ss.Clip.Judgements = append(ss.Clip.Judgements, res.Judgement)

	// stessa ripresa: recuperato dallo stato, zero invii
	due, _, err = storyJudgeDue(s, st, "s1", story.TakeFinal)
	if err != nil || due {
		t.Fatalf("giudizio non recuperato: due=%v err=%v", due, err)
	}
	if e.hits() != 1 {
		t.Errorf("reinvio: %d", e.hits())
	}

	// ripresa diversa (altro contenuto): il giudizio non vale
	cutClip(t, e.video, "320x180", 25, "3")
	due, _, err = storyJudgeDue(s, st, "s1", story.TakeFinal)
	if err != nil || !due {
		t.Errorf("il giudizio vale per un'altra ripresa: due=%v err=%v", due, err)
	}
}

func TestStoryJudgeIncertoNonSiRilancia(t *testing.T) {
	e := newJudgeEnv(t, geminiText(judgeVerdict("aderente", "")))
	board := filepath.Join(e.dir, "spot.json")
	os.WriteFile(board, []byte(judgeBoard), 0o600)
	s, err := story.Load(board)
	if err != nil {
		t.Fatal(err)
	}
	ss := &story.SceneState{}
	ss.Clip = &story.Take{File: "clip.mp4", Done: time.Now(), Judgements: []story.Judgement{{Status: story.Incerto, Provider: "gemini", At: time.Now()}}}
	st := &story.State{Scenes: map[string]*story.SceneState{"s1": ss}}
	due, why, err := storyJudgeDue(s, st, "s1", story.TakeFinal)
	if err != nil || due || why == "" {
		t.Errorf("incerto: due=%v why=%q err=%v", due, why, err)
	}
	if e.hits() != 0 {
		t.Errorf("invii: %d", e.hits())
	}
}

func TestStoryJudgeSpentoNonDue(t *testing.T) {
	e := newJudgeEnv(t, geminiText(judgeVerdict("aderente", "")))
	board := filepath.Join(e.dir, "spot.json")
	off := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9"},"scene":[{"id":"s1","tipo":"video","prompt":"x"}]}`
	os.WriteFile(board, []byte(off), 0o600)
	s, err := story.Load(board)
	if err != nil {
		t.Fatal(err)
	}
	ss := &story.SceneState{}
	ss.Clip = &story.Take{File: "clip.mp4", Done: time.Now()}
	st := &story.State{Scenes: map[string]*story.SceneState{"s1": ss}}
	if due, _, _ := storyJudgeDue(s, st, "s1", story.TakeFinal); due || e.hits() != 0 {
		t.Errorf("senza controllo.aderenza: due=%v hits=%d", due, e.hits())
	}
}
