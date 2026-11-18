package session

import (
	"testing"

	"github.com/actspeed/actspeed-backend/internal/model"
)

// pair plays "N N" (volume up) with the given rest between the strokes, then
// rests long enough for the gesture to finish.
func (r *rig) pair(pauseMs int64) {
	v := vec["N"]
	r.sample(v[0], v[1], 0, 100)
	r.sample(0, 0, 0, pauseMs)
	r.sample(v[0], v[1], 0, 100)
	r.sample(0, 0, 0, 1500)
	r.m.Wait()
}

func learned(r *rig) []map[string]any {
	var out []map[string]any
	for _, d := range r.out.of("timing_learned") {
		out = append(out, d.(map[string]any))
	}
	return out
}

func TestTimingSlowPausesLengthenGap(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	for range 16 {
		r.pair(420)
	}
	ev := learned(r)
	if len(ev) == 0 {
		t.Fatal("no timing_learned event")
	}
	gap := ev[len(ev)-1]["gap_ms"].(int)
	if gap <= 500 || gap > maxGapMs {
		t.Fatalf("gap_ms = %d, want above 500", gap)
	}
	if r.store.learnedGap != gap {
		t.Fatalf("stored gap %d, published %d", r.store.learnedGap, gap)
	}
	if n := len(r.out.actions()); n == 0 {
		t.Fatal("no actions")
	}
}

func TestTimingFastPausesShortenGap(t *testing.T) {
	r := newRig(t, model.ProfileSettings{GapMs: 900}, nil)
	for range 16 {
		r.pair(60)
	}
	ev := learned(r)
	if len(ev) == 0 {
		t.Fatal("no timing_learned event")
	}
	if gap := ev[len(ev)-1]["gap_ms"].(int); gap >= 900 || gap < minGapMs {
		t.Fatalf("gap_ms = %d, want below 900", gap)
	}
}

func TestTimingNearMissesLengthenGap(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	// Each pause runs past the 500 ms wait, so "N N" splits into two "N".
	for range 16 {
		r.pair(750)
	}
	ev := learned(r)
	if len(ev) == 0 {
		t.Fatal("near misses did not adapt the gap")
	}
	if gap := ev[len(ev)-1]["gap_ms"].(int); gap <= 750 {
		t.Fatalf("gap_ms = %d, want above the person's pause", gap)
	}
	// Once learned, the slow pair is one gesture again.
	before := len(r.out.actions())
	r.pair(750)
	after := r.out.actions()[before:]
	if len(after) != 1 || after[0] != "system.volume_up" {
		t.Fatalf("after learning got %v, want [system.volume_up]", after)
	}
}

func TestTimingCanBeTurnedOff(t *testing.T) {
	off := false
	r := newRig(t, model.ProfileSettings{AdaptiveTiming: &off}, nil)
	for range 16 {
		r.pair(420)
	}
	if ev := learned(r); len(ev) != 0 {
		t.Fatalf("learned with adaptive_timing off: %v", ev)
	}
	if r.store.learnedGap != 0 {
		t.Fatal("stored a gap with adaptive_timing off")
	}
}
