package session

import (
	"context"
	"slices"

	"github.com/actspeed/actspeed-backend/internal/gesture"
)

// Learning gap_ms: how long the engine waits for the next stroke of a
// compound gesture such as "N S". Too short splits the person's gestures;
// too long makes every single flick feel slow. The engine watches the
// pauses the person actually makes between strokes of one gesture, and the
// near misses (a gesture split because the pause ran over, when the two
// halves together were a stored pattern), and sets the wait just above
// what this person needs.
const (
	learnWindow = 40   // pauses remembered
	learnMin    = 12   // pauses before the first adjustment
	learnEvery  = 8    // pauses between adjustments
	minGapMs    = 300  // never faster than this
	maxGapMs    = 1200 // never slower than this
	gapStepMs   = 50   // ignore smaller changes
)

type timingLearner struct {
	gaps    []int64
	fresh   int
	lastSeq string // the last gesture handed on, for near misses
	lastEnd int64
	lastCtx string
}

// noteGap records a pause between two strokes of one gesture.
func (s *Session) noteGap(ctx context.Context, ms int64) {
	if !s.set.LearnsTiming() || ms < 0 || s.context() == "text.corners" {
		return
	}
	t := &s.timing
	t.gaps = append(t.gaps, ms)
	if len(t.gaps) > learnWindow {
		t.gaps = t.gaps[len(t.gaps)-learnWindow:]
	}
	t.fresh++
	if len(t.gaps) >= learnMin && t.fresh >= learnEvery {
		t.fresh = 0
		s.adaptGap(ctx)
	}
}

// noteNearMiss counts a pause that split a stored gesture in two.
func (s *Session) noteNearMiss(ctx context.Context, g gesture.Gesture) {
	t := &s.timing
	if t.lastSeq == "" || t.lastCtx != s.context() || g.Circular() {
		return
	}
	pause := g.Start - t.lastEnd
	if pause <= 0 || pause > 2*int64(maxGapMs) {
		return
	}
	if _, ok := s.m.d.Library.Match(s.profileID(), s.context(), t.lastSeq+" "+g.Sequence()); ok {
		t.lastSeq = ""
		s.noteGap(ctx, pause)
	}
}

// adaptGap sets gap_ms a little above the person's slow pauses (90th
// percentile), and stores it in their profile.
func (s *Session) adaptGap(ctx context.Context) {
	sorted := slices.Clone(s.timing.gaps)
	slices.Sort(sorted)
	p90 := sorted[(len(sorted)*9)/10]
	target := int(p90*14/10) + 60
	target = min(maxGapMs, max(minGapMs, (target+5)/10*10))
	cur := s.set.GapMs
	if target-cur < gapStepMs && cur-target < gapStepMs {
		return
	}
	s.set.GapMs = target
	s.publish(ChanOutput, "timing_learned", map[string]any{"gap_ms": target, "previous_ms": cur, "samples": len(sorted)})
	if pid := s.profileID(); pid != nil {
		id := *pid
		s.m.async(func(ctx context.Context) {
			if err := s.m.d.Store.SetProfileGap(ctx, id, target); err != nil {
				s.m.d.Log.Warn("save learned timing failed", "err", err)
			}
		})
	}
}
