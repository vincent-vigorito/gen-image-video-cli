package speech

import (
	"strings"
	"testing"
	"time"
)

func TestV0StimaCaratteriPerPrezzo(t *testing.T) {
	req, _, err := Check(Request{Text: strings.Repeat("a", 1000)})
	if err != nil {
		t.Fatal(err)
	}
	est, _ := EstimateCost(req, time.Now())
	if est == nil || est.Chars != 1000 || est.USD != 0.015 {
		t.Fatalf("stima: %+v", est)
	}
}

func TestV0DizionarioLimiti(t *testing.T) {
	if _, err := ParseDictionary([]byte(`{"Anja":"Ania","anja":"x"}`)); err == nil {
		t.Error("chiavi doppie a meno delle maiuscole: atteso errore")
	}
	d, err := ParseDictionary([]byte(`{"Swerpify":"Suerpifai"}`))
	if err != nil || d.Check() != nil {
		t.Fatalf("valido: %v", err)
	}
	if got := d.Apply("ciao swerpify!"); got != "ciao Suerpifai!" {
		t.Errorf("Apply=%q", got)
	}
	if err := (Dictionary{strings.Repeat("a", 101): "x"}).Check(); err == nil {
		t.Error("chiave >100: atteso errore")
	}
	if err := (Dictionary{"a": ""}).Check(); err == nil {
		t.Error("valore vuoto: atteso errore")
	}
}

func TestV0TestoVuotoRifiutato(t *testing.T) {
	if _, _, err := Check(Request{Text: ""}); err == nil {
		t.Error("testo vuoto: atteso errore")
	}
}
