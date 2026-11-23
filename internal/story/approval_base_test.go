package story

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// storyboard: s1 e s3 indipendenti, s2 continua da s1.
func nuovoStoryboard(t *testing.T) (*Storyboard, string) {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"a.png", "c.png", "bozza1.mp4", "bozza2.mp4", "bozza3.mp4"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("dati "+f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sb := `{"titolo":"t","formato":{"aspect":"16:9","risoluzione":"720p","fps":24},
	 "default":{"provider":"xai","modello":"m","audio":"off","bozza":{"modello":"mb"}},
	 "scene":[
	  {"id":"s1","tipo":"video","durata":5,"immagine":"a.png","prompt":"uno"},
	  {"id":"s2","tipo":"video","durata":5,"continua_da":"s1","prompt":"due"},
	  {"id":"s3","tipo":"video","durata":5,"immagine":"c.png","prompt":"tre"}]}`
	p := filepath.Join(dir, "spot.json")
	if err := os.WriteFile(p, []byte(sb), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	return s, p
}

func approva(t *testing.T, s *Storyboard, st *State, id, file string) {
	t.Helper()
	st.Scene(id).TrackOf(TakeDraft).Clip = &Take{File: file}
	r, err := s.RecipeKey(id, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Approve(s, id, TakeDraft, r); err != nil {
		t.Fatal(err)
	}
}

func valida(t *testing.T, s *Storyboard, st *State, id string) error {
	t.Helper()
	r, err := s.RecipeKey(id, "")
	if err != nil {
		t.Fatal(err)
	}
	return st.CheckApproval(s, id, r)
}

// Un'approvazione resta valida se cambia un'altra scena indipendente e cade
// se cambia la ricetta della sua scena (o di quella da cui continua).
func TestBaseApprovazioneRestaOCadeConLaRicetta(t *testing.T) {
	s, path := nuovoStoryboard(t)
	st := newState(StatePath(path))
	if err := valida(t, s, st, "s1"); !errors.Is(err, ErrNotApproved) {
		t.Fatalf("senza approvazione: %v", err)
	}
	if _, err := st.Approve(s, "s1", TakeDraft, "x"); err == nil {
		t.Fatal("ricetta non valida accettata")
	}
	approva(t, s, st, "s1", "bozza1.mp4")
	approva(t, s, st, "s2", "bozza2.mp4")
	approva(t, s, st, "s3", "bozza3.mp4")
	for _, id := range []string{"s1", "s2", "s3"} {
		if err := valida(t, s, st, id); err != nil {
			t.Fatalf("%s subito dopo l'approvazione: %v", id, err)
		}
	}
	// la sola approvazione non scrive nulla: lo stato si salva a parte
	if _, err := os.Stat(StatePath(path)); err == nil {
		t.Fatal("approvare in memoria ha scritto il file")
	}

	// cambia la scena indipendente s3: s1 e s2 restano valide, s3 cade
	data, _ := os.ReadFile(path)
	mod := []byte(string(data))
	mod = []byte(replaceOnce(string(mod), `"prompt":"tre"`, `"prompt":"tre diverso"`))
	if err := os.WriteFile(path, mod, 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := valida(t, s2, st, "s1"); err != nil {
		t.Fatalf("s1 non doveva cadere per un cambio in s3: %v", err)
	}
	if err := valida(t, s2, st, "s2"); err != nil {
		t.Fatalf("s2: %v", err)
	}
	if err := valida(t, s2, st, "s3"); !errors.Is(err, ErrStaleApproval) {
		t.Fatalf("s3 doveva cadere: %v", err)
	}

	// cambia il riferimento di s1: cadono s1 e s2 (continua da s1), non s3 originale
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "a.png"), []byte("altro riferimento"), 0o644); err != nil {
		t.Fatal(err)
	}
	s3, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := valida(t, s3, st, "s1"); !errors.Is(err, ErrStaleApproval) {
		t.Fatalf("s1 doveva cadere: %v", err)
	}
	if err := valida(t, s3, st, "s2"); !errors.Is(err, ErrStaleApproval) {
		t.Fatalf("s2 (continua da s1) doveva cadere: %v", err)
	}
	if err := valida(t, s3, st, "s3"); err != nil {
		t.Fatalf("s3 indipendente non doveva cadere: %v", err)
	}
}

// Una nuova ripresa o un file cambiato rendono vecchia l'approvazione; senza
// ripresa non si approva.
func TestBaseApprovazioneNuovaRipresaEMancante(t *testing.T) {
	s, path := nuovoStoryboard(t)
	st := newState(StatePath(path))
	if _, err := st.Approve(s, "s1", TakeDraft, "0000000000000000000000000000000000000000000000000000000000000000"); !errors.Is(err, ErrNothingToApprove) {
		t.Fatalf("senza ripresa: %v", err)
	}
	approva(t, s, st, "s1", "bozza1.mp4")
	st.Scenes["s1"].Draft.Clip = &Take{File: "bozza2.mp4"} // nuova bozza
	if err := valida(t, s, st, "s1"); !errors.Is(err, ErrStaleApproval) {
		t.Fatalf("nuova bozza: %v", err)
	}
	st.Scenes["s1"].Draft.Clip = &Take{File: "bozza1.mp4"}
	if err := valida(t, s, st, "s1"); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(filepath.Dir(path), "bozza1.mp4"), []byte("rifatta"), 0o644)
	if err := valida(t, s, st, "s1"); !errors.Is(err, ErrStaleApproval) {
		t.Fatalf("file cambiato: %v", err)
	}
}

// Spostare la cartella del progetto non cambia la ricetta.
func TestBaseRicettaStabileSpostandoLaCartella(t *testing.T) {
	s, path := nuovoStoryboard(t)
	k1, err := s.RecipeKey("s1", "")
	if err != nil {
		t.Fatal(err)
	}
	nuova := filepath.Join(t.TempDir(), "altro")
	if err := os.Rename(filepath.Dir(path), nuova); err != nil {
		t.Fatal(err)
	}
	s2, err := Load(filepath.Join(nuova, "spot.json"))
	if err != nil {
		t.Fatal(err)
	}
	if k2, _ := s2.RecipeKey("s1", ""); k1 != k2 {
		t.Fatal("la ricetta cambia con la cartella")
	}
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}

// Fotogramma approvato poi default.bozza (e il contrario): non vale più; un
// fotogramma riscritto nello stesso file: non vale più.
func TestBaseApprovazioneMaterialeCambiato(t *testing.T) {
	s, path := nuovoStoryboard(t) // con default.bozza
	st := newState(StatePath(path))
	st.Scene("s3").Keyframe = &Take{File: "c.png"}
	r, _ := s.RecipeKey("s3", "")
	if _, err := st.Approve(s, "s3", TakeKeyframe, r); err == nil {
		t.Fatal("fotogramma approvato con default.bozza")
	}
	data, _ := os.ReadFile(path)
	senza := replaceOnce(string(data), `,"bozza":{"modello":"mb"}`, ``)
	if senza == string(data) {
		t.Fatal("fixture non modificata")
	}
	os.WriteFile(path, []byte(senza), 0o644)
	sk, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, _ = sk.RecipeKey("s3", "")
	if _, err := st.Approve(sk, "s3", TakeKeyframe, r); err != nil {
		t.Fatal(err)
	}
	if err := st.CheckApproval(sk, "s3", r); err != nil {
		t.Fatal(err)
	}
	// stesso file, contenuto riscritto: stale
	os.WriteFile(filepath.Join(filepath.Dir(path), "c.png"), []byte("riscritto"), 0o644)
	r2, _ := sk.RecipeKey("s3", "")
	if err := st.CheckApproval(sk, "s3", r2); !errors.Is(err, ErrStaleApproval) {
		t.Fatalf("fotogramma riscritto: %v", err)
	}
	// ripristino il contenuto; poi torna default.bozza: la sua approvazione cade
	os.WriteFile(filepath.Join(filepath.Dir(path), "c.png"), []byte("dati c.png"), 0o644)
	os.WriteFile(path, data, 0o644)
	sb, _ := Load(path)
	r3, _ := sb.RecipeKey("s3", "")
	if err := st.CheckApproval(sb, "s3", r3); !errors.Is(err, ErrStaleApproval) {
		t.Fatalf("fotogramma approvato, poi default.bozza: %v", err)
	}
}

// continua_da: B approvata prima che A abbia la clip; A registrata e fissata
// (PinSource): B vale; file di A sostituito o clip rigenerata: B cade; clip di
// A senza PinSource: B cade.
func TestBaseApprovazioneClipDiPartenza(t *testing.T) {
	prepara := func(t *testing.T) (*Storyboard, *State, string) {
		s, path := nuovoStoryboard(t)
		st := newState(StatePath(path))
		approva(t, s, st, "s2", "bozza2.mp4") // s2 continua da s1, s1 senza clip
		if err := valida(t, s, st, "s2"); err != nil {
			t.Fatalf("senza clip di partenza: %v", err)
		}
		return s, st, filepath.Dir(path)
	}
	registra := func(t *testing.T, s *Storyboard, st *State, dir, file, contenuto string) {
		os.WriteFile(filepath.Join(dir, file), []byte(contenuto), 0o644)
		st.Scene("s1").Clip = &Take{File: file}
	}
	t.Run("fissata", func(t *testing.T) {
		s, st, dir := prepara(t)
		registra(t, s, st, dir, "a1.mp4", "clip a")
		if err := st.PinSource(s, "s1"); err != nil {
			t.Fatal(err)
		}
		if err := valida(t, s, st, "s2"); err != nil {
			t.Fatalf("clip fissata: %v", err)
		}
		os.WriteFile(filepath.Join(dir, "a1.mp4"), []byte("sostituita"), 0o644)
		if err := valida(t, s, st, "s2"); !errors.Is(err, ErrStaleApproval) {
			t.Fatalf("file sostituito: %v", err)
		}
	})
	t.Run("rigenerata", func(t *testing.T) {
		s, st, dir := prepara(t)
		registra(t, s, st, dir, "a1.mp4", "clip a")
		st.PinSource(s, "s1")
		registra(t, s, st, dir, "a2.mp4", "clip a2")
		st.PinSource(s, "s1") // non cambia una sorgente già fissata
		if err := valida(t, s, st, "s2"); !errors.Is(err, ErrStaleApproval) {
			t.Fatalf("clip rigenerata: %v", err)
		}
	})
	t.Run("senza PinSource", func(t *testing.T) {
		s, st, dir := prepara(t)
		registra(t, s, st, dir, "a1.mp4", "clip a")
		if err := valida(t, s, st, "s2"); !errors.Is(err, ErrStaleApproval) {
			t.Fatalf("clip non fissata: %v", err)
		}
	})
}

// B approvata prima che A abbia la clip, poi B tolta dallo storyboard: PinSource
// fissa comunque l'approvazione nello stato. Se A1 è già stata sostituita da A2
// (A1 fra le precedenti), PinSource non fissa e B resta vecchia.
func TestBasePinSourceSceneTolteESostituite(t *testing.T) {
	prepara := func(t *testing.T) (*Storyboard, *State, string, string) {
		s, path := nuovoStoryboard(t)
		st := newState(StatePath(path))
		approva(t, s, st, "s2", "bozza2.mp4")
		return s, st, filepath.Dir(path), path
	}
	t.Run("B tolta dallo storyboard", func(t *testing.T) {
		s, st, dir, path := prepara(t)
		senza := `{"titolo":"t","formato":{"aspect":"16:9","risoluzione":"720p","fps":24},
	 "default":{"provider":"xai","modello":"m","audio":"off","bozza":{"modello":"mb"}},
	 "scene":[
	  {"id":"s1","tipo":"video","durata":5,"immagine":"a.png","prompt":"uno"},
	  {"id":"s3","tipo":"video","durata":5,"immagine":"c.png","prompt":"tre"}]}`
		os.WriteFile(path, []byte(senza), 0o644)
		s2, err := Load(path)
		if err != nil {
			t.Fatalf("storyboard senza s2: %v\n%s", err, senza)
		}
		os.WriteFile(filepath.Join(dir, "a1.mp4"), []byte("clip a"), 0o644)
		st.Scene("s1").Clip = &Take{File: "a1.mp4"}
		if err := st.PinSource(s2, "s1"); err != nil {
			t.Fatal(err)
		}
		if src := st.Scenes["s2"].Approval.Source; src == nil || src.File != "a1.mp4" || src.SHA256 == "" {
			t.Fatalf("sorgente non fissata: %+v", src)
		}
		_ = s
	})
	t.Run("A già sostituita", func(t *testing.T) {
		s, st, dir, _ := prepara(t)
		os.WriteFile(filepath.Join(dir, "a1.mp4"), []byte("clip a"), 0o644)
		os.WriteFile(filepath.Join(dir, "a2.mp4"), []byte("clip a2"), 0o644)
		a := st.Scene("s1")
		a.Clip = &Take{File: "a2.mp4"}
		a.Previous = []Take{{File: "a1.mp4"}}
		st.PinSource(s, "s1")
		if src := st.Scenes["s2"].Approval.Source; src != nil && src.File != "" {
			t.Fatalf("fissata su una clip già sostituita: %+v", src)
		}
		if err := valida(t, s, st, "s2"); !errors.Is(err, ErrStaleApproval) {
			t.Fatalf("B doveva essere vecchia: %v", err)
		}
	})
}
