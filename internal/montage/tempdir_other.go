//go:build !unix

package montage

// tempBase: fuori da unix nessun controllo su proprietario e permessi.
func tempBase(dir string) (string, error) { return dir, nil }
