//go:build unix

package montage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// tempBase risolve i link della cartella temporanea dir e la restituisce solo
// se lei e ogni cartella sopra sono dell'utente o di root e, se scrivibili da
// gruppo o altri, hanno lo sticky bit: nessun altro utente può rinominarle o
// sostituirle. Le cartelle private si creano nel percorso risolto.
func tempBase(dir string) (string, error) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if real, err = filepath.Abs(real); err != nil {
		return "", err
	}
	uid := uint32(os.Getuid())
	for d := real; ; {
		fi, err := os.Lstat(d)
		if err != nil {
			return "", err
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("%s non è una cartella", d)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return "", errors.New("proprietario non leggibile")
		}
		if st.Uid != uid && st.Uid != 0 {
			return "", fmt.Errorf("%s è di un altro utente", d)
		}
		if fi.Mode().Perm()&0o022 != 0 && fi.Mode()&fs.ModeSticky == 0 {
			return "", fmt.Errorf("%s è scrivibile da altri utenti senza sticky bit", d)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return real, nil
		}
		d = parent
	}
}
