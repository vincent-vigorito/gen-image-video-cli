package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// CLI-1012: approvazioni, bozza e montato BOZZA (filone B della 0.13.0).
// Solo locale: OpenRouter finto (capsServer) col catalogo, nessun avvio.

const bBoard = `{"approvazione":true,
"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9",
 "bozza":{"modello":"alibaba/wan-3.0","risoluzione":"480p"}},
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},
"scene":[%s]}`

func bScene(id, extra string) string {
	return fmt.Sprintf(`{"id":"%s","tipo":"video","durata":5,"prompt":"p %s"%s}`, id, id, extra)
}

// bEnv: cartella con storyboard (s1, s2 continua da s1, s3) e bozze finte per
// le scene in drafts.
func bEnv(t *testing.T, drafts ...string) (dir, board string) {
	t.Helper()
	capsServer(t)
	dir = t.TempDir()
	board = filepath.Join(dir, "spot.json")
	sb := fmt.Sprintf(bBoard, strings.Join([]string{bScene("s1", ""), bScene("s2", `,"continua_da":"s1"`), bScene("s3", "")}, ","))
	if err := os.WriteFile(board, []byte(sb), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "clip", "bozza"), 0o755); err != nil {
		t.Fatal(err)
	}
	scenes := []string{}
	for _, id := range drafts {
		f := "clip/bozza/" + id + ".mp4"
		if err := os.WriteFile(filepath.Join(dir, f), []byte("bozza "+id), 0o644); err != nil {
			t.Fatal(err)
		}
		scenes = append(scenes, fmt.Sprintf(`"%s":{"status":"da_fare","draft":{"status":"fatto","clip":{"file":%q,"done":"2026-11-23T10:00:00Z"}}}`, id, f))
	}
	st := `{"version":1,"scenes":{` + strings.Join(scenes, ",") + `}}`
	if err := os.WriteFile(filepath.Join(dir, "spot.stato.json"), []byte(st), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, board
}

func bRun(t *testing.T, args ...string) (out, errOut string, code int) {
	t.Helper()
	grab := func(f **os.File) func() string {
		old := *f
		r, w, _ := os.Pipe()
		*f = w
		done := make(chan string)
		go func() { b, _ := readAllFile(r); done <- string(b) }()
		return func() string { w.Close(); *f = old; return <-done }
	}
	so, se := grab(&os.Stdout), grab(&os.Stderr)
	err := cmdStory(args)
	out, errOut = so(), se()
	if err != nil {
		code, _ = exitCode(err)
		errOut += err.Error()
	}
	return
}

type bApproveOut struct {
	Material string `json:"material"`
	Approved []struct {
		ID     string `json:"id"`
		Take   string `json:"take"`
		File   string `json:"file"`
		SHA256 string `json:"sha256"`
	} `json:"approved"`
	Skipped []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"skipped"`
}

func bApprovals(t *testing.T, dir string) map[string]bool {
	t.Helper()
	var st struct {
		Scenes map[string]struct {
			Approval *struct{} `json:"approval"`
		} `json:"scenes"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "spot.stato.json"))
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	m := map[string]bool{}
	for id, s := range st.Scenes {
		m[id] = s.Approval != nil
	}
	return m
}

