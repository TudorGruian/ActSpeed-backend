package predict

import (
	"strings"
	"testing"
)

func TestPrefixCompletion(t *testing.T) {
	p := New("en")
	got := p.Predict("i need he", 3)
	if len(got) == 0 || got[0] != "help" {
		t.Errorf("got %v, want help first", got)
	}
	for _, w := range got {
		if !strings.HasPrefix(w, "he") || w == "he" {
			t.Errorf("bad completion %q", w)
		}
	}
	if got := p.Predict("", 3); got != nil {
		t.Errorf("empty buffer predicted %v", got)
	}
}

func TestLearningPersonalWordsAndNextWord(t *testing.T) {
	p := New("en")
	if got := p.Predict("call mih", 3); len(got) != 0 {
		t.Fatalf("unexpected base completions %v", got)
	}
	p.Learn("Call Mihaela please")
	p.Learn("call Mihaela now")
	if got := p.Predict("call mi", 3); len(got) == 0 || got[0] != "mihaela" {
		t.Errorf("got %v, want mihaela first", got)
	}
	// Trailing space: predict the next word from what followed before.
	if got := p.Predict("call ", 3); len(got) == 0 || got[0] != "mihaela" {
		t.Errorf("next word = %v, want mihaela first", got)
	}
}

func TestUnknownLanguageStartsEmptyButLearns(t *testing.T) {
	p := New("de")
	if got := p.Predict("mu", 3); len(got) != 0 {
		t.Errorf("got %v", got)
	}
	p.Learn("mulțumesc")
	if got := p.Predict("mu", 3); len(got) != 1 || got[0] != "mulțumesc" {
		t.Errorf("got %v", got)
	}
}

func TestComplete(t *testing.T) {
	cases := [][3]string{
		{"i need he", "help", "i need help "},
		{"he", "hello", "hello "},
		{"call ", "mihaela", "call mihaela "},
		{"", "yes", "yes "},
	}
	for _, c := range cases {
		if got := Complete(c[0], c[1]); got != c[2] {
			t.Errorf("Complete(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestNextLetters(t *testing.T) {
	p := New("en")
	if got := p.NextLetters("th", 3); len(got) == 0 || got[0] != "e" {
		t.Fatalf("after th: %v, want e first", got)
	}
	p.Learn("i need water")
	p.Learn("i need water")
	if got := p.NextLetters("i need w", 1); len(got) != 1 || got[0] != "a" {
		t.Fatalf("after 'i need w': %v, want a", got)
	}
	if got := p.NextLetters("zzzzq", 3); len(got) != 0 {
		t.Fatalf("unknown prefix: %v, want none", got)
	}
}

func TestRomanianWithoutDiacritics(t *testing.T) {
	p := New("ro")
	if got := p.Predict("multum", 3); len(got) == 0 || got[0] != "mulțumesc" {
		t.Errorf("multum -> %v, want mulțumesc first", got)
	}
	if got := p.Predict("mi-e sa", 3); !contains(got, "să") {
		t.Errorf("sa -> %v, want să offered", got)
	}
	for _, l := range p.NextLetters("mult", 4) {
		if l == "ț" || l == "ă" {
			t.Errorf("next letters %q should be plain letters", l)
		}
	}
	if got := New("ro-RO").Predict("aju", 1); len(got) == 0 || !strings.HasPrefix(got[0], "ajut") {
		t.Errorf("ro-RO aju -> %v, want ajutor", got)
	}
}

func contains(list []string, w string) bool {
	for _, x := range list {
		if x == w {
			return true
		}
	}
	return false
}
