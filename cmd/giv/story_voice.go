package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"
	"unicode/utf8"

	"gen-image-video-cli/internal/httpx"
	"gen-image-video-cli/internal/output"
	"gen-image-video-cli/internal/speech"
	"gen-image-video-cli/internal/story"
)

// Voce fuori campo dai testi dello storyboard (V-T7): una scena con testo e
// senza voce (file) ha una voce generata da giv story run in
// voce/<id>-v<N>.wav, prima dei video, con la stessa riserva e lo stesso
// tetto di ogni richiesta a pagamento. Una voce fatta non si rifà da sola:
// testo o impostazioni cambiati sono un avviso, si rifà con --scene <id>
// --new-voice; un esito incerto resta contato e non si rilancia da solo (K7).

// storyVoiceDir è la cartella delle voci generate, dentro quella dello storyboard.
const storyVoiceDir = "voce"

// voiceCharsPerSecond è il ritmo con cui story check stima la durata di una
// voce non ancora generata (approssimato: lo dice l'avviso).
const voiceCharsPerSecond = 15.0

// needsVoice: la scena ha un testo da far dire e nessun file di voce (le
// scene avatar vogliono il file, che guida il labiale).
func needsVoice(sc *story.Scene) bool {
	return sc.Testo != "" && sc.Voce == "" && sc.Tipo != story.Avatar
}

// sceneVoice è la voce di una scena, controllata e stimata.
type sceneVoice struct {
	req   speech.Request
	est   *speech.Estimate // nil: non stimabile
	why   string           // perché non è stimabile
	key   string           // impronta di testo e impostazioni (voice_key nello stato)
	warns []string
}

// storyVoiceRequest è la richiesta di voce della scena: testo della scena,
// impostazioni del blocco parlato (già fuso col kit).
func storyVoiceRequest(s *story.Storyboard, scene *story.Scene) speech.Request {
	req := speech.Request{Text: scene.Testo}
	if p := s.Parlato; p != nil {
		req.Provider, req.Model, req.Voice, req.Language, req.Style = p.Provider, p.Modello, p.Voce, p.Lingua, p.Stile
		if p.Velocita != nil {
			req.Speed = *p.Velocita
		}
		if len(p.Pronuncia) > 0 {
			req.Dict = speech.Dictionary(maps.Clone(p.Pronuncia))
		}
	}
	return req
}

// voice controlla e stima la voce della scena (una volta sola per scena):
// errori d'uso a codice 2, senza chiave né chiamate.
func (sc *storyChecker) voice(scene *story.Scene) (*sceneVoice, error) {
	if v := sc.voices[scene.ID]; v != nil {
		return v, nil
	}
	req, warns, err := speech.Check(storyVoiceRequest(sc.s, scene))
	if err != nil {
		return nil, usageError("%v", err)
	}
	v := &sceneVoice{req: req, warns: warns, key: voiceKey(req)}
	v.est, v.why = speech.EstimateCost(req, time.Now())
	sc.voices[scene.ID] = v
	return v, nil
}

