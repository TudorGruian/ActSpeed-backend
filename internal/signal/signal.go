// Package signal turns raw joystick samples into discrete tokens
// (direction changes, holds, repeats, button clicks) and exposes the
// calibrated analog position for proportional pointer control.
//
// The package is pure: time is always passed in, so it is fully testable.
package signal

import "math"

const DefaultNeutralMs = 40

// Sample is one reading published by the device. X and Y are in -1000..1000.
type Sample struct {
	Seq uint32 `json:"seq"`
	T   int64  `json:"t"` // device clock, ms (informational)
	X   int    `json:"x"`
	Y   int    `json:"y"`
	B   int    `json:"b,omitempty"` // legacy input; sessions ignore it
}

// Direction is a quantized stick direction.
type Direction uint8

const (
	Center Direction = iota
	N
	NE
	E
	SE
	S
	SW
	W
	NW
)

var dirNames = [...]string{"C", "N", "NE", "E", "SE", "S", "SW", "W", "NW"}

func (d Direction) String() string { return dirNames[d] }

// Diagonal reports whether d is one of the four corners.
func (d Direction) Diagonal() bool { return d == NE || d == SE || d == SW || d == NW }

// Kind is the type of a Token.
type Kind uint8

const (
	KindMove      Kind = iota // stick entered a new direction
	KindHold                  // direction held for HoldMs
	KindRepeat                // emitted every RepeatMs while still held
	KindCenter                // stick returned to centre
	KindClick                 // button released before HoldMs
	KindLongPress             // button held for HoldMs
)

// Token is one discrete input event.
type Token struct {
	Kind Kind
	Dir  Direction
	T    int64 // server time, ms
}

// Name is the token's canonical text form used in pattern sequences:
// "N", "NE", "N+" (held), "B" (click), "B+" (long press).
func (t Token) Name() string {
	switch t.Kind {
	case KindMove:
		return t.Dir.String()
	case KindHold:
		return t.Dir.String() + "+"
	case KindClick:
		return "B"
	case KindLongPress:
		return "B+"
	}
	return ""
}

// Settings are the per-profile tuning parameters.
type Settings struct {
	DeadZone  int  `json:"dead_zone"` // magnitude needed to leave centre (0..1000)
	Release   int  `json:"release"`   // magnitude below which the stick is centred again
	HoldMs    int  `json:"hold_ms"`   // time before a direction/button counts as held
	RepeatMs  int  `json:"repeat_ms"` // repeat interval after a hold
	CenterX   int  `json:"center_x"`  // calibration offset
	CenterY   int  `json:"center_y"`  // calibration offset
	InvertX   bool `json:"invert_x"`
	InvertY   bool `json:"invert_y"`
	Diagonals bool `json:"diagonals"` // 8-way when true, 4-way otherwise
	// ReboundMs ignores a push in roughly the opposite direction this soon
	// after the stick was released: the spring overshooting the centre.
	ReboundMs   int `json:"rebound_ms"`
	DirectionMs int `json:"direction_ms"` // minimum directional dwell
	NeutralMs   int `json:"neutral_ms"`   // minimum time at rest to end a stroke
}

// DefaultSettings are conservative values suited to mouth use.
func DefaultSettings() Settings {
	return Settings{DeadZone: 400, Release: 250, HoldMs: 600, RepeatMs: 250, ReboundMs: 120, DirectionMs: 35, NeutralMs: DefaultNeutralMs}
}

// Normalize fills zero values with defaults and keeps the hysteresis sane.
func (s Settings) Normalize() Settings {
	d := DefaultSettings()
	if s.DeadZone <= 0 {
		s.DeadZone = d.DeadZone
	}
	s.DeadZone = min(s.DeadZone, 1000)
	s.CenterX = max(-1000, min(s.CenterX, 1000))
	s.CenterY = max(-1000, min(s.CenterY, 1000))
	if s.Release <= 0 || s.Release >= s.DeadZone {
		s.Release = s.DeadZone * 5 / 8
	}
	if s.HoldMs <= 0 {
		s.HoldMs = d.HoldMs
	}
	if s.RepeatMs <= 0 {
		s.RepeatMs = d.RepeatMs
	}
	if s.DirectionMs <= 0 {
		s.DirectionMs = d.DirectionMs
	}
	if s.NeutralMs <= 0 {
		s.NeutralMs = d.NeutralMs
	}
	if s.ReboundMs == 0 {
		s.ReboundMs = d.ReboundMs
	} else if s.ReboundMs < 0 {
		s.ReboundMs = 0 // explicitly disabled
	}
	return s
}

// Processor converts samples to tokens. It is not safe for concurrent use.
type Processor struct {
	set Settings

	dir        Direction
	dirSince   int64
	released   Direction // direction just released, for rebound suppression
	releasedAt int64
	held       bool
	lastRepeat int64

	btn      bool
	btnSince int64
	btnLong  bool

	x, y int // calibrated position
}

// NewProcessor returns a processor with normalized settings.
func NewProcessor(s Settings) *Processor { return &Processor{set: s.Normalize()} }

// SetSettings swaps the tuning parameters without losing state.
func (p *Processor) SetSettings(s Settings) { p.set = s.Normalize() }

// Settings returns the active settings.
func (p *Processor) Settings() Settings { return p.set }

