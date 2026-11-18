package session

import (
	"slices"
	"testing"

	"github.com/actspeed/actspeed-backend/internal/model"
)

func texts(cs []Choice) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Text
	}
	return out
}

func TestSuggestLettersAndWordsCarryGestures(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	r.play("CW |")
	r.play("NE |") // t
	st := r.state()
	if st.Buffer != "t" || st.Suggest == nil {
		t.Fatalf("buffer %q, suggest %v", st.Buffer, st.Suggest)
	}
	sg := st.Suggest
	if len(sg.Letters) == 0 || sg.Letters[0].Text != "h" {
		t.Fatalf("letters after t: %v, want h first", texts(sg.Letters))
	}
	for _, l := range sg.Letters {
		if l.Sequence == "" {
			t.Fatalf("letter %q has no gesture", l.Text)
		}
	}
	if len(sg.Words) < 2 || sg.Words[0].Sequence != "N" || sg.Words[1].Sequence != "N+" {
		t.Fatalf("words %+v, want N then N+", sg.Words)
	}
	second := sg.Words[1].Text
	r.play("N+ |")
	if got := r.state().Buffer; got != second+" " {
		t.Fatalf("N+ gave %q, want %q", got, second+" ")
	}
}

func TestSuggestPhrasesFromHistory(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	r.store.recentTexts = []string{"i need water", "hello there"}
	r.play("CW |")
	st := r.state()
	if got := texts(st.Suggest.Phrases); !slices.Contains(got, "i need water") {
		t.Fatalf("empty message phrases %v, want the person's own", got)
	}
	r.play("NW NE |") // i
	sg := r.state().Suggest
	if len(sg.Phrases) == 0 || sg.Phrases[0].Text != "i need water" || sg.Phrases[0].Sequence != "E+" || sg.Phrases[0].Source != "history" {
		t.Fatalf("phrases after i: %+v", sg.Phrases)
	}
	r.play("E+ |")
	if got := r.state().Buffer; got != "i need water " {
		t.Fatalf("E+ gave %q", got)
	}
}

func TestSuggestAskClaudeAfterAPause(t *testing.T) {
	fake := &fakeAI{phrases: []string{"tell them I'm here", "tea please"}}
	r := newRig(t, model.ProfileSettings{}, fake)
	r.play("CW |")
	r.play("NE | NW |") // t e
	r.m.Wait()
	fake.mu.Lock()
	n, asked := fake.completions, fake.lastComplete.Text
	fake.mu.Unlock()
	if n == 0 || asked != "te" {
		t.Fatalf("completions %d for %q, want a call for \"te\"", n, asked)
	}
	sg := r.state().Suggest
	if len(sg.Phrases) == 0 || sg.Phrases[0].Text != "tell them I'm here" || sg.Phrases[0].Source != "ai" {
		t.Fatalf("phrases %+v", sg.Phrases)
	}
	if len(r.out.of("text_update")) < 3 {
		t.Fatal("completions were not published")
	}
	r.play("S+ |")
	if got := r.state().Buffer; got != "tea please " {
		t.Fatalf("S+ gave %q", got)
	}
}

func TestSuggestNoClaudeWhenOff(t *testing.T) {
	fake := &fakeAI{phrases: []string{"tell them"}}
	r := newRig(t, model.ProfileSettings{}, fake)
	r.store.profile.AIEnabled = false
	r.play("CW |")
	r.play("NE | NW |")
	r.m.Wait()
	if fake.completions != 0 {
		t.Fatalf("asked Claude %d times with AI off", fake.completions)
	}
}

func TestInitialsAndStablePlaces(t *testing.T) {
	if !initialsMatch("how are you", "hay") || initialsMatch("how are you", "ha y") || initialsMatch("hi", "hay") {
		t.Fatal("initialsMatch")
	}
	prev := []Choice{{Text: "a"}, {Text: "b"}}
	got := texts(keepPlaces(prev, []Choice{{Text: "b"}, {Text: "c"}, {Text: "a"}}))
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("keepPlaces: %v", got)
	}
}
