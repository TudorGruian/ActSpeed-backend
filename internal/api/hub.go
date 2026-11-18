package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Hub fans output messages out to WebSocket clients, mirroring what is
// published on MQTT. Slow clients are dropped rather than allowed to stall
// the input path.
//
// A connection either watches one profile (?profile=id, the dashboard's
// Live page), is a client device (?device=id, an Android phone: it gets the
// messages of the profile it is assigned to), or watches everything.
type Hub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}

	// Connect registers a client device and returns its profile ("" if
	// unassigned); Disconnect marks it offline. Both are optional.
	Connect    func(ctx context.Context, deviceID string) string
	Disconnect func(deviceID string)
}

type wsClient struct {
	profile string // messages of this profile; "" with no device = everything
	device  string // the client device on this connection, if any
	send    chan []byte
}

func (c *wsClient) wants(profileID string) bool {
	if c.device == "" && c.profile == "" {
		return true
	}
	return c.profile != "" && c.profile == profileID
}

// NewHub returns an empty hub.
func NewHub() *Hub { return &Hub{clients: map[*wsClient]struct{}{}} }

// Broadcast queues an already-encoded envelope for matching clients.
func (h *Hub) Broadcast(profileID string, payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.wants(profileID) {
			h.queue(c, payload)
		}
	}
}

// Rebind moves a client device's open connections to another profile ("" =
// unassigned) and tells it, so it reloads its state.
func (h *Hub) Rebind(deviceID, profileID string) {
	payload, _ := json.Marshal(map[string]any{
		"v": 1, "type": "assigned", "profile": profileID, "ts": time.Now().UnixMilli(),
		"data": map[string]any{"device_id": deviceID, "profile_id": profileID},
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.device == deviceID {
			c.profile = profileID
			h.queue(c, payload)
		}
	}
}

// queue sends without blocking; a client that cannot keep up is dropped.
// h.mu must be held.
func (h *Hub) queue(c *wsClient, payload []byte) {
	select {
	case c.send <- payload:
	default:
		close(c.send)
		delete(h.clients, c)
	}
}

func (h *Hub) remove(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		close(c.send)
		delete(h.clients, c)
	}
}

// ServeWS upgrades the request and streams messages until the client leaves.
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Native clients send no Origin; browsers are covered by the token.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	q := r.URL.Query()
	c := &wsClient{profile: q.Get("profile"), device: q.Get("device"), send: make(chan []byte, 256)}
	if c.device != "" {
		c.profile = ""
		if h.Connect != nil {
			c.profile = h.Connect(r.Context(), c.device)
		}
		if h.Disconnect != nil {
			defer h.Disconnect(c.device)
		}
	}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer h.remove(c)

	// CloseRead handles pings/closes and cancels ctx when the peer goes away.
	ctx := conn.CloseRead(r.Context())
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-c.send:
			if !ok {
				conn.Close(websocket.StatusPolicyViolation, "client too slow")
				return
			}
			wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
