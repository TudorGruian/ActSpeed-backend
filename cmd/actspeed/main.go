// Command actspeed is the ActSpeed backend: it ingests joystick samples
// over MQTT and turns them into navigation actions, text and speech.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/actspeed/actspeed-backend/internal/ai"
	"github.com/actspeed/actspeed-backend/internal/alert"
	"github.com/actspeed/actspeed-backend/internal/api"
	"github.com/actspeed/actspeed-backend/internal/config"
	"github.com/actspeed/actspeed-backend/internal/mqtt"
	"github.com/actspeed/actspeed-backend/internal/resolver"
	"github.com/actspeed/actspeed-backend/internal/session"
	sig "github.com/actspeed/actspeed-backend/internal/signal"
	"github.com/actspeed/actspeed-backend/internal/store"
	"github.com/actspeed/actspeed-backend/internal/tts"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	patterns, err := st.ListPatterns(ctx)
	if err != nil {
		return err
	}
	library := resolver.NewLibrary(patterns)

	var interpreter ai.Interpreter = ai.Noop{}
	if cfg.AIEnabled {
		interpreter = ai.NewClaude(ai.ClaudeConfig{
			APIKey: cfg.AnthropicAPIKey, InterpretModel: cfg.AIModelInterpret,
			CorrectModel: cfg.AIModelCorrect, AssistModel: cfg.AIModelAssist,
			Timeout: cfg.AITimeout, AssistTimeout: cfg.AIAssistTimeout,
		})
		log.Info("ai enabled", "interpret_model", cfg.AIModelInterpret, "correct_model", cfg.AIModelCorrect, "assist_model", cfg.AIModelAssist)
	} else {
		log.Info("ai disabled: gestures resolve against stored patterns only")
	}

	speech, err := tts.New(tts.Config{
		Provider: cfg.TTSProvider, CacheDir: cfg.TTSCacheDir,
		PiperURL: cfg.PiperURL, PiperVoice: cfg.PiperVoice, PiperVoiceRO: cfg.PiperVoiceRO,
		ElevenLabsKey: cfg.ElevenLabsKey, ElevenLabsVoiceID: cfg.ElevenLabsVoiceID,
	})
	if err != nil {
		return err
	}
	log.Info("tts provider", "name", speech.Name())

	hub := api.NewHub()
	var mgr *session.Manager
	managerReady := make(chan struct{})
	broker, err := mqtt.Connect(ctx, mqtt.Config{
		URL: cfg.MQTTURL, Username: cfg.MQTTUsername, Password: cfg.MQTTPassword, ClientID: "actspeed-backend",
	}, mqtt.Handlers{
		OnSample: func(id string, s sig.Sample) {
			select {
			case <-managerReady:
				mgr.HandleSample(ctx, id, s)
			default:
			}
		},
		OnStatus: func(id, status, fw string) {
			if err := st.SetDeviceStatus(ctx, id, status, fw); err != nil {
				log.Error("save device status failed", "device", id, "err", err)
			}
		},
	}, hub.Broadcast, log)
	if err != nil {
		return err
	}
	defer broker.Close()

	mgr = session.NewManager(session.Deps{
		Store: st, Library: library, Out: broker, AI: interpreter, TTS: speech,
		Alert:         alert.New(cfg.AlertWebhookURL, log),
		AlertMessage:  cfg.AlertMessage,
		AlertMessages: map[string]string{"ro": cfg.AlertMessageRO},
		AudioURL:      func(f string) string { return strings.TrimRight(cfg.PublicURL, "/") + "/v1/tts/" + f },
		Log:           log,
	})
	close(managerReady)
	go mgr.Run(ctx)

	if cfg.AdminToken == "" {
		log.Warn("ADMIN_TOKEN is not set: the API is open. Set it before exposing this server.")
	}
	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: (&api.Server{
			Store: st, Sessions: mgr, Library: library, Hub: hub,
			AdminToken: cfg.AdminToken, TTSDir: cfg.TTSCacheDir, DashboardDir: cfg.DashboardDir, MQTTReady: broker.Connected, Log: log,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.HTTPAddr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	mgr.Wait()
	return nil
}
