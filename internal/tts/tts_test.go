package tts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPiperVoiceFollowsLanguage(t *testing.T) {
	var voices []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		voices = append(voices, body["voice"])
		w.Write([]byte("RIFF"))
	}))
	defer srv.Close()
	p, err := New(Config{Provider: "piper", CacheDir: t.TempDir(), PiperURL: srv.URL, PiperVoiceRO: "ro_RO-mihai-medium"})
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"en", "ro-RO", "ro"} {
		if _, err := p.Synthesize(context.Background(), "Da", lang); err != nil {
			t.Fatal(err)
		}
	}
	// English uses the server's default voice; Romanian asks for its own,
	// once: "ro-RO" and "ro" share the cached audio.
	if len(voices) != 2 || voices[0] != nil || voices[1] != "ro_RO-mihai-medium" {
		t.Errorf("voices requested = %v", voices)
	}
}