// Due scene approvate su tre: l'uscita identifica il materiale (file e
// impronta), lo stato registra solo quelle due.
func TestBApproveDueSuTre(t *testing.T) {
	dir, board := bEnv(t, "s1", "s2", "s3")
	out, se, code := bRun(t, "approve", board, "s1", "s2")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	var r bApproveOut
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r.Material != "bozza" || len(r.Approved) != 2 {
		t.Fatalf("uscita: %s", out)
	}
	for _, a := range r.Approved {
		sum := sha256.Sum256([]byte("bozza " + a.ID))
		if a.Take != "bozza" || a.File != "clip/bozza/"+a.ID+".mp4" || a.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("materiale non identificato: %+v", a)
		}
	}
	if got := bApprovals(t, dir); !got["s1"] || !got["s2"] || got["s3"] {
		t.Fatalf("approvazioni nello stato: %v", got)
	}
	// check: s1 e s2 pronte, s3 da approvare; i definitivi non approvati restano fuori dalla stima
	cout, cse, ccode := bRun(t, "check", board)
	if ccode != 0 {
		t.Fatalf("check code=%d %s", ccode, cse)
	}
	var c struct {
		Scenes []struct {
			ID       string `json:"id"`
			Approval string `json:"approval"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal([]byte(cout), &c); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"s1": "pronta", "s2": "pronta", "s3": "da_approvare"}
	for _, s := range c.Scenes {
		if s.Approval != want[s.ID] {
			t.Errorf("scena %s: approval %q, atteso %q", s.ID, s.Approval, want[s.ID])
		}
	}
	// cambio del riferimento (prompt) di s1: cadono s1 e s2 (continua da s1), non s3
	data, _ := os.ReadFile(board)
	if err := os.WriteFile(board, bytes.Replace(data, []byte(`"p s1"`), []byte(`"p s1 nuovo"`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	cout, _, _ = bRun(t, "check", board)
	c.Scenes = nil
	json.Unmarshal([]byte(cout), &c)
	want = map[string]string{"s1": "da_approvare", "s2": "da_approvare", "s3": "da_approvare"}
	for _, s := range c.Scenes {
		if s.Approval != want[s.ID] {
			t.Errorf("dopo il cambio, scena %s: %q, atteso %q", s.ID, s.Approval, want[s.ID])
		}
	}
}

// La revoca tocca solo le scene che dipendono: s3 approvata resta pronta.
func TestBApprovazioneIndipendenteResta(t *testing.T) {
	_, board := bEnv(t, "s1", "s2", "s3")
	if _, se, code := bRun(t, "approve", board, "s1", "s2", "s3"); code != 0 {
		t.Fatalf("%d %s", code, se)
	}
	data, _ := os.ReadFile(board)
	os.WriteFile(board, bytes.Replace(data, []byte(`"p s1"`), []byte(`"p s1 nuovo"`), 1), 0o644)
	cout, _, _ := bRun(t, "check", board)
	var c struct {
		Scenes []struct {
			ID       string `json:"id"`
			Approval string `json:"approval"`
		} `json:"scenes"`
	}
	json.Unmarshal([]byte(cout), &c)
	want := map[string]string{"s1": "da_approvare", "s2": "da_approvare", "s3": "pronta"}
	for _, s := range c.Scenes {
		if s.Approval != want[s.ID] {
			t.Errorf("scena %s: %q, atteso %q", s.ID, s.Approval, want[s.ID])
		}
	}
}

// --all approva chi ha materiale ed elenca le saltate; una scena indicata
// senza materiale: codice 2 e stato invariato.
func TestBApproveAllESaltate(t *testing.T) {
	dir, board := bEnv(t, "s1", "s2")
	out, se, code := bRun(t, "approve", "--all", board)
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	var r bApproveOut
	json.Unmarshal([]byte(out), &r)
	if len(r.Approved) != 2 || len(r.Skipped) != 1 || r.Skipped[0].ID != "s3" || r.Skipped[0].Reason == "" {
		t.Fatalf("uscita: %s", out)
	}
	dir2, board2 := bEnv(t, "s1")
	before, _ := os.ReadFile(filepath.Join(dir2, "spot.stato.json"))
	if _, _, code := bRun(t, "approve", board2, "s1", "s3"); code != 2 {
		t.Fatalf("scena senza materiale: code=%d, atteso 2", code)
	}
	after, _ := os.ReadFile(filepath.Join(dir2, "spot.stato.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("stato cambiato da un'approvazione rifiutata")
	}
	_ = dir
}

// Storyboard 0.12.x senza opzioni nuove: check senza campi nuovi.
func TestBCheck012SenzaCampiNuovi(t *testing.T) {
	out, se, code, starts := storyCheckRun(t, sb("", sc("s1", ""), sc("s2", `,"continua_da":"s1"`)))
	if code != 0 || starts != 0 {
		t.Fatalf("code=%d starts=%d %s", code, starts, se)
	}
	for _, k := range []string{`"approval"`, `"draft"`, `"drafts_cost_est_usd"`, `"finals_cost_est_usd"`, "da_approvare"} {
		if strings.Contains(out, k) {
			t.Errorf("campo nuovo %s in uno storyboard 0.12: %s", k, out)
		}
	}
}

func bSum(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// cut --bozza: scritta BOZZA, nessun file definitivo toccato; --export rifiutato.
func TestBCutBozza(t *testing.T) {
	cutNeedFF(t)
	capsServer(t)
	dir := t.TempDir()
	board := `{"default":{"provider":"openrouter","modello":"alibaba/wan-3.0","audio":"off","risoluzione":"720p","aspect":"16:9","bozza":{"modello":"alibaba/wan-3.0"}},
"formato":{"aspect":"16:9","risoluzione":"144p","fps":25},
"scene":[` + sc("s1", "") + `,` + sc("s2", "") + `]}`
	if err := os.WriteFile(filepath.Join(dir, "spot.json"), []byte(board), 0o644); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "clip", "bozza"), 0o755)
	st := `{"version":1,"scenes":{`
	for i, id := range []string{"s1", "s2"} {
		fin, dr := "clip-"+id+".mp4", "clip/bozza/"+id+".mp4"
		cutClip(t, filepath.Join(dir, fin), "160x90", 25, "5")
		cutClip(t, filepath.Join(dir, dr), "160x90", 25, "5")
		if i > 0 {
			st += ","
		}
		st += fmt.Sprintf(`%q:{"status":"fatto","clip":{"file":%q,"done":"2026-11-23T10:00:00Z"},"draft":{"status":"fatto","clip":{"file":%q,"done":"2026-11-23T10:00:00Z"}}}`, id, fin, dr)
	}
	st += `}}`
	os.WriteFile(filepath.Join(dir, "spot.stato.json"), []byte(st), 0o644)
	p := filepath.Join(dir, "spot.json")

	if _, se, code := bRun(t, "cut", p); code != 0 {
		t.Fatalf("cut definitivo: code=%d %s", code, se)
	}
	prot := []string{"spot.mp4", "spot.stato.json", "clip-s1.mp4", "clip-s2.mp4", "clip/bozza/s1.mp4"}
	for _, f := range []string{"foglio-contatto.jpg", "spot.controllo.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			prot = append(prot, f)
		}
	}
	before := map[string]string{}
	for _, f := range prot {
		before[f] = bSum(t, filepath.Join(dir, f))
	}
	out, se, code := bRun(t, "cut", "--bozza", p)
	if code != 0 {
		t.Fatalf("cut --bozza: code=%d %s", code, se)
	}
	if !strings.Contains(out, `"draft": true`) && !strings.Contains(out, `"draft":true`) {
		t.Errorf("uscita senza draft:true: %s", out)
	}
	for _, f := range prot {
		if bSum(t, filepath.Join(dir, f)) != before[f] {
			t.Errorf("%s modificato da cut --bozza", f)
		}
	}
	// scritta BOZZA: l'angolo in alto a sinistra differisce dal montato definitivo
	// (stesse clip), quello in basso a destra no
	crop := func(file, area string) string {
		o, err := execOut(t, "ffmpeg", "-v", "error", "-i", filepath.Join(dir, file), "-frames:v", "1",
			"-vf", "crop="+area, "-f", "md5", "-")
		if err != nil {
			t.Fatalf("ffmpeg: %v %s", err, o)
		}
		return o
	}
	if crop("spot.mp4", "100:25:0:0") == crop("spot.bozza.mp4", "100:25:0:0") {
		t.Error("nessuna scritta in alto a sinistra nel montato bozza")
	}
	if _, _, code := bRun(t, "cut", "--bozza", "--export", "instagram", p); code != 2 {
		t.Errorf("--bozza --export: code=%d, atteso 2", code)
	}
}

func execOut(t *testing.T, name string, args ...string) (string, error) {
	t.Helper()
	b, err := exec.Command(name, args...).CombinedOutput()
	return string(b), err
}

// Bozza approvata, poi default.bozza tolto: l'approvazione non vale più per il
// materiale che lo storyboard chiede oggi; approve senza bozza: codice 2.
func TestBApprovazioneBozzaPoiTolta(t *testing.T) {
	dir, board := bEnv(t, "s1")
	if _, se, code := bRun(t, "approve", board, "s1"); code != 0 {
		t.Fatalf("%d %s", code, se)
	}
	data, _ := os.ReadFile(board)
	senza := bytes.Replace(data, []byte(`,
 "bozza":{"modello":"alibaba/wan-3.0","risoluzione":"480p"}`), nil, 1)
	if bytes.Equal(senza, data) {
		t.Fatal("storyboard di prova non modificato")
	}
	os.WriteFile(board, senza, 0o644)
	cout, se, code := bRun(t, "check", board)
	if code != 0 {
		t.Fatalf("check %d %s", code, se)
	}
	var c struct {
		Scenes []struct {
			ID       string `json:"id"`
			Approval string `json:"approval"`
		} `json:"scenes"`
	}
	json.Unmarshal([]byte(cout), &c)
	for _, s := range c.Scenes {
		if s.ID == "s1" && s.Approval == "pronta" {
			t.Errorf("approvazione della bozza ancora pronta senza default.bozza")
		}
	}
	before, _ := os.ReadFile(filepath.Join(dir, "spot.stato.json"))
	if _, _, code := bRun(t, "approve", board, "s1"); code != 2 {
		t.Errorf("approve senza bozza né fotogramma: code=%d, atteso 2", code)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "spot.stato.json"))
	if !bytes.Equal(before, after) {
		t.Error("stato cambiato da un'approvazione rifiutata")
	}
}