// voiceKey è l'impronta della richiesta di voce già controllata: cambia se
// cambiano testo, provider, voce, lingua, velocità, stile o pronunce.
func voiceKey(req speech.Request) string {
	data, _ := json.Marshal(struct {
		Provider, Model, Voice, Language, Style, Text string
		Speed                                         float64
		Dict                                          map[string]string
	}{req.Provider, req.Model, req.Voice, req.Language, req.Style, req.Text, req.Speed, req.Dict})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// voiceDuration è la durata stimata della voce dai caratteri e dalla velocità.
func voiceDuration(req speech.Request) float64 {
	d := float64(utf8.RuneCountInString(req.Text)) / voiceCharsPerSecond
	if req.Speed > 0 {
		d /= req.Speed
	}
	return math.Round(d*10) / 10
}

// checkVoice porta nella scena controllata da story check la voce dal testo:
// caratteri, stima, durata stimata o voce già generata nello stato, e gli
// avvisi (voce incerta, mancante o fatta con un testo diverso).
func (sc *storyChecker) checkVoice(scene *story.Scene, c *sceneCheck) error {
	if scene.Testo == "" {
		return nil
	}
	c.TextChars = utf8.RuneCountInString(scene.Testo)
	if !needsVoice(scene) {
		return nil
	}
	v, err := sc.voice(scene)
	if err != nil {
		return fmt.Errorf("voce: %w", err)
	}
	c.Warnings = append(c.Warnings, v.warns...)
	c.VoiceStatus = "da_generare"
	c.VoiceDurationEstS = voiceDuration(v.req)
	if v.est != nil {
		usd := v.est.USD
		c.VoiceCostEstUSD = &usd
	} else {
		sc.notEstimated = append(sc.notEstimated, scene.ID+" (voce)")
		c.Warnings = append(c.Warnings, "voce: stima del costo non disponibile: "+v.why)
	}
	ss := sc.state.Scenes[scene.ID]
	if ss == nil {
		return nil
	}
	switch {
	case ss.VoiceStatus == story.Incerto:
		c.VoiceStatus = story.Incerto
		c.Warnings = append(c.Warnings, fmt.Sprintf("voce: l'ultima richiesta è rimasta senza esito e può essere stata addebitata (contata nella spesa): controlla sul provider, poi rifalla con giv story run --scene %s --new-voice", scene.ID))
	case ss.Voice != nil:
		c.Voice, c.VoiceStatus, c.VoiceDurationEstS = ss.Voice.File, story.Fatto, 0
		if _, err := sc.s.CheckFile(ss.Voice.File); err != nil {
			c.Warnings = append(c.Warnings, fmt.Sprintf("voce: la voce registrata %s non c'è più: giv story cut la rifiuterà; per rifarla (a pagamento) --scene %s --new-voice", ss.Voice.File, scene.ID))
		} else if ss.VoiceKey != v.key {
			c.VoiceStatus = "cambiata"
			c.Warnings = append(c.Warnings, fmt.Sprintf("voce: testo o impostazioni cambiati dopo la generazione: giv story cut usa la voce già fatta (%s); per rifarla (a pagamento) --scene %s --new-voice", ss.Voice.File, scene.ID))
		}
	}
	return nil
}

// voicesToMake sono le scene di cui giv story run genera la voce, in ordine:
// con --scene solo quella; una voce già fatta solo con --new-voice.
func (r *storyRunner) voicesToMake() []*story.Scene {
	var out []*story.Scene
	for i := range r.s.Scene {
		scene := &r.s.Scene[i]
		if !needsVoice(scene) || (r.forced != "" && scene.ID != r.forced) {
			continue
		}
		ss := r.st.Scenes[scene.ID]
		if scene.ID == r.newVoice || ss == nil || (ss.Voice == nil && ss.VoiceStatus != story.Incerto) {
			out = append(out, scene)
			continue
		}
		if ss.VoiceStatus == story.Incerto || !r.exists(ss.Voice.File) {
			out = append(out, scene) // rifiutata in voicePreflight
		}
	}
	return out
}

// voicePreflight controlla le voci da fare prima di ogni avvio: voce incerta o
// registrata ma mancante (si rifà solo con --new-voice), richiesta, stima (con
// un tetto, una voce non stimabile ferma tutto) e client del provider (chiave).
func (r *storyRunner) voicePreflight() error {
	var rejects []error
	for _, scene := range r.voicesToMake() {
		ss := r.st.Scenes[scene.ID]
		if scene.ID != r.newVoice && ss != nil {
			switch {
			case ss.VoiceStatus == story.Incerto:
				rejects = append(rejects, fmt.Errorf("scena %s: l'ultima richiesta della voce è rimasta senza esito o senza file salvato e può essere stata addebitata (contata nella spesa): controlla sul provider, poi rifalla con --scene %s --new-voice", scene.ID, scene.ID))
				continue
			case ss.Voice != nil:
				rejects = append(rejects, fmt.Errorf("scena %s: la voce registrata %s non c'è più: per generarne una nuova (a pagamento) giv story run --scene %s --new-voice %s", scene.ID, ss.Voice.File, scene.ID, r.path))
				continue
			}
		}
		v, err := r.sc.voice(scene)
		if err != nil {
			rejects = append(rejects, fmt.Errorf("scena %s: voce: %w", scene.ID, err))
			continue
		}
		for _, w := range v.warns {
			fmt.Fprintf(os.Stderr, "avviso: scena %s: voce: %s\n", scene.ID, w)
		}
		if v.est == nil {
			if r.s.TettoCostoUSD != nil {
				rejects = append(rejects, fmt.Errorf("scena %s: voce non stimabile (%s): con tetto_costo_usd non parte", scene.ID, v.why))
				continue
			}
			fmt.Fprintf(os.Stderr, "avviso: scena %s: voce senza stima del costo (non contata nel totale): %s\n", scene.ID, v.why)
		}
		r.voiceOrder = append(r.voiceOrder, scene)
	}
	if len(rejects) > 0 {
		return storyError(r.st.Path(), errors.Join(rejects...))
	}
	for _, scene := range r.voiceOrder {
		p := r.sc.voices[scene.ID].req.Provider
		if r.speech[p] != nil {
			continue
		}
		c, err := newSpeechProvider(p)
		if err != nil {
			return err
		}
		r.speech[p] = c
	}
	return nil
}

// voiceNext è la stima non arrotondata delle voci ancora da chiedere.
func (r *storyRunner) voiceNext() float64 {
	var next float64
	for _, scene := range r.voiceOrder {
		if v := r.sc.voices[scene.ID]; !r.voiceSent[scene.ID] && v.est != nil {
			next += v.est.Exact
		}
	}
	return next
}

// makeVoices genera, in ordine, le voci da fare: la prima che non riesce
// ferma l'esecuzione prima dei video.
func (r *storyRunner) makeVoices(ctx context.Context) error {
	for _, scene := range r.voiceOrder {
		if err := r.makeVoice(ctx, scene); err != nil {
			return fmt.Errorf("scena %s: voce: %w", scene.ID, err)
		}
	}
	return nil
}

// makeVoice genera la voce della scena: riserva scritta nello stato prima
// della richiesta (con il tetto ricontrollato sulla spesa reale), una sola
// richiesta mai ripetuta, file nuovi in voce/ e stato. Un esito incerto, o una
// voce arrivata ma non salvata, resta riservata e incerta: si rifà solo con
// --scene --new-voice. Una richiesta rifiutata (4xx) toglie la riserva.
func (r *storyRunner) makeVoice(ctx context.Context, scene *story.Scene) error {
	v := r.sc.voices[scene.ID]
	ss := r.st.Scene(scene.ID)
	ch := story.Charge{Kind: story.ChargeVoice}
	if v.est != nil {
		e := v.est.Exact
		ch.CostEstUSD = &e
	}
	prevStatus, prevError, err := r.reserveVoice(scene.ID, ss, ch)
	if err != nil {
		return err
	}
	k := len(ss.Charges) - 1
	man := manifest{Command: "story voice", Provider: v.req.Provider, Model: voiceModel(v.req), Prompt: v.req.Text,
		Params: map[string]any{"scene": scene.ID, "voice": v.req.Voice, "language": v.req.Language, "chars": utf8.RuneCountInString(v.req.Text)}}
	if v.est != nil {
		man.CostEstUSD, man.CostSource = v.est.USD, v.est.Source
	}
	fmt.Fprintf(os.Stderr, "scena %s: voce con %s (voce %s, %d caratteri)…\n", scene.ID, v.req.Provider, v.req.Voice, utf8.RuneCountInString(v.req.Text))
	res, err := r.speech[v.req.Provider].Synthesize(ctx, v.req)
	if err != nil {
		var se *httpx.StartError
		if errors.As(err, &se) {
			ss.VoiceError = err.Error() // resta incerta e contata
			r.save()
			failed := man
			failed.Status, failed.Files = "failed", []output.FileInfo{}
			logRun(failed)
			return &cliError{code: exitAPI, msg: fmt.Sprintf("voce dall'esito incerto: %v — può essere stata addebitata (contata nella spesa) e non si rilancia da sola: controlla sul provider, poi giv story run --scene %s --new-voice %s", se.Err, scene.ID, r.path)}
		}
		// rifiutata o mai partita: niente addebito, si toglie solo la sua
		// riserva; una richiesta di prima rimasta incerta resta incerta (K7)
		ss.Charges = slices.Delete(ss.Charges, k, k+1)
		if prevStatus == story.Incerto {
			ss.VoiceStatus, ss.VoiceError = prevStatus, prevError
		} else {
			ss.VoiceStatus, ss.VoiceError = voiceIdle(ss), err.Error()
		}
		r.save()
		return voiceError(v.req.Provider, err)
	}
	r.man.Voices++
	if res.CostUSD > 0 {
		ss.Charges[k].CostUSD = res.CostUSD
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "avviso: scena %s: voce: %s\n", scene.ID, w)
		man.Warnings = append(man.Warnings, w)
	}
	rel, files, err := r.saveVoice(scene, ss, res, v)
	if err != nil {
		ss.VoiceError = "voce generata (e addebitata) ma non salvata: " + err.Error() // resta incerta e contata
		r.save()
		return errors.New(ss.VoiceError)
	}
	if ss.Voice != nil {
		old := *ss.Voice
		old.Kind = story.TakeVoice
		ss.Previous = append(ss.Previous, old)
	}
	ss.Voice = &story.Take{File: rel, Done: time.Now()}
	ss.VoiceKey, ss.VoiceStatus, ss.VoiceError = v.key, story.Fatto, ""
	r.voiceThisRun[scene.ID] = "generata"
	man.Files, man.DurationS, man.CostUSD = files, res.DurationS, res.CostUSD
	logRun(man)
	if err := r.st.Save(); err != nil {
		return fmt.Errorf("voce %s generata ma stato non salvato, nessuna altra richiesta inviata: %w", rel, err)
	}
	return nil
}

