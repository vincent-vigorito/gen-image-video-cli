package config

import (
	"os"
	"strings"
)

// Get risolve una chiave in ordine: env di processo, ./credentials.env, ./.env.
func Get(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	for _, f := range []string{"credentials.env", ".env"} {
		if v := fromFile(f, key); v != "" {
			return v
		}
	}
	return ""
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
