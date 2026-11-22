package speech

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gen-image-video-cli/internal/output"
)

// WriteFiles scrive in dir <base>.wav e, se ci sono i tempi, <base>.tempi.json.
// I file si creano nuovi (O_EXCL): uno che c'è già, anche come link
// simbolico, non si sovrascrive e non si segue (errore che soddisfa
// errors.Is(err, fs.ErrExist)); se il secondo non si può creare, il primo si
// toglie. base è un nome di file, senza cartelle.
func WriteFiles(dir, base string, res *Result, tf *TimingsFile) ([]output.FileInfo, error) {
	if base == "" || base == "." || base == ".." || strings.ContainsAny(base, `/\`+"\x00") || strings.HasPrefix(base, "-") {
		return nil, fmt.Errorf("nome dei file della voce non valido: %q", base)
	}
	wavPath := filepath.Join(dir, base+".wav")
	if err := writeNew(wavPath, res.Audio); err != nil {
		return nil, err
	}
	files := []output.FileInfo{{Path: wavPath, Mime: "audio/wav", Bytes: len(res.Audio)}}
	if tf == nil {
		return files, nil
	}
	data, err := MarshalTimings(*tf)
	if err == nil {
		tp := filepath.Join(dir, base+".tempi.json")
		if err = writeNew(tp, data); err == nil {
			return append(files, output.FileInfo{Path: tp, Mime: "application/json", Bytes: len(data)}), nil
		}
	}
	os.Remove(wavPath)
	return nil, err
}

// writeNew crea path nuovo e ci scrive data; su errore di scrittura lo toglie.
func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return errors.Join(fmt.Errorf("%s: scrittura non riuscita", path), err)
	}
	return nil
}
