package resolver

import (
	"testing"

	"github.com/actspeed/actspeed-backend/internal/model"
	"github.com/actspeed/actspeed-backend/internal/seed"
)

func p(profile *string, mode, seq, value string, enabled bool) model.Pattern {
	return model.Pattern{ProfileID: profile, Mode: mode, Sequence: seq, OutputKind: model.OutAction, OutputValue: value, Enabled: enabled}
}

func TestMatchPrecedence(t *testing.T) {
	ana, bob := "ana", "bob"
	lib := NewLibrary([]model.Pattern{
		p(nil, "any", "N", "global-any", true),
		p(nil, "nav", "N", "global-nav", true),
		p(nil, "nav.gesture", "N", "global-specific", true),
		p(&ana, "nav", "N", "ana-nav", true),
		p(nil, "nav.gesture", "S", "disabled", false),
		p(nil, "text", "E", "text-only", true),
	})

	cases := []struct {
		profile *string
		context string
		seq     string
		want    string
		found   bool
	}{
		{nil, "nav.gesture", "N", "global-specific", true}, // most specific mode wins
		{nil, "nav.pointer", "N", "global-nav", true},      // falls back to the broader mode
		{nil, "text.grid", "N", "global-any", true},        // then to "any"
		{&ana, "nav.gesture", "N", "ana-nav", true},        // a personal pattern beats any global one
		{&bob, "nav.gesture", "N", "global-specific", true},
		{nil, "nav.gesture", "S", "", false},   // disabled
		{nil, "nav.gesture", "E", "", false},   // text pattern does not leak into nav
		{nil, "nav.gesture", "N N", "", false}, // exact sequence only
	}
	for _, c := range cases {
		got, ok := lib.Match(c.profile, c.context, c.seq)
		if ok != c.found || got.OutputValue != c.want {
			t.Errorf("Match(%v, %s, %q) = %q, %v; want %q, %v", c.profile, c.context, c.seq, got.OutputValue, ok, c.want, c.found)
		}
	}
}

func TestSeedPatternsAreUnambiguous(t *testing.T) {
	seen := map[string]string{}
	letters := map[string]bool{}
	for _, pat := range seed.Patterns() {
		key := pat.Mode + "|" + pat.Sequence
		if prev, dup := seen[key]; dup {
			t.Errorf("%s maps to both %s and %s", key, prev, pat.OutputValue)
		}
		seen[key] = pat.OutputValue
		if pat.Mode == "text.corners" && pat.OutputKind == model.OutChar {
			letters[pat.OutputValue] = true
		}
	}
	if len(letters) != 26 {
		t.Errorf("corner alphabet covers %d letters, want 26", len(letters))
	}
	actions := map[string]bool{}
	for _, a := range seed.Actions() {
		actions[a.Key] = true
	}
	for _, pat := range seed.Patterns() {
		if pat.OutputKind == model.OutAction && !actions[pat.OutputValue] {
			t.Errorf("pattern %q points at unknown action %s", pat.Sequence, pat.OutputValue)
		}
	}
}