// Direction returns the current quantized direction.
func (p *Processor) Direction() Direction { return p.dir }

// Position returns the stick position as -1..1 per axis with the dead zone
// removed, so 0 means "inside the dead zone" and 1 means full deflection.
// Y is positive upwards.
func (p *Processor) Position() (float64, float64) {
	fx, fy := float64(p.x), float64(p.y)
	mag := math.Hypot(fx, fy)
	dz := float64(p.set.Release)
	if mag <= dz {
		return 0, 0
	}
	scaled := math.Min((mag-dz)/(1000-dz), 1)
	return fx / mag * scaled, fy / mag * scaled
}

// Calibrate applies the profile center and axis orientation.
func (p *Processor) Calibrate(s Sample) (int, int) {
	x, y := s.X-p.set.CenterX, s.Y-p.set.CenterY
	if p.set.InvertX {
		x = -x
	}
	if p.set.InvertY {
		y = -y
	}
	return x, y
}

// Feed processes one sample received at server time now (ms).
func (p *Processor) Feed(s Sample, now int64) []Token {
	x, y := p.Calibrate(s)
	p.x, p.y = x, y

	var out []Token
	mag := math.Hypot(float64(x), float64(y))

	next := p.dir
	switch {
	case p.dir == Center && mag >= float64(p.set.DeadZone):
		next = Quantize(x, y, p.set.Diagonals)
		if p.released != Center && now-p.releasedAt < int64(p.set.ReboundMs) && opposite(next, p.released) {
			next = Center // spring overshoot, not a new push
		}
	case p.dir != Center && mag <= float64(p.set.Release):
		next = Center
	case p.dir != Center:
		// Switching between directions needs a clear move past the sector
		// boundary; quantize handles that because we only re-quantize while
		// outside the release radius.
		if mag >= float64(p.set.DeadZone) && !withinSticky(x, y, p.dir, p.set.Diagonals) {
			next = Quantize(x, y, p.set.Diagonals)
		}
	}
	if next != p.dir {
		prev := p.dir
		p.dir, p.dirSince, p.held, p.lastRepeat = next, now, false, 0
		if next == Center {
			p.released, p.releasedAt = prev, now
			out = append(out, Token{Kind: KindCenter, T: now})
		} else {
			out = append(out, Token{Kind: KindMove, Dir: next, T: now})
		}
	}

	pressed := s.B != 0
	if pressed != p.btn {
		if pressed {
			p.btn, p.btnSince, p.btnLong = true, now, false
		} else {
			if !p.btnLong {
				out = append(out, Token{Kind: KindClick, T: now})
			}
			p.btn = false
		}
	}
	return append(out, p.Tick(now)...)
}

// Tick advances time without a new sample so holds and repeats fire even
// when the device only reports changes.
func (p *Processor) Tick(now int64) []Token {
	var out []Token
	if p.dir != Center {
		if !p.held && now-p.dirSince >= int64(p.set.HoldMs) {
			p.held, p.lastRepeat = true, now
			out = append(out, Token{Kind: KindHold, Dir: p.dir, T: now})
		} else if p.held && now-p.lastRepeat >= int64(p.set.RepeatMs) {
			p.lastRepeat = now
			out = append(out, Token{Kind: KindRepeat, Dir: p.dir, T: now})
		}
	}
	if p.btn && !p.btnLong && now-p.btnSince >= int64(p.set.HoldMs) {
		p.btnLong = true
		out = append(out, Token{Kind: KindLongPress, T: now})
	}
	return out
}

// ButtonDown reports whether the button is currently pressed.
func (p *Processor) ButtonDown() bool { return p.btn }

// Idle reports whether the stick is centred and the button released.
func (p *Processor) Idle() bool { return p.dir == Center && !p.btn }

// opposite reports whether b points within 45 degrees of the reverse of a.
func opposite(a, b Direction) bool {
	if a == Center || b == Center {
		return false
	}
	diff := math.Abs(math.Mod(dirAngles[a]-dirAngles[b]+360, 360) - 180)
	return diff <= 45
}

var dirAngles = map[Direction]float64{E: 0, NE: 45, N: 90, NW: 135, W: 180, SW: 225, S: 270, SE: 315}

// stickyMargin widens the current sector so tremor near a boundary does not
// flip between two directions.
const stickyMargin = 12.0

func withinSticky(x, y int, cur Direction, diagonals bool) bool {
	deg := math.Atan2(float64(y), float64(x)) * 180 / math.Pi
	diff := math.Abs(math.Mod(deg-dirAngles[cur]+540, 360) - 180)
	half := 45.0
	if diagonals {
		half = 22.5
	}
	return diff <= half+stickyMargin
}

// Quantize returns the nearest four-way or eight-way direction.
func Quantize(x, y int, diagonals bool) Direction {
	deg := math.Atan2(float64(y), float64(x)) * 180 / math.Pi // 0 = east, 90 = north
	if deg < 0 {
		deg += 360
	}
	if diagonals {
		sector := int(math.Floor((deg+22.5)/45)) % 8
		return [...]Direction{E, NE, N, NW, W, SW, S, SE}[sector]
	}
	sector := int(math.Floor((deg+45)/90)) % 4
	return [...]Direction{E, N, W, S}[sector]
}
