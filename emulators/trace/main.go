// Command trace pretends to be an ActSpeed joystick: it publishes raw
// samples over MQTT so the backend can be exercised without hardware.
//
// Sequence language (space separated):
//
//	N E S W NE ...   flick in a direction
//	N+ E+ ...        push and hold a direction
//	CW CCW          full circles
//	|                pause long enough to end the gesture
//	>E:800           hold a direction for 800 ms (pointer movement)
//
// Examples:
//
//	go run ./emulators/trace -seq "N | CCW |"
//	go run ./emulators/trace -seq "CW |" -spell "hello"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/actspeed/actspeed-backend/internal/mqtt"
	"github.com/actspeed/actspeed-backend/internal/scheme"
	"github.com/actspeed/actspeed-backend/internal/signal"
)

const (
	flickMs = 150
	restMs  = 150
	holdMs  = 900
	pauseMs = 900
)

var vectors = map[string][2]int{
	"N": {0, 900}, "S": {0, -900}, "E": {900, 0}, "W": {-900, 0},
	"NE": {650, 650}, "NW": {-650, 650}, "SE": {650, -650}, "SW": {-650, -650},
}

// morseSequence spells text for the two-direction scheme: left = dot,
// right = dash, up = end of letter; CW CCW is space.
func morseSequence(text string) (string, error) {
	var parts []string
	for _, r := range strings.ToLower(text) {
		if r == ' ' {
			parts = append(parts, "| CW CCW |")
			continue
		}
		code, ok := scheme.MorseFor(string(r))
		if !ok {
			return "", fmt.Errorf("no code for %q", r)
		}
		for _, c := range code {
			if c == '.' {
				parts = append(parts, "W")
			} else {
				parts = append(parts, "E")
			}
		}
		parts = append(parts, "N")
	}
	return strings.Join(parts, " "), nil
}

type sim struct {
	cm    *autopaho.ConnectionManager
	topic string
	seq   uint32
	start time.Time
	x, y  int
}

func (s *sim) send(ctx context.Context) error {
	s.seq++
	payload, _ := json.Marshal(signal.Sample{
		Seq: s.seq, T: time.Since(s.start).Milliseconds(), X: s.x, Y: s.y,
	})
	_, err := s.cm.Publish(ctx, &paho.Publish{Topic: s.topic, Payload: payload, QoS: 0})
	return err
}

func (s *sim) set(ctx context.Context, x, y int, d time.Duration) error {
	s.x, s.y = x, y
	if err := s.send(ctx); err != nil {
		return err
	}
	time.Sleep(d)
	return nil
}

func (s *sim) play(ctx context.Context, seq string) error {
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	for _, tok := range strings.Fields(seq) {
		var err error
		switch {
		case tok == "|":
			time.Sleep(ms(pauseMs))
		case tok == "CW" || tok == "CCW":
			sign := 1.0
			if tok == "CW" {
				sign = -1
			}
			for i := 0; i <= 24; i++ {
				angle := sign * float64(i) * 2 * math.Pi / 24
				if err = s.set(ctx, int(850*math.Cos(angle)), int(850*math.Sin(angle)), ms(25)); err != nil {
					break
				}
			}
			if err == nil {
				err = s.set(ctx, 0, 0, ms(restMs))
			}
		case strings.HasPrefix(tok, ">"):
			dir, dur, _ := strings.Cut(tok[1:], ":")
			v, ok := vectors[dir]
			n, convErr := strconv.Atoi(dur)
			if !ok || convErr != nil {
				return fmt.Errorf("bad move token %q", tok)
			}
			if err = s.set(ctx, v[0], v[1], ms(n)); err == nil {
				err = s.set(ctx, 0, 0, ms(restMs))
			}
		default:
			d := flickMs
			name := tok
			if strings.HasSuffix(tok, "+") {
				d, name = holdMs, strings.TrimSuffix(tok, "+")
			}
			v, ok := vectors[name]
			if !ok {
				return fmt.Errorf("unknown token %q", tok)
			}
			if err = s.set(ctx, v[0], v[1], ms(d)); err == nil {
				err = s.set(ctx, 0, 0, ms(restMs))
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func main() {
	broker := flag.String("broker", "mqtt://localhost:1883", "MQTT broker URL")
	device := flag.String("device", "sim-1", "device id")
	seq := flag.String("seq", "", "sequence to play (see package doc)")
	spell := flag.String("spell", "", "text to spell with the two-direction scheme, then speak")
	flag.Parse()

	if err := run(*broker, *device, *seq, *spell); err != nil {
		fmt.Fprintln(os.Stderr, "trace:", err)
		os.Exit(1)
	}
}

func run(broker, device, seq, spell string) error {
	if !mqtt.ValidDeviceID(device) {
		return fmt.Errorf("invalid device id %q", device)
	}
	if spell != "" {
		m, err := morseSequence(spell)
		if err != nil {
			return err
		}
		seq = strings.TrimSpace(seq + " " + m + " | CCW |")
	}
	if seq == "" {
		return fmt.Errorf("nothing to play: pass -seq and/or -spell")
	}
	u, err := url.Parse(broker)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	status := mqtt.Topic(device, "status")
	cm, err := autopaho.NewConnection(ctx, autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{u},
		KeepAlive:                     30,
		CleanStartOnInitialConnection: true,
		WillMessage:                   &paho.WillMessage{Topic: status, Payload: []byte("offline"), QoS: 1, Retain: true},
		ClientConfig:                  paho.ClientConfig{ClientID: "joysim-" + device},
	})
	if err != nil {
		return err
	}
	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	defer wcancel()
	if err := cm.AwaitConnection(wctx); err != nil {
		return fmt.Errorf("could not reach broker %s: %w", broker, err)
	}
	online, _ := json.Marshal(map[string]string{"status": "online", "fw": "joysim"})
	if _, err := cm.Publish(ctx, &paho.Publish{Topic: status, Payload: online, QoS: 1, Retain: true}); err != nil {
		return err
	}

	s := &sim{cm: cm, topic: mqtt.Topic(device, "raw"), start: time.Now()}
	fmt.Println("playing:", seq)
	if err := s.play(ctx, seq); err != nil {
		return err
	}
	time.Sleep(pauseMs * time.Millisecond)
	_, _ = cm.Publish(ctx, &paho.Publish{Topic: status, Payload: []byte("offline"), QoS: 1, Retain: true})
	dctx, dcancel := context.WithTimeout(ctx, 2*time.Second)
	defer dcancel()
	return cm.Disconnect(dctx)
}
