package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// CLI-988: giv voice --voices --provider gemini, elenco su più pagine.

type voicesOut struct {
	Provider string `json:"provider"`
	Voices   []struct {
		ID string `json:"voice_id"`
	} `json:"voices"`
	Warnings []string `json:"warnings"`
}

func listVoicesRun(t *testing.T, args ...string) (voicesOut, string, int) {
	t.Helper()
	out, se, code := voiceRun(t, append([]string{"--voices"}, args...)...)
	var v voicesOut
	if code == 0 {
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("stdout non JSON: %v\n%.300s", err, out)
		}
	}
	return v, se, code
}

func has(v voicesOut, id string) bool {
	for _, x := range v.Voices {
		if x.ID == id {
			return true
		}
	}
	return false
}

func TestVoicesGeminiPiuPagine(t *testing.T) {
	var tokens []string
	var key string
	hits, _ := geminiEnv(t, func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("x-goog-api-key")
		tok := r.URL.Query().Get("page_token")
		tokens = append(tokens, tok)
		if tok == "" {
			fmt.Fprint(w, `{"voices":[{"id":"voices/ZetaUno","display_name":"Z1"}],"next_page_token":"p2"}`)
		} else {
			fmt.Fprint(w, `{"voices":[{"id":"voices/ZetaDue","display_name":"Z2"}]}`)
		}
	})
	v, se, code := listVoicesRun(t, "--provider", "gemini")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if *hits != 2 || len(tokens) != 2 || tokens[1] != "p2" {
		t.Errorf("hits=%d tokens=%v", *hits, tokens)
	}
	if !has(v, "ZetaUno") || !has(v, "ZetaDue") || !has(v, "Kore") {
		t.Errorf("voci mancanti: %v", v.Voices)
	}
	if len(v.Warnings) != 0 || strings.Contains(se, "parziale") {
		t.Errorf("avviso inatteso: %v %s", v.Warnings, se)
	}
	if key != "fake-gem-key" {
		t.Errorf("chiave all'host API: %q", key)
	}
}

func TestVoicesGeminiOltreIlMassimoDiPagine(t *testing.T) {
	var n int32
	hits, _ := geminiEnv(t, func(w http.ResponseWriter, r *http.Request) {
		i := atomic.AddInt32(&n, 1)
		fmt.Fprintf(w, `{"voices":[{"id":"voices/V%d"}],"next_page_token":"t%d"}`, i, i)
	})
	v, se, code := listVoicesRun(t, "--provider", "gemini")
	if code != 0 {
		t.Fatalf("code=%d %s", code, se)
	}
	if *hits > 10 || *hits < 2 {
		t.Errorf("richieste=%d (attese al più 10)", *hits)
	}
	if len(v.Warnings) == 0 || !strings.Contains(se, "parziale") {
		t.Errorf("manca l'avviso di elenco parziale: %v / %s", v.Warnings, se)
	}
}

func TestVoicesGeminiOltreIlMassimoDiVoci(t *testing.T) {
	_, _ = geminiEnv(t, func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString(`{"voices":[`)
		for i := 0; i < 10500; i++ {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, `{"id":"voices/W%d"}`, i)
		}
		b.WriteString(`]}`)
		fmt.Fprint(w, b.String())
	})
	v, se, code := listVoicesRun(t, "--provider", "gemini")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if len(v.Voices) > 10000 || len(v.Warnings) == 0 || !strings.Contains(se, "parziale") {
		t.Errorf("voci=%d warnings=%v", len(v.Voices), v.Warnings)
	}
}

func TestVoicesGeminiPaginaSuAltroHostNonSeguita(t *testing.T) {
	var other int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&other, 1) }))
	defer srv2.Close()
	hits, _ := geminiEnv(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"voices":[{"id":"voices/Uno"}],"next_page_token":%q}`, srv2.URL+"/voices?page=2")
	})
	_, _, code := listVoicesRun(t, "--provider", "gemini")
	if code != 0 && code != 5 {
		t.Logf("code=%d", code)
	}
	if other != 0 {
		t.Fatalf("seguito un indirizzo su altro host: %d richieste", other)
	}
	if *hits > 10 {
		t.Errorf("richieste=%d", *hits)
	}
}

func TestVoicesXAIInvariato(t *testing.T) {
	hits, _ := voiceEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tts/voices" {
			t.Errorf("percorso %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"voices":[{"voice_id":"eve","name":"Eve","language":"en"},{"voice_id":"ara","name":"Ara","language":"en"}]}`)
	})
	v, se, code := listVoicesRun(t, "--provider", "xai")
	if code != 0 || *hits != 1 || len(v.Voices) != 2 || len(v.Warnings) != 0 {
		t.Errorf("code=%d hits=%d voci=%d warn=%v %s", code, *hits, len(v.Voices), v.Warnings, se)
	}
}
