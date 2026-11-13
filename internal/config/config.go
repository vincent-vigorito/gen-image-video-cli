package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Get risolve una chiave in ordine: env di processo, ./credentials.env, ./.env,
// ~/.config/giv/credentials.env.
func Get(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	for _, f := range files() {
		if v := fromFile(f, key); v != "" {
			return v
		}
	}
	return ""
}

// files elenca i file di configurazione in ordine di precedenza: quelli della
// directory corrente, poi quello dell'utente (giv usabile da qualsiasi cwd).
func files() []string {
	fs := []string{"credentials.env", ".env"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		fs = append(fs, filepath.Join(home, ".config", "giv", "credentials.env"))
	}
	return fs
}

func fromFile(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}
