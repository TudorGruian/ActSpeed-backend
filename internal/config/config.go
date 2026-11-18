// Package config reads settings from environment variables.
package config

import (
	"os"
	"strconv"
	"time"
)

// Config is the full runtime configuration.
type Config struct {
	DashboardDir string
	HTTPAddr     string
	PublicURL    string // base URL clients use to fetch audio, e.g. http://host:8080
	AdminToken   string
	DatabaseURL  string

	MQTTURL      string
	MQTTUsername string
	MQTTPassword string

	AIEnabled        bool
	AnthropicAPIKey  string
	AIModelInterpret string
	AIModelCorrect   string
	AIModelAssist    string
	AITimeout        time.Duration
	AIAssistTimeout  time.Duration

	TTSProvider       string
	TTSCacheDir       string
	PiperURL          string
	PiperVoice        string
	PiperVoiceRO      string
	ElevenLabsKey     string
	ElevenLabsVoiceID string

	AlertWebhookURL string
	AlertMessage    string
	AlertMessageRO  string
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

// Load reads the environment. AI is on when an Anthropic credential is
// present, unless AI_ENABLED=false.
func Load() Config {
	c := Config{
		HTTPAddr:     env("HTTP_ADDR", ":8080"),
		PublicURL:    env("PUBLIC_URL", ""),
		AdminToken:   os.Getenv("ADMIN_TOKEN"),
		DashboardDir: env("DASHBOARD_DIR", "./dashboard/dist"),
		DatabaseURL:  env("DATABASE_URL", "postgres://actspeed:actspeed@localhost:5432/actspeed?sslmode=disable"),

		MQTTURL:      env("MQTT_URL", "mqtt://localhost:1883"),
		MQTTUsername: os.Getenv("MQTT_USERNAME"),
		MQTTPassword: os.Getenv("MQTT_PASSWORD"),

		AnthropicAPIKey:  os.Getenv("ANTHROPIC_API_KEY"),
		AIModelInterpret: env("AI_MODEL_INTERPRET", "claude-haiku-5-5"),
		AIModelCorrect:   env("AI_MODEL_CORRECT", "claude-sonnet-5-5"),
		AIModelAssist:    env("AI_MODEL_ASSIST", "claude-haiku-5-5"),
		AITimeout:        envDuration("AI_TIMEOUT", 8*time.Second),
		AIAssistTimeout:  envDuration("AI_ASSIST_TIMEOUT", 25*time.Second),

		TTSProvider:       env("TTS_PROVIDER", "client"),
		TTSCacheDir:       env("TTS_CACHE_DIR", "./data/tts"),
		PiperURL:          env("PIPER_URL", "http://localhost:5000"),
		PiperVoice:        os.Getenv("PIPER_VOICE"),
		PiperVoiceRO:      env("PIPER_VOICE_RO", "ro_RO-mihai-medium"),
		ElevenLabsKey:     os.Getenv("ELEVENLABS_API_KEY"),
		ElevenLabsVoiceID: os.Getenv("ELEVENLABS_VOICE_ID"),

		AlertWebhookURL: os.Getenv("ALERT_WEBHOOK_URL"),
		AlertMessage:    env("ALERT_MESSAGE", "I need help, please come."),
		AlertMessageRO:  env("ALERT_MESSAGE_RO", "Am nevoie de ajutor, vă rog să veniți."),
	}
	hasCred := c.AnthropicAPIKey != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != ""
	c.AIEnabled = envBool("AI_ENABLED", hasCred)
	return c
}
