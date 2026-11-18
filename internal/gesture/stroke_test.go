package gesture

import (
	"math"
	"testing"

	"github.com/actspeed/actspeed-backend/internal/signal"
)

func TestCircleDirectionAndRejection(t *testing.T) {
	for _, test := range []struct {
		name                 string
		step, count, reverse int
		want                 string
	}{
		{"clockwise wraps from north", -15, 24, 0, Clockwise},
		{"counterclockwise wraps from north", 15, 24, 0, Counterclockwise},
		{"incomplete loop", 15, 18, 0, ""},
		{"backtracking loop", 15, 24, 12, ""},
		{"sparse jumps", 90, 4, 0, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var s Stroke
			settings := signal.Settings{Diagonals: true}.Normalize()
			var now int64
			feed := func(degrees int) {
				angle := float64(90+degrees) * math.Pi / 180
				s.Feed(int(810*math.Cos(angle)), int(760*math.Sin(angle)), now, settings)
				now += 20
			}
			for i := 0; i <= test.count; i++ {
				feed(i * test.step)
			}
			for i := 1; i <= test.reverse; i++ {
				feed((test.count - i) * test.step)
			}
			s.Feed(0, 0, now, settings)
			if _, ok := s.Finish(now+int64(settings.NeutralMs)-1, settings); ok {
				t.Fatal("stroke finished before neutral settled")
			}
			g, ok := s.Finish(now+int64(settings.NeutralMs), settings)
			if !ok || g.Sequence() != test.want {
				t.Fatalf("finished = %v, gesture = %q, want %q", ok, g.Sequence(), test.want)
			}
		})
	}
}

func TestDiagonalIgnoresShortIngressAndReleaseRebound(t *testing.T) {
	settings := signal.Settings{Diagonals: true}.Normalize()
	var s Stroke
	s.Feed(0, 440, 0, settings)
	s.Feed(-570, 600, 20, settings)
	s.Feed(-740, 700, 40, settings)
	s.Feed(0, 0, 100, settings)
	s.Feed(380, -380, 115, settings)
	s.Feed(0, 0, 130, settings)
	g, ok := s.Finish(170, settings)
	if !ok || g.Sequence() != "NW" {
		t.Fatalf("finished = %v, gesture = %q, want NW", ok, g.Sequence())
	}
}