// reserveVoice ricontrolla il tetto con la spesa fatta fin qui (più le voci e
// i video ancora da fare) e registra la voce ch come incerta nel file di
// stato, prima di inviarla: se lo stato non si salva la richiesta non parte.
// Restituisce stato ed errore della voce di prima della riserva.
func (r *storyRunner) reserveVoice(id string, ss *story.SceneState, ch story.Charge) (string, string, error) {
	if err := r.checkCap(0); err != nil {
		return "", "", err
	}
	status, msg := ss.VoiceStatus, ss.VoiceError
	ch.At = time.Now()
	ss.Charges = append(ss.Charges, ch)
	ss.VoiceStatus, ss.VoiceError = story.Incerto, ""
	if err := r.st.Save(); err != nil {
		ss.Charges = ss.Charges[:len(ss.Charges)-1]
		ss.VoiceStatus, ss.VoiceError = status, msg
		return "", "", fmt.Errorf("stato non salvato, nessuna richiesta inviata: %w", err)
	}
	r.voiceSent[id] = true
	return status, msg, nil
}

// voiceIdle è lo stato della voce senza richieste in corso: fatta se ce n'è
// una in uso, altrimenti da fare.
func voiceIdle(ss *story.SceneState) string {
	if ss.Voice != nil {
		return story.Fatto
	}
	return story.DaFare
}

