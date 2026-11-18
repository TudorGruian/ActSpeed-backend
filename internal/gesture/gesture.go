// Package gesture recognizes joystick strokes and groups compound gestures.
package gesture

import "strings"

// DefaultGapMs is the maximum neutral interval between strokes of a gesture.
const DefaultGapMs = 500

// Gesture is a completed sequence of token names, e.g. ["NW", "NE"] or ["CW"].
type Gesture struct {
	Tokens []string `json:"tokens"`
	Start  int64    `json:"start"`
	End    int64    `json:"end"`
}

// Sequence is the canonical space-separated form stored in patterns.
func (g Gesture) Sequence() string { return strings.Join(g.Tokens, " ") }

// Circular reports whether the gesture starts with a circle.
func (g Gesture) Circular() bool {
	return len(g.Tokens) > 0 && (g.Tokens[0] == Clockwise || g.Tokens[0] == Counterclockwise)
}

// ParseSequence splits and normalizes a stored sequence.
func ParseSequence(s string) []string { return strings.Fields(strings.ToUpper(s)) }

// NormalizeSequence returns the canonical form of a user-supplied sequence.
func NormalizeSequence(s string) string { return strings.Join(ParseSequence(s), " ") }
