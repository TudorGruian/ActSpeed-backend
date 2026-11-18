// Package tts turns text into audio through a pluggable provider.
//
//   - client:     no audio is produced; clients speak the text themselves
//   - piper:      self-hosted, offline (https://github.com/OHF-Voice/piper1-gpl)
//   - elevenlabs: cloud voice
//
// Audio is cached on disk by content hash and served by the HTTP API.
package tts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Audio is a synthesized file inside the cache directory.
type Audio struct {
	File string // base name, e.g. "3fa9….wav"
	MIME string
}

// Provider synthesizes speech. A nil Audio with a nil error means "the
// client should speak this text itself".
type Provider interface {
	Name() string
	Synthesize(ctx context.Context, text, language string) (*Audio, error)
}

// Config selects and configures the provider.
type Config struct {
	Provider          string // client | piper | elevenlabs
	CacheDir          string
	PiperURL          string // e.g. http://localhost:5000
	PiperVoice        string // optional, server default otherwise
	PiperVoiceRO      string // voice for Romanian; e.g. ro_RO-mihai-medium
	ElevenLabsKey     string
	ElevenLabsVoiceID string
	ElevenLabsModel   string
}

// New builds the configured provider.
func New(c Config) (Provider, error) {
	switch c.Provider {
	case "", "client":
		return Client{}, nil
	case "piper":
		if c.PiperURL == "" {
			return nil, fmt.Errorf("tts: PIPER_URL is required for the piper provider")
		}
	case "elevenlabs":
		if c.ElevenLabsKey == "" || c.ElevenLabsVoiceID == "" {
			return nil, fmt.Errorf("tts: ELEVENLABS_API_KEY and ELEVENLABS_VOICE_ID are required")
		}
		if c.ElevenLabsModel == "" {
			c.ElevenLabsModel = "eleven_multilingual_v2"
		}
	default:
		return nil, fmt.Errorf("tts: unknown provider %q", c.Provider)
	}
	if err := os.MkdirAll(c.CacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("tts: cache dir: %w", err)
	}
	return &httpProvider{cfg: c, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// Client is the provider that leaves speaking to the client.
type Client struct{}

func (Client) Name() string                                               { return "client" }
func (Client) Synthesize(context.Context, string, string) (*Audio, error) { return nil, nil }

type httpProvider struct {
	cfg  Config
	http *http.Client
}

func (p *httpProvider) Name() string { return p.cfg.Provider }

func (p *httpProvider) Synthesize(ctx context.Context, text, language string) (*Audio, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	ext, mime := ".wav", "audio/wav"
	lang, _, _ := strings.Cut(strings.ToLower(language), "-")
	voice := p.cfg.PiperVoice
	if lang == "ro" && p.cfg.PiperVoiceRO != "" {
		voice = p.cfg.PiperVoiceRO
	}
	if p.cfg.Provider == "elevenlabs" {
		ext, mime = ".mp3", "audio/mpeg"
		voice = p.cfg.ElevenLabsVoiceID
	}
	sum := sha256.Sum256([]byte(p.cfg.Provider + "\x00" + voice + "\x00" + lang + "\x00" + text))
	name := hex.EncodeToString(sum[:16]) + ext
	path := filepath.Join(p.cfg.CacheDir, name)
	if _, err := os.Stat(path); err == nil {
		return &Audio{File: name, MIME: mime}, nil
	}

	var req *http.Request
	var err error
	if p.cfg.Provider == "elevenlabs" {
		payload := map[string]any{"text": text, "model_id": p.cfg.ElevenLabsModel}
		// Multilingual models pick the language from the text; the v2.5
		// models can be told it, which keeps short Romanian words Romanian.
		if lang != "" && strings.Contains(p.cfg.ElevenLabsModel, "v2_5") {
			payload["language_code"] = lang
		}
		body, _ := json.Marshal(payload)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost,
			"https://api.elevenlabs.io/v1/text-to-speech/"+voice, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("xi-api-key", p.cfg.ElevenLabsKey)
			req.Header.Set("Accept", "audio/mpeg")
		}
	} else {
		payload := map[string]any{"text": text}
		if voice != "" {
			payload["voice"] = voice
		}
		body, _ := json.Marshal(payload)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost,
			strings.TrimRight(p.cfg.PiperURL, "/")+"/synthesize", bytes.NewReader(body))
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts %s: %w", p.cfg.Provider, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("tts %s: status %d: %s", p.cfg.Provider, resp.StatusCode, msg)
	}
	tmp, err := os.CreateTemp(p.cfg.CacheDir, "tmp-*")
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	return &Audio{File: name, MIME: mime}, nil
}
