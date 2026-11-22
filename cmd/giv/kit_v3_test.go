package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Test di V-T3 (giv kit check), a scatola nera: gira il binario e guarda
// codice d'uscita e file. Niente rete, niente spesa.

func v3Run(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return so.String(), se.String(), ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), se.String(), 0
}

func v3PNG(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	im := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	for x := 0; x < 64; x++ {
		for y := 0; y < 32; y++ {
			im.Set(x, y, color.NRGBA{R: 200, G: 30, B: 30, A: uint8(x * 4)})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

const v3Kit = `{
  "marca": {
    "colori": {"primario": "#0F3D33", "testo": "#FFFFFF"},
    "logo": {"file": "logo.png", "posizione": "alto-destra", "larghezza": 0.12}
  },
  "personaggi": {
    "anja": {"riferimenti": ["anja/regia.png"], "descrizione": "Anja, mascotte illustrata", "tutela": "illustrazione"}
  },
  "pronuncia": {"Swerpify": "suèrpifai"}
}`

// v3Dir prepara una cartella con kit/ valido (logo e riferimento); ritorna la cartella del kit.
func v3Dir(t *testing.T, kit string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "kit")
	v3PNG(t, filepath.Join(d, "logo.png"))
	v3PNG(t, filepath.Join(d, "anja", "regia.png"))
	if err := os.WriteFile(filepath.Join(d, "kit.json"), []byte(kit), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func v3Snapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			out = append(out, p+"|"+fi.ModTime().String()+"|"+fi.Mode().String())
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func TestV3KitValidoPassa(t *testing.T) {
	d := v3Dir(t, v3Kit)
	before := v3Snapshot(t, d)
	so, se, code := v3Run(t, t.TempDir(), "kit", "check", filepath.Join(d, "kit.json"))
	if code != 0 {
		t.Fatalf("codice %d\n%s\n%s", code, so, se)
	}
	if !strings.Contains(so+se, "logo.png") {
		t.Errorf("l'elenco non cita il logo:\n%s\n%s", so, se)
	}
	// nessuna scrittura dentro il kit
	after := v3Snapshot(t, d)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("il kit e' cambiato:\nprima %v\ndopo %v", before, after)
	}
}

func TestV3KitRifiuti(t *testing.T) {
	cases := []struct {
		name  string
		kit   string
		setup func(t *testing.T, d string)
	}{
		{"campo sconosciuto", strings.Replace(v3Kit, `"pronuncia"`, `"stranio": 1, "pronuncia"`, 1), nil},
		{"campo sconosciuto nel logo", strings.Replace(v3Kit, `"larghezza": 0.12`, `"larghezza": 0.12, "x": 1`, 1), nil},
		{"colore non valido", strings.Replace(v3Kit, `#0F3D33`, `verdino`, 1), nil},
		{"colore esadecimale corto", strings.Replace(v3Kit, `#0F3D33`, `#12`, 1), nil},
		{"json rotto", `{"marca": `, nil},
		{"file mancante", strings.Replace(v3Kit, `logo.png`, `non-c-e.png`, 1), nil},
		{"riferimento mancante", strings.Replace(v3Kit, `anja/regia.png`, `anja/no.png`, 1), nil},
		{"tipo sbagliato: cartella al posto del file", v3Kit, func(t *testing.T, d string) {
			os.Remove(filepath.Join(d, "logo.png"))
			os.Mkdir(filepath.Join(d, "logo.png"), 0o755)
		}},
		{"tipo sbagliato: file non immagine", v3Kit, func(t *testing.T, d string) {
			os.WriteFile(filepath.Join(d, "logo.png"), []byte("non sono un'immagine"), 0o644)
		}},
		{"percorso assoluto", strings.Replace(v3Kit, `"logo.png"`, `"/etc/hostname"`, 1), nil},
		{"percorso con ..", strings.Replace(v3Kit, `"logo.png"`, `"../fuori.png"`, 1), func(t *testing.T, d string) {
			v3PNG(t, filepath.Join(filepath.Dir(d), "fuori.png"))
		}},
		{"link a file fuori dal kit", v3Kit, func(t *testing.T, d string) {
			out := filepath.Join(filepath.Dir(d), "esterno.png")
			v3PNG(t, out)
			os.Remove(filepath.Join(d, "logo.png"))
			if err := os.Symlink(out, filepath.Join(d, "logo.png")); err != nil {
				t.Skip(err)
			}
		}},
		{"link a cartella fuori dal kit", v3Kit, func(t *testing.T, d string) {
			out := filepath.Join(filepath.Dir(d), "esterna")
			v3PNG(t, filepath.Join(out, "regia.png"))
			os.RemoveAll(filepath.Join(d, "anja"))
			if err := os.Symlink(out, filepath.Join(d, "anja")); err != nil {
				t.Skip(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := v3Dir(t, c.kit)
			if c.setup != nil {
				c.setup(t, d)
			}
			so, se, code := v3Run(t, t.TempDir(), "kit", "check", filepath.Join(d, "kit.json"))
			if code != 2 {
				t.Fatalf("codice %d, atteso 2\n%s\n%s", code, so, se)
			}
		})
	}
}

func TestV3KitSenzaArgomentiOFileInesistente(t *testing.T) {
	if _, _, code := v3Run(t, t.TempDir(), "kit", "check"); code != 2 {
		t.Errorf("senza file: codice %d, atteso 2", code)
	}
	if _, _, code := v3Run(t, t.TempDir(), "kit", "check", filepath.Join(t.TempDir(), "no.json")); code != 2 {
		t.Errorf("kit inesistente: codice %d, atteso 2", code)
	}
}

// Un file del kit che comincia con '-' e' un nome come gli altri: non diventa un'opzione.
func TestV3NomeConTrattino(t *testing.T) {
	d := v3Dir(t, strings.Replace(v3Kit, `"logo.png"`, `"-logo.png"`, 1))
	v3PNG(t, filepath.Join(d, "-logo.png"))
	so, se, code := v3Run(t, t.TempDir(), "kit", "check", filepath.Join(d, "kit.json"))
	if code != 0 || !strings.Contains(so+se, "-logo.png") {
		t.Fatalf("codice %d\n%s\n%s", code, so, se)
	}
}

// un JSON che non e' un kit (nessuno dei campi noti): codice 2 con messaggio chiaro.
func TestV3NonEUnKit(t *testing.T) {
	d := v3Dir(t, `{"stranio": 1}`)
	_, se, code := v3Run(t, t.TempDir(), "kit", "check", filepath.Join(d, "kit.json"))
	if code != 2 || !strings.Contains(se, "non è un kit") {
		t.Errorf("code=%d\n%s", code, se)
	}
}
