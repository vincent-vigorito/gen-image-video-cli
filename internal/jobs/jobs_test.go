package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// F2-T2b M3: il file dei job è solo un file regolare, creato 0600.
func TestAppendCreaConPermessi0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "giv-jobs.jsonl")
	if err := Append(p, State{Provider: "xai", ID: "a", Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm=%v err=%v", fi.Mode().Perm(), err)
	}
	if st, err := Find(p, "xai", "a"); err != nil || st.ID != "a" {
		t.Fatalf("find: %+v %v", st, err)
	}
}

func TestFileNonRegolareRifiutato(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "bersaglio")
	os.WriteFile(target, []byte("{}\n"), 0o600)
	link := filepath.Join(d, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if err := Append(link, State{Provider: "xai", ID: "a"}); !errors.Is(err, ErrNotRegular) {
		t.Errorf("Append su symlink: %v", err)
	}
	if _, err := Find(link, "xai", "a"); !errors.Is(err, ErrNotRegular) {
		t.Errorf("Find su symlink: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "{}\n" {
		t.Errorf("bersaglio modificato: %q", b)
	}
	if err := Append(d, State{Provider: "xai", ID: "a"}); !errors.Is(err, ErrNotRegular) {
		t.Errorf("Append su directory: %v", err)
	}
}
