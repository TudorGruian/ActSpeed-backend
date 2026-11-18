// Package alert delivers the attention call: a reserved gesture that works
// in every mode, never involves the AI, and notifies a carer by webhook.
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Info describes one attention call.
type Info struct {
	DeviceID    string    `json:"device_id"`
	DeviceName  string    `json:"device_name,omitempty"`
	ProfileName string    `json:"profile_name,omitempty"`
	Message     string    `json:"message"`
	Time        time.Time `json:"time"`
}

// Notifier posts alerts to a webhook (ntfy, an SMS gateway, anything that
// accepts a JSON POST). With no URL it only logs.
type Notifier struct {
	URL  string
	Log  *slog.Logger
	http *http.Client
}

// New returns a notifier; url may be empty.
func New(url string, log *slog.Logger) *Notifier {
	return &Notifier{URL: url, Log: log, http: &http.Client{Timeout: 10 * time.Second}}
}

// Send delivers the alert, retrying a few times: this is the one message
// that must not be dropped quietly.
func (n *Notifier) Send(ctx context.Context, info Info) error {
	n.Log.Warn("attention call", "device", info.DeviceID, "profile", info.ProfileName)
	if n.URL == "" {
		return nil
	}
	body, _ := json.Marshal(info)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Title", "ActSpeed attention call") // shown by ntfy
		resp, err := n.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("webhook status %d", resp.StatusCode)
	}
	n.Log.Error("attention call webhook failed", "err", lastErr)
	return lastErr
}
