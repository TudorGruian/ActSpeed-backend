// Package resolver holds the in-memory pattern library and matches
// gestures against it. Profile-scoped patterns win over global ones, and a
// more specific mode ("text.corners") wins over a broader one ("text", "any").
package resolver

import (
	"sort"
	"strings"
	"sync"

	"github.com/actspeed/actspeed-backend/internal/model"
)

// Library is a concurrency-safe snapshot of all enabled patterns.
type Library struct {
	mu       sync.RWMutex
	patterns []model.Pattern
}

// NewLibrary returns a library holding the given patterns.
func NewLibrary(p []model.Pattern) *Library {
	l := &Library{}
	l.Set(p)
	return l
}

// Set replaces the snapshot.
func (l *Library) Set(p []model.Pattern) {
	enabled := make([]model.Pattern, 0, len(p))
	for _, x := range p {
		if x.Enabled {
			enabled = append(enabled, x)
		}
	}
	l.mu.Lock()
	l.patterns = enabled
	l.mu.Unlock()
}

func specificity(mode string) int {
	if mode == "any" {
		return 0
	}
	n := 1
	for _, c := range mode {
		if c == '.' {
			n++
		}
	}
	return n
}

// Match finds the best pattern for a gesture sequence in a context.
func (l *Library) Match(profileID *string, context, sequence string) (model.Pattern, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var best model.Pattern
	bestScore := -1
	for _, p := range l.patterns {
		if p.Sequence != sequence || !model.ContextMatches(p.Mode, context) {
			continue
		}
		score := specificity(p.Mode)
		if p.ProfileID != nil {
			if profileID == nil || *p.ProfileID != *profileID {
				continue
			}
			score += 10
		}
		if score > bestScore {
			best, bestScore = p, score
		}
	}
	return best, bestScore >= 0
}

// ForContext lists the patterns visible to a profile in a context. Used to
// build the vocabulary shown to the AI interpreter.
func (l *Library) ForContext(profileID *string, context string) []model.Pattern {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []model.Pattern
	for _, p := range l.patterns {
		if !model.ContextMatches(p.Mode, context) {
			continue
		}
		if p.ProfileID != nil && (profileID == nil || *p.ProfileID != *profileID) {
			continue
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out
}

// HasContinuation reports whether this sequence can still become a longer pattern.
func (l *Library) HasContinuation(profileID *string, context, sequence string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	prefix := sequence + " "
	for _, p := range l.patterns {
		if strings.HasPrefix(p.Sequence, prefix) && model.ContextMatches(p.Mode, context) &&
			(p.ProfileID == nil || (profileID != nil && *p.ProfileID == *profileID)) {
			return true
		}
	}
	return false
}
