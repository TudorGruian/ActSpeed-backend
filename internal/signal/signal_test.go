package signal

import (
	"strings"
	"testing"
)

func names(tokens []Token) string {
	var out []string
	for _, t := range tokens {
		if t.Kind == KindCenter {
			out = append(out, "C")
		} else if t.Kind == KindRepeat {
			out = append(out, "R")
		} else {
			out = append(out, t.Name())
		}
	}
	return strings.Join(out, " ")
}

func TestDirectionsAndHysteresis(t *testing.T) {
	p := NewProcessor(Settings{DeadZone: 400, Release: 250, HoldMs: 600, RepeatMs: 250})
	steps := []struct {
		x, y int
		now  int64
		want string
	}{
		{300, 0, 0, ""},    // inside the dead zone
		{900, 0, 10, "E"},  // clear push
		{300, 0, 20, ""},   // still above the release radius: stays E
		{100, 0, 30, "C"},  // released
		{0, -900, 40, "S"}, // straight to another direction
		{-900, 0, 50, "W"}, // sweeping across without centring
		{0, 0, 60, "C"},
	}
	for i, s := range steps {
		got := names(p.Feed(Sample{X: s.x, Y: s.y}, s.now))
		if got != s.want {
			t.Errorf("step %d: got %q, want %q", i, got, s.want)
		}
	}
}

func TestTremorNearBoundaryDoesNotFlip(t *testing.T) {
	p := NewProcessor(Settings{})
	if got := names(p.Feed(Sample{X: 900, Y: 100}, 0)); got != "E" {
		t.Fatalf("got %q", got)
	}
	// 50 degrees is past the 45 degree boundary but within the sticky margin.
	if got := names(p.Feed(Sample{X: 580, Y: 690}, 10)); got != "" {
		t.Errorf("tremor flipped direction: %q", got)
	}
	// A decisive move to north does switch.
	if got := names(p.Feed(Sample{X: 0, Y: 900}, 20)); got != "N" {
		t.Errorf("got %q, want N", got)
	}
}

func TestDiagonalsOnlyWhenEnabled(t *testing.T) {
	four := NewProcessor(Settings{})
	if got := names(four.Feed(Sample{X: 650, Y: 700}, 0)); got != "N" {
		t.Errorf("4-way: got %q, want N", got)
	}
	eight := NewProcessor(Settings{Diagonals: true})
	if got := names(eight.Feed(Sample{X: 650, Y: 700}, 0)); got != "NE" {
		t.Errorf("8-way: got %q, want NE", got)
	}
}

func TestHoldAndRepeatFireFromTick(t *testing.T) {
	p := NewProcessor(Settings{HoldMs: 600, RepeatMs: 250})
	p.Feed(Sample{X: 0, Y: 900}, 0)
	if got := names(p.Tick(500)); got != "" {
		t.Errorf("early hold: %q", got)
	}
	if got := names(p.Tick(600)); got != "N+" {
		t.Errorf("got %q, want N+", got)
	}
	if got := names(p.Tick(700)); got != "" {
		t.Errorf("early repeat: %q", got)
	}
	if got := names(p.Tick(850)); got != "R" {
		t.Errorf("got %q, want R", got)
	}
}

func TestButtonClickAndLongPress(t *testing.T) {
	p := NewProcessor(Settings{HoldMs: 600})
	p.Feed(Sample{B: 1}, 0)
	if got := names(p.Feed(Sample{B: 0}, 100)); got != "B" {
		t.Errorf("got %q, want B", got)
	}
	p.Feed(Sample{B: 1}, 1000)
	if got := names(p.Tick(1600)); got != "B+" {
		t.Errorf("got %q, want B+", got)
	}
	if got := names(p.Feed(Sample{B: 0}, 1700)); got != "" {
		t.Errorf("long press also produced a click: %q", got)
	}
}

func TestCalibrationInvertAndPosition(t *testing.T) {
	p := NewProcessor(Settings{CenterX: 100, InvertY: true, DeadZone: 400, Release: 250})
	if got := names(p.Feed(Sample{X: 100, Y: -900}, 0)); got != "N" {
		t.Errorf("got %q, want N (inverted Y)", got)
	}
	x, y := p.Position()
	if x != 0 || y < 0.8 {
		t.Errorf("position = %.2f, %.2f", x, y)
	}
	p.Feed(Sample{X: 100, Y: 0}, 10)
	if x, y := p.Position(); x != 0 || y != 0 {
		t.Errorf("centred position = %.2f, %.2f", x, y)
	}
}

func TestSpringReboundIsIgnored(t *testing.T) {
	p := NewProcessor(Settings{})
	p.Feed(Sample{X: -900}, 0)                // flick west
	p.Feed(Sample{X: 0}, 100)                 // released
	got := names(p.Feed(Sample{X: 600}, 140)) // overshoot east
	if got != "" {
		t.Errorf("rebound produced %q", got)
	}
	p.Feed(Sample{X: 0}, 160)
	// A deliberate push east after the window is a real move.
	if got := names(p.Feed(Sample{X: 900}, 500)); got != "E" {
		t.Errorf("got %q, want E", got)
	}
	// A different direction right after release is not a rebound.
	p.Feed(Sample{X: 0}, 600)
	if got := names(p.Feed(Sample{Y: 900}, 620)); got != "N" {
		t.Errorf("got %q, want N", got)
	}
}
