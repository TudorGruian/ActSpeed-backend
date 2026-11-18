package gesture

import (
	"math"
	"math/bits"

	"github.com/actspeed/actspeed-backend/internal/signal"
)

const (
	Clockwise        = "CW"
	Counterclockwise = "CCW"
)

// Stroke follows the analog path from deflection until a settled return to
// neutral. Samples in the outer zone contribute angles and directional dwell.
type Stroke struct {
	active, releasing    bool
	start, release, last int64
	candidate            signal.Direction
	dwell                int64
	directions           []signal.Direction
	holdFor              int64

	hasAngle, broken     bool
	angle, sweep, travel float64
	sectors              uint8
}

func (s *Stroke) Active() bool { return s.active }

func (s *Stroke) Reset() { *s = Stroke{} }

// Feed uses calibrated coordinates (positive Y points up). Only time spent
// in the core of a sector counts towards a directional visit.
func (s *Stroke) Feed(x, y int, now int64, set signal.Settings) {
	radius := math.Hypot(float64(x), float64(y))
	if !s.active {
		if radius < float64(set.DeadZone) {
			return
		}
		s.active, s.start, s.last = true, now, now
	}
	s.credit(now, set.DirectionMs)
	if radius <= float64(set.Release) {
		if !s.releasing {
			s.releasing, s.release = true, now
		}
		s.candidate, s.dwell = signal.Center, 0
		return
	}
	s.releasing = false
	if radius < float64(set.DeadZone) {
		s.candidate, s.dwell = signal.Center, 0
		return
	}
	angle := math.Atan2(float64(y), float64(x)) * 180 / math.Pi
	sector := int(math.Floor((angle+382.5)/45)) % 8
	s.sectors |= 1 << sector
	if s.hasAngle {
		delta := math.Mod(angle-s.angle+540, 360) - 180
		if math.Abs(delta) > 85 {
			s.broken = true // missing arc or a straight jump across the stick
		}
		s.sweep += delta
		s.travel += math.Abs(delta)
	}
	s.angle, s.hasAngle = angle, true

	width := 90.0
	if set.Diagonals {
		width = 45
	}
	offset := math.Abs(angle - math.Round(angle/width)*width)
	next := signal.Center
	if offset <= width/2-4 {
		next = signal.Quantize(x, y, set.Diagonals)
	}
	if next != s.candidate {
		s.candidate, s.dwell = next, 0
	}
}

func (s *Stroke) credit(now int64, directionMs int) {
	elapsed := now - s.last
	s.last = now
	if s.candidate == signal.Center || elapsed <= 0 {
		return
	}
	s.dwell += elapsed
	if s.dwell < int64(directionMs) {
		return
	}
	if n := len(s.directions); n == 0 || s.directions[n-1] != s.candidate {
		s.directions = append(s.directions, s.candidate)
	}
	s.holdFor = max(s.holdFor, s.dwell)
}

// Finish emits a single circle or a directional sequence. A circle requires a
// near-complete, mostly one-way sweep through at least seven angular sectors.
func (s *Stroke) Finish(now int64, set signal.Settings) (Gesture, bool) {
	if !s.releasing || now-s.release < int64(set.NeutralMs) {
		return Gesture{}, false
	}
	g := Gesture{Start: s.start, End: s.release}
	switch {
	case !s.broken && bits.OnesCount8(s.sectors) >= 7 &&
		math.Abs(s.sweep) >= 300 && math.Abs(s.sweep) >= .8*s.travel:
		name := Counterclockwise
		if s.sweep < 0 {
			name = Clockwise
		}
		g.Tokens = []string{name}
	case bits.OnesCount8(s.sectors) >= 7 && s.travel >= 270:
		// An incomplete or inconsistent loop should not spell a letter.
	case len(s.directions) > 0:
		for _, dir := range s.directions {
			g.Tokens = append(g.Tokens, dir.String())
		}
		if len(g.Tokens) == 1 && s.holdFor >= int64(set.HoldMs) {
			g.Tokens[0] += "+"
		}
	}
	s.Reset()
	return g, true
}