// saveVoice scrive la voce in voce/<id>-v<N>.wav (e i tempi, se ci sono, in
// voce/<id>-v<N>.tempi.json) dentro la cartella dello storyboard: cartella
// aperta da root (una cartella voce che porta fuori, anche con un link, è
// rifiutata), file nuovi (un nome che c'è già, anche come link, non si
// sovrascrive: si passa al numero dopo). Restituisce il WAV relativo alla
// cartella dello storyboard.
func (r *storyRunner) saveVoice(scene *story.Scene, ss *story.SceneState, res *speech.Result, v *sceneVoice) (string, []output.FileInfo, error) {
	if err := r.root.MkdirAll(storyVoiceDir, 0o755); err != nil {
		return "", nil, err
	}
	dir, err := r.root.OpenRoot(storyVoiceDir)
	if err != nil {
		return "", nil, fmt.Errorf("cartella %s: %w", storyVoiceDir, err)
	}
	defer dir.Close()
	write := func(name string, data []byte) error {
		f, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			dir.Remove(name)
		}
		return err
	}
	var base string
	for n := ss.VoiceTakes() + 1; ; n++ {
		base = fmt.Sprintf("%s-v%d", scene.ID, n)
		err = write(base+".wav", res.Audio)
		if errors.Is(err, fs.ErrExist) && n < ss.VoiceTakes()+100 {
			continue
		}
		break
	}
	if err != nil {
		return "", nil, fmt.Errorf("%s/%s.wav: %w", storyVoiceDir, base, err)
	}
	files := []output.FileInfo{{Path: filepath.Join(r.s.Dir, storyVoiceDir, base+".wav"), Mime: "audio/wav", Bytes: len(res.Audio)}}
	if res.Timings != nil {
		data, err := speech.MarshalTimings(speech.TimingsFile{Provider: v.req.Provider, Text: v.req.Text, DurationS: res.DurationS, Chars: res.Timings})
		if err == nil {
			err = write(base+".tempi.json", data)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "avviso: scena %s: tempi della voce non salvati (%v): i sottotitoli si distribuiranno sulla durata\n", scene.ID, err)
		} else {
			files = append(files, output.FileInfo{Path: filepath.Join(r.s.Dir, storyVoiceDir, base+".tempi.json"), Mime: "application/json", Bytes: len(data)})
		}
	}
	return storyVoiceDir + "/" + base + ".wav", files, nil
}
