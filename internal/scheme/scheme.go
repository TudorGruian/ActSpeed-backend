// Package scheme implements the text input schemes. A scheme gets first
// refusal on every token; tokens it does not consume flow on to the gesture
// segmenter and the pattern library.
package scheme

import (
	"github.com/actspeed/actspeed-backend/internal/model"
	"github.com/actspeed/actspeed-backend/internal/signal"
)

// Output is something a scheme produced: a character, a phrase or a control.
type Output struct {
	Kind  string // model.OutChar | model.OutPhrase | model.OutControl
	Value string
	Param int // e.g. prediction index for text.accept_prediction
}

// Scheme is a text input method.
type Scheme interface {
	Name() string
	// Token handles one token; consumed tokens are hidden from the segmenter.
	Token(t signal.Token) (out []Output, consumed bool)
	// Tick lets time-based schemes (dwell select) fire.
	Tick(now int64) []Output
	// SetPredictions passes the current word predictions (shown by the grid).
	SetPredictions(p []string)
	// State is published to clients so they can render the scheme.
	State() any
	// NormalizeGesture filters a finished gesture before pattern matching.
	NormalizeGesture(tokens []string) []string
}

// Options configure a scheme from the profile.
type Options struct {
	Phrases       []string
	DwellSelectMs int
}

// New builds the scheme named in the profile settings.
func New(name string, o Options) Scheme {
	switch name {
	case model.SchemeCorners:
		return &Corners{}
	case model.SchemeGrid:
		return NewGrid(o)
	default:
		return &TwoDir{}
	}
}

// ---- Corners ---------------------------------------------------------

// Corners spells letters as paths through the four corners. The letters
// themselves live in the pattern library (mode "text.corners"), so the
// scheme only cleans up the gesture: passing through N/E/S/W on the way
// between corners must not count.
type Corners struct{}

func (*Corners) Name() string                           { return model.SchemeCorners }
func (*Corners) Token(signal.Token) ([]Output, bool)    { return nil, false }
func (*Corners) Tick(int64) []Output                    { return nil }
func (*Corners) SetPredictions([]string)                {}
func (*Corners) State() any                             { return nil }
func (*Corners) NormalizeGesture(tok []string) []string { return KeepCorners(tok) }

// KeepCorners keeps only corner tokens when the gesture contains any, and
// collapses immediate repeats. Gestures without corners are left alone so
// plain flicks stay available as controls.
func KeepCorners(tok []string) []string {
	var out []string
	for _, t := range tok {
		base := t
		if len(base) == 3 && base[2] == '+' {
			base = base[:2]
		}
		switch base {
		case "NE", "NW", "SE", "SW":
			if len(out) == 0 || out[len(out)-1] != base {
				out = append(out, base)
			}
		}
	}
	if len(out) == 0 {
		return tok
	}
	return out
}

// ---- Two-direction (Morse-style) ------------------------------------

// TwoDir: left = dot, right = dash, up = end of letter, down = cancel the
// pending letter or delete one character. With no letter in progress,
// holding up accepts the first word prediction and holding down clears the
// message. Space and speak are circular gestures in the pattern library.
type TwoDir struct {
	pending string
}

func (*TwoDir) Name() string                         { return model.SchemeTwoDir }
func (*TwoDir) Tick(int64) []Output                  { return nil }
func (*TwoDir) SetPredictions([]string)              {}
func (t *TwoDir) State() any                         { return map[string]any{"pending": t.pending} }
func (*TwoDir) NormalizeGesture(x []string) []string { return x }

func (t *TwoDir) Token(tok signal.Token) ([]Output, bool) {
	if tok.Kind != signal.KindMove {
		return nil, false
	}
	switch tok.Dir {
	case signal.W:
		t.pending += "."
	case signal.E:
		t.pending += "-"
	case signal.N:
		code := t.pending
		t.pending = ""
		if ch, ok := morse[code]; ok {
			return []Output{{Kind: model.OutChar, Value: ch}}, true
		}
	case signal.S:
		if t.pending != "" {
			t.pending = ""
		} else {
			return []Output{{Kind: model.OutControl, Value: model.CtlTextBackspace}}, true
		}
	}
	return nil, true
}

// Held handles a deliberate hold without also performing its short action.
func (t *TwoDir) Held(dir signal.Direction) []Output {
	if t.pending == "" {
		switch dir {
		case signal.N:
			return []Output{{Kind: model.OutControl, Value: model.CtlTextAcceptPredic}}
		case signal.S:
			return []Output{{Kind: model.OutControl, Value: model.CtlTextClear}}
		}
	}
	out, _ := t.Token(signal.Token{Kind: signal.KindMove, Dir: dir})
	return out
}

var morse = map[string]string{
	".-": "a", "-...": "b", "-.-.": "c", "-..": "d", ".": "e", "..-.": "f",
	"--.": "g", "....": "h", "..": "i", ".---": "j", "-.-": "k", ".-..": "l",
	"--": "m", "-.": "n", "---": "o", ".--.": "p", "--.-": "q", ".-.": "r",
	"...": "s", "-": "t", "..-": "u", "...-": "v", ".--": "w", "-..-": "x",
	"-.--": "y", "--..": "z",
	".----": "1", "..---": "2", "...--": "3", "....-": "4", ".....": "5",
	"-....": "6", "--...": "7", "---..": "8", "----.": "9", "-----": "0",
	".-.-.-": ".", "--..--": ",", "..--..": "?", "-.-.--": "!", ".----.": "'",
}

// MorseFor returns the dot/dash code for a character (used by the simulator).
func MorseFor(ch string) (string, bool) {
	for code, c := range morse {
		if c == ch {
			return code, true
		}
	}
	return "", false
}
