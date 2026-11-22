package montage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkSrc(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "src.mp4")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func noPart(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".part") {
			t.Errorf("temporaneo rimasto: %s", e.Name())
		}
	}
}

// R2a: TMPDIR scrivibile da altri senza sticky: rifiuto; 1777 e 0700: ok.
func TestSicurezzaTempDirCondivisa(t *testing.T) {
	base := t.TempDir()
	for mode, wantErr := range map[os.FileMode]bool{0o777: true, 0o700: false, 0o777 | os.ModeSticky: false} {
		d := filepath.Join(base, mode.String())
		os.Mkdir(d, 0o700)
		os.Chmod(d, mode)
		t.Setenv("TMPDIR", d)
		got, err := TempDir("giv-")
		if (err != nil) != wantErr {
			t.Errorf("mode %v: err=%v", mode, err)
		}
		if err == nil {
			if fi, _ := os.Stat(got); fi.Mode().Perm() != 0o700 {
				t.Errorf("mode %v: cartella %v", mode, fi.Mode().Perm())
			}
		}
	}
}

// R2b: nome finale mai parziale; file o link già presenti restano intatti.
func TestSicurezzaPublishNomeEsistente(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "bersaglio")
	os.WriteFile(secret, []byte("ORIGINALE"), 0o644)
	os.WriteFile(filepath.Join(dir, "file.mp4"), []byte("MIO"), 0o644)
	os.Symlink(secret, filepath.Join(dir, "link.mp4"))
	for _, n := range []string{"file.mp4", "link.mp4"} {
		err := publish(context.Background(), mkSrc(t, "NUOVO"), dir, nil, filepath.Join(dir, n), false)
		if !errors.Is(err, ErrExists) {
			t.Errorf("%s: err=%v", n, err)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "ORIGINALE" {
		t.Errorf("bersaglio del link modificato: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "file.mp4")); string(b) != "MIO" {
		t.Errorf("file sostituito: %q", b)
	}
	noPart(t, dir)
	// nome libero: pubblicato completo
	out := filepath.Join(dir, "nuovo.mp4")
	if err := publish(context.Background(), mkSrc(t, "NUOVO"), dir, nil, out, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); string(b) != "NUOVO" {
		t.Errorf("contenuto %q", b)
	}
	noPart(t, dir)
	// overwrite: sostituisce
	if err := publish(context.Background(), mkSrc(t, "V2"), dir, nil, filepath.Join(dir, "file.mp4"), true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "file.mp4")); string(b) != "V2" {
		t.Errorf("overwrite %q", b)
	}
	noPart(t, dir)
}

// R2c: ctx scaduto: errore, nessun file finale, nessun temporaneo.
func TestSicurezzaPublishCtxScaduto(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := filepath.Join(dir, "out.mp4")
	if err := publish(ctx, mkSrc(t, "DATI"), dir, nil, out, false); err == nil {
		t.Fatal("atteso errore")
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("file finale presente")
	}
	noPart(t, dir)
}

// R3: root aperta su A, poi A rinominata e symlink A->B: il file va in A2.
func TestSicurezzaPublishRootAperta(t *testing.T) {
	base := t.TempDir()
	a, b := filepath.Join(base, "A"), filepath.Join(base, "B")
	os.Mkdir(a, 0o755)
	os.Mkdir(b, 0o755)
	root, err := os.OpenRoot(a)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a2 := filepath.Join(base, "A2")
	os.Rename(a, a2)
	os.Symlink(b, a)
	if err := publish(context.Background(), mkSrc(t, "DATI"), a, root, filepath.Join(a, "out.mp4"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a2, "out.mp4")); err != nil {
		t.Errorf("non in A2: %v", err)
	}
	if ents, _ := os.ReadDir(b); len(ents) != 0 {
		t.Errorf("B toccata: %v", ents)
	}
}
