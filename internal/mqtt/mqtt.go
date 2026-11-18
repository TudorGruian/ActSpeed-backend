// Package mqtt connects the backend to the broker: it receives raw samples
// and status reports from joysticks and publishes each profile's events and
// outputs for its clients. Every published message is also mirrored to the
// WebSocket hub.
package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/actspeed/actspeed-backend/internal/signal"
)

// TopicRoot is the versioned prefix of device (joystick) topics.
const TopicRoot = "actspeed/v1/devices"

// ProfileTopicRoot is the versioned prefix of profile output topics.
const ProfileTopicRoot = "actspeed/v1/profiles"

// Topic builds a device topic, e.g. Topic("esp32-1", "raw").
func Topic(deviceID, leaf string) string { return TopicRoot + "/" + deviceID + "/" + leaf }

// ProfileTopic builds a profile topic, e.g. ProfileTopic(id, "output").
func ProfileTopic(profileID, leaf string) string {
	return ProfileTopicRoot + "/" + profileID + "/" + leaf
}

var deviceIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidDeviceID reports whether id is safe to use in topics and URLs.
func ValidDeviceID(id string) bool { return deviceIDRe.MatchString(id) }

// Envelope is the versioned wrapper around every message the backend emits.
type Envelope struct {
	V       int    `json:"v"`
	Type    string `json:"type"`
	Profile string `json:"profile"`
	// Client is the profile's primary client: the one that carries out
	// actions, speech and screen readings. Other clients only mirror.
	Client string `json:"client,omitempty"`
	TS     int64  `json:"ts"`
	Seq    uint64 `json:"seq"`
	Data   any    `json:"data"`
}

// Config holds broker connection settings.
type Config struct {
	URL      string
	Username string
	Password string
	ClientID string
}

// Handlers receive inbound device messages.
type Handlers struct {
	OnSample func(deviceID string, s signal.Sample)
	OnStatus func(deviceID, status, fwVersion string)
}

type outbound struct {
	topic   string
	payload []byte
	qos     byte
}

// Client is a reconnecting MQTT client.
type Client struct {
	cm        *autopaho.ConnectionManager
	log       *slog.Logger
	connected atomic.Bool
	seq       atomic.Uint64
	queue     chan outbound
	mirror    func(profileID string, payload []byte)
}

// Connect starts the connection manager. It returns immediately; the client
// keeps retrying in the background until ctx is cancelled.
func Connect(ctx context.Context, cfg Config, h Handlers, mirror func(string, []byte), log *slog.Logger) (*Client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("mqtt: bad MQTT_URL: %w", err)
	}
	c := &Client{log: log, queue: make(chan outbound, 1024), mirror: mirror}

	pcfg := autopaho.ClientConfig{
		ServerUrls:                    []*url.URL{u},
		KeepAlive:                     30,
		CleanStartOnInitialConnection: true,
		ConnectUsername:               cfg.Username,
		ConnectPassword:               []byte(cfg.Password),
		OnConnectionUp: func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			c.connected.Store(true)
			log.Info("mqtt connected", "url", u.Redacted())
			if _, err := cm.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{
				{Topic: TopicRoot + "/+/raw", QoS: 0},
				{Topic: TopicRoot + "/+/status", QoS: 1},
			}}); err != nil {
				log.Error("mqtt subscribe failed", "err", err)
			}
		},
		OnConnectError: func(err error) {
			c.connected.Store(false)
			log.Warn("mqtt connect failed, retrying", "err", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID: cfg.ClientID,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					c.dispatch(pr.Packet.Topic, pr.Packet.Payload, h)
					return true, nil
				},
			},
			OnClientError:      func(err error) { log.Warn("mqtt client error", "err", err) },
			OnServerDisconnect: func(*paho.Disconnect) { c.connected.Store(false) },
		},
	}
	cm, err := autopaho.NewConnection(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("mqtt: %w", err)
	}
	c.cm = cm
	go c.writer(ctx)
	return c, nil
}

// Connected reports whether the broker connection is currently up.
func (c *Client) Connected() bool { return c.connected.Load() }

func (c *Client) dispatch(topic string, payload []byte, h Handlers) {
	rest, ok := strings.CutPrefix(topic, TopicRoot+"/")
	if !ok {
		return
	}
	id, leaf, ok := strings.Cut(rest, "/")
	if !ok || !ValidDeviceID(id) {
		return
	}
	switch leaf {
	case "raw":
		var s signal.Sample
		if err := json.Unmarshal(payload, &s); err != nil {
			c.log.Debug("bad raw payload", "device", id, "err", err)
			return
		}
		h.OnSample(id, s)
	case "status":
		// Either a bare word ("online") or {"status":"online","fw":"1.2.0"}.
		st := struct {
			Status string `json:"status"`
			FW     string `json:"fw"`
		}{}
		if json.Unmarshal(payload, &st) != nil || st.Status == "" {
			st.Status = strings.TrimSpace(string(payload))
		}
		if st.Status != "online" && st.Status != "offline" {
			return
		}
		h.OnStatus(id, st.Status, st.FW)
	}
}

// Publish implements session.Publisher. It never blocks the caller: messages
// are queued and written by a background goroutine. Messages without a
// profile only go to the WebSocket hub (dashboards).
func (c *Client) Publish(profileID, client, channel, msgType string, data any, reliable bool) {
	payload, err := json.Marshal(Envelope{
		V: 1, Type: msgType, Profile: profileID, Client: client, TS: time.Now().UnixMilli(), Seq: c.seq.Add(1), Data: data,
	})
	if err != nil {
		c.log.Error("encode message failed", "type", msgType, "err", err)
		return
	}
	if c.mirror != nil {
		c.mirror(profileID, payload)
	}
	if profileID == "" {
		return
	}
	qos := byte(0)
	if reliable {
		qos = 1
	}
	select {
	case c.queue <- outbound{topic: ProfileTopic(profileID, channel), payload: payload, qos: qos}:
	default:
		c.log.Warn("mqtt publish queue full, dropping message", "type", msgType)
	}
}

func (c *Client) writer(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-c.queue:
			pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, err := c.cm.Publish(pctx, &paho.Publish{Topic: m.topic, Payload: m.payload, QoS: m.qos})
			cancel()
			if err != nil && ctx.Err() == nil {
				c.log.Warn("mqtt publish failed", "topic", m.topic, "err", err)
			}
		}
	}
}

// Close disconnects cleanly.
func (c *Client) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.cm.Disconnect(ctx)
}
