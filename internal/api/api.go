// Package api is the REST and WebSocket surface the dashboard and clients
// are built on.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/actspeed/actspeed-backend/internal/gesture"
	"github.com/actspeed/actspeed-backend/internal/model"
	"github.com/actspeed/actspeed-backend/internal/mqtt"
	"github.com/actspeed/actspeed-backend/internal/resolver"
	"github.com/actspeed/actspeed-backend/internal/session"
	"github.com/actspeed/actspeed-backend/internal/signal"
	"github.com/actspeed/actspeed-backend/internal/store"
)

// Server holds the API dependencies.
type Server struct {
	Store        *store.Store
	Sessions     *session.Manager
	Library      *resolver.Library
	Hub          *Hub
	AdminToken   string // empty = no auth (development only)
	TTSDir       string
	DashboardDir string
	MQTTReady    func() bool
	Log          *slog.Logger
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Get("/healthz", s.health)

	r.Route("/v1", func(r chi.Router) {
		r.Use(s.auth)

		s.Hub.Connect, s.Hub.Disconnect = s.clientConnected, s.clientDisconnected
		r.Get("/ws", s.Hub.ServeWS)
		r.Get("/tts/{file}", s.ttsFile)

		r.Get("/profiles", s.listProfiles)
		r.Post("/profiles", s.createProfile)
		// A profile is the person: the live session belongs to it.
		r.Route("/profiles/{id}", func(r chi.Router) {
			r.Get("/", s.getProfile)
			r.Put("/", s.updateProfile)
			r.Delete("/", s.deleteProfile)
			r.Put("/primary", s.setPrimary)
			r.Post("/sample", s.injectProfileSample)
			r.Group(func(r chi.Router) {
				r.Use(s.byProfile)
				s.sessionRoutes(r)
			})
		})

		// Devices are joysticks (input) and clients (Android). The session
		// routes under a device act on the profile it is assigned to.
		r.Get("/devices", s.listDevices)
		r.Post("/devices", s.registerDevice)
		r.Route("/devices/{id}", func(r chi.Router) {
			r.Get("/", s.getDevice)
			r.Patch("/", s.updateDevice)
			r.Delete("/", s.deleteDevice)
			r.Post("/sample", s.injectSample)
			r.Group(func(r chi.Router) {
				r.Use(s.byDevice)
				s.sessionRoutes(r)
			})
		})
		r.Get("/assists", s.listAssists)

		r.Get("/memories", s.listMemories)
		r.Post("/memories", s.createMemory)
		r.Delete("/memories", s.clearMemories)
		r.Delete("/memories/{id}", s.deleteMemory)

		r.Get("/recordings", s.listRecordings)
		r.Get("/recordings/{id}", s.getRecording)
		r.Post("/recordings/{id}/stop", s.stopRecording)
		r.Post("/recordings/{id}/pattern", s.patternFromRecording)

		r.Get("/actions", s.listActions)
		r.Put("/actions/{key}", s.upsertAction)
		r.Delete("/actions/{key}", s.deleteAction)

		r.Get("/patterns", s.listPatterns)
		r.Post("/patterns", s.createPattern)
		r.Put("/patterns/{id}", s.updatePattern)
		r.Delete("/patterns/{id}", s.deletePattern)

		r.Get("/phrases", s.listPhrases)
		r.Post("/phrases", s.createPhrase)
		r.Delete("/phrases/{id}", s.deletePhrase)

		r.Get("/suggestions", s.listSuggestions)
		r.Post("/suggestions/{id}/accept", s.resolveSuggestion(true))
		r.Post("/suggestions/{id}/reject", s.resolveSuggestion(false))
		r.Post("/suggestions/{id}/edit", s.editSuggestion)
		r.Post("/suggestions/{id}/promote", s.promoteSuggestion)

		r.Get("/utterances", s.listUtterances)
	})
	if s.DashboardDir != "" {
		r.Handle("/*", http.FileServer(http.Dir(s.DashboardDir)))
	}
	return r
}

// auth checks the bearer token. WebSocket clients that cannot set headers
// may pass it as the "token" query parameter instead.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.AdminToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.AdminToken)) != 1 {
			writeErr(w, http.StatusUnauthorized, "invalid or missing token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- helpers ------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "already exists")
	case errors.Is(err, store.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		s.Log.Error("request failed", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func limit(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 500 {
		return 100
	}
	return n
}

// reloadLibrary refreshes the in-memory patterns after a change and makes
// running sessions pick up new configuration.
func (s *Server) reloadLibrary(ctx context.Context) {
	patterns, err := s.Store.ListPatterns(ctx)
	if err != nil {
		s.Log.Error("pattern reload failed", "err", err)
		return
	}
	s.Library.Set(patterns)
	s.Sessions.Invalidate()
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	out := map[string]any{"status": "ok", "database": true, "mqtt": true}
	if err := s.Store.Ping(r.Context()); err != nil {
		out["database"], status = false, http.StatusServiceUnavailable
	}
	if s.MQTTReady != nil && !s.MQTTReady() {
		out["mqtt"], status = false, http.StatusServiceUnavailable
	}
	if status != http.StatusOK {
		out["status"] = "degraded"
	}
	writeJSON(w, status, out)
}

func (s *Server) ttsFile(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(chi.URLParam(r, "file"))
	if s.TTSDir == "" || strings.HasPrefix(name, "tmp-") || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.TTSDir, name))
}

// ---- profiles -----------------------------------------------------------

func validProfile(w http.ResponseWriter, p *model.Profile) bool {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return false
	}
	if p.InputMethod == "" {
		p.InputMethod = "mouth"
	}
	if p.InputMethod != "mouth" && p.InputMethod != "thumb" {
		writeErr(w, http.StatusBadRequest, "input_method must be mouth or thumb")
		return false
	}
	p.Language, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(p.Language)), "-")
	if p.Language == "" {
		p.Language = "en"
	}
	if !slices.Contains(model.Languages, p.Language) {
		writeErr(w, http.StatusBadRequest, "language must be one of "+strings.Join(model.Languages, ", "))
		return false
	}
	p.Settings = p.Settings.Normalize()
	return true
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	out, err := s.Store.ListProfiles(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProfile(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	p := model.Profile{AIEnabled: true}
	if !decode(w, r, &p) || !validProfile(w, &p) {
		return
	}
	out, err := s.Store.CreateProfile(r.Context(), p)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProfile(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	// Decoding onto the stored profile keeps fields the caller left out.
	if !decode(w, r, &p) || !validProfile(w, &p) {
		return
	}
	p.ID = chi.URLParam(r, "id")
	out, err := s.Store.UpdateProfile(r.Context(), p)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Sessions.ProfileChanged(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteProfile(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.fail(w, err)
		return
	}
	s.Sessions.Forget(chi.URLParam(r, "id"))
	s.Sessions.Invalidate()
	s.reloadLibrary(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// ---- devices ------------------------------------------------------------

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	out, err := s.Store.ListDevices(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if p := r.URL.Query().Get("profile_id"); p != "" {
		out = slices.DeleteFunc(out, func(d model.Device) bool { return d.ProfileID == nil || *d.ProfileID != p })
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.GetDevice(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func validKind(k string) bool { return k == model.KindJoystick || k == model.KindClient }

// registerDevice is how a client (an Android phone) introduces itself.
// Joysticks register on their first sample or status report. A new device
// belongs to no profile until it is assigned.
func (s *Server) registerDevice(w http.ResponseWriter, r *http.Request) {
	body := struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
		Name string `json:"name"`
	}{}
	if !decode(w, r, &body) {
		return
	}
	if !mqtt.ValidDeviceID(body.ID) {
		writeErr(w, http.StatusBadRequest, "invalid device id")
		return
	}
	if body.Kind == "" {
		body.Kind = model.KindClient
	}
	if !validKind(body.Kind) {
		writeErr(w, http.StatusBadRequest, "kind must be joystick or client")
		return
	}
	d, err := s.Store.TouchDevice(r.Context(), body.ID, body.Kind)
	if err != nil {
		s.fail(w, err)
		return
	}
	if name := strings.TrimSpace(body.Name); name != "" && d.Name == "" {
		if d, err = s.Store.UpdateDevice(r.Context(), d.ID, name, d.Kind, d.ProfileID); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.Sessions.Invalidate()
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	d, err := s.Store.GetDevice(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	old := d.ProfileID
	body := struct {
		Name      *string         `json:"name"`
		Kind      *string         `json:"kind"`
		ProfileID json.RawMessage `json:"profile_id"` // absent = keep, null = unassign
	}{}
	if !decode(w, r, &body) {
		return
	}
	if body.Name != nil {
		d.Name = strings.TrimSpace(*body.Name)
	}
	if body.Kind != nil {
		if !validKind(*body.Kind) {
			writeErr(w, http.StatusBadRequest, "kind must be joystick or client")
			return
		}
		d.Kind = *body.Kind
	}
	if len(body.ProfileID) > 0 {
		d.ProfileID = nil
		if string(body.ProfileID) != "null" {
			var pid string
			if err := json.Unmarshal(body.ProfileID, &pid); err != nil {
				writeErr(w, http.StatusBadRequest, "profile_id must be a string or null")
				return
			}
			d.ProfileID = &pid
		}
	}
	out, err := s.Store.UpdateDevice(r.Context(), id, d.Name, d.Kind, d.ProfileID)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.devicesChanged(r.Context(), out, old)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	d, err := s.Store.GetDevice(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.DeleteDevice(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	old := d.ProfileID
	d.ProfileID = nil
	s.devicesChanged(r.Context(), d, old)
	w.WriteHeader(http.StatusNoContent)
}

// devicesChanged moves a client's connections to its new profile and tells
// the profiles involved, whose primary client may have changed.
func (s *Server) devicesChanged(ctx context.Context, d model.Device, old *string) {
	pid := ""
	if d.ProfileID != nil {
		pid = *d.ProfileID
	}
	s.Hub.Rebind(d.ID, pid)
	s.Sessions.Invalidate()
	for _, p := range []*string{old, d.ProfileID} {
		if p != nil {
			s.Sessions.DevicesChanged(ctx, *p)
		}
	}
}

// setPrimary picks which of a profile's clients acts on the screen.
func (s *Server) setPrimary(w http.ResponseWriter, r *http.Request) {
	body := struct {
		DeviceID string `json:"device_id"`
	}{}
	if !decode(w, r, &body) {
		return
	}
	id := chi.URLParam(r, "id")
	p, err := s.Store.SetPrimaryClient(r.Context(), id, body.DeviceID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusBadRequest, "the device is not a client of this profile")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Sessions.DevicesChanged(r.Context(), id)
	writeJSON(w, http.StatusOK, p)
}

// clientConnected registers a client device opening the live link and
// returns the profile whose messages it should get.
func (s *Server) clientConnected(ctx context.Context, deviceID string) string {
	if !mqtt.ValidDeviceID(deviceID) {
		return ""
	}
	d, err := s.Sessions.Device(ctx, deviceID, model.KindClient)
	if err != nil {
		s.Log.Error("client registration failed", "device", deviceID, "err", err)
		return ""
	}
	if err := s.Store.SetDeviceStatus(ctx, deviceID, "online", ""); err != nil {
		s.Log.Error("save device status failed", "device", deviceID, "err", err)
	}
	if d.ProfileID == nil {
		return ""
	}
	return *d.ProfileID
}

func (s *Server) clientDisconnected(deviceID string) {
	if !mqtt.ValidDeviceID(deviceID) {
		return
	}
	if err := s.Store.SetDeviceStatus(context.Background(), deviceID, "offline", ""); err != nil {
		s.Log.Error("save device status failed", "device", deviceID, "err", err)
	}
}

// ---- the live session ---------------------------------------------------

type ctxKey int

const (
	keyProfile ctxKey = iota
	keyDevice
)

// sessionRoutes are the same under /profiles/{id} (dashboard) and
// /devices/{id} (a client or joystick acting for its profile).
func (s *Server) sessionRoutes(r chi.Router) {
	r.Get("/state", s.sessionState)
	r.Post("/mode", s.setMode)
	r.Post("/speak", s.speak)
	r.Post("/recordings", s.startRecording)
	r.Post("/assist", s.assist)
	r.Delete("/assist", s.dismissAssist)
	r.Post("/assist/choose", s.chooseProposal)
	r.Post("/assist/request", s.requestLook)
	r.Post("/assist/more", s.moreProposals)
	r.Post("/assist/hide", s.hideProposal)
	r.Post("/field", s.setField)
}

func (s *Server) byProfile(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if _, err := s.Store.GetProfile(r.Context(), id); err != nil {
			s.fail(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyProfile, id)))
	})
}

func (s *Server) byDevice(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		d, err := s.Store.GetDevice(r.Context(), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		if d.ProfileID == nil {
			writeErr(w, http.StatusConflict, "this device is not assigned to a profile yet")
			return
		}
		ctx := context.WithValue(r.Context(), keyProfile, *d.ProfileID)
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, keyDevice, id)))
	})
}

// profileKey is the profile whose session a request acts on.
func profileKey(r *http.Request) string { v, _ := r.Context().Value(keyProfile).(string); return v }

// deviceKey is the client device making the request, if it came through
// /devices/{id}.
func deviceKey(r *http.Request) string { v, _ := r.Context().Value(keyDevice).(string); return v }

// notPrimary reports whether the request comes from a client that is not
// its profile's primary one. Only the primary client acts on its screen.
func (s *Server) notPrimary(r *http.Request) bool {
	dev := deviceKey(r)
	if dev == "" {
		return false
	}
	st, err := s.Sessions.State(r.Context(), profileKey(r))
	return err == nil && st.PrimaryClient != dev
}

func (s *Server) sessionState(w http.ResponseWriter, r *http.Request) {
	st, err := s.Sessions.State(r.Context(), profileKey(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) setMode(w http.ResponseWriter, r *http.Request) {
	id := profileKey(r)
	body := struct {
		Mode   string `json:"mode"`
		Paused *bool  `json:"paused"`
	}{}
	if !decode(w, r, &body) {
		return
	}
	if body.Mode != "" && body.Mode != model.ModeNav && body.Mode != model.ModeText {
		writeErr(w, http.StatusBadRequest, "mode must be nav or text")
		return
	}
	st, err := s.Sessions.SetMode(r.Context(), id, body.Mode, body.Paused)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) speak(w http.ResponseWriter, r *http.Request) {
	id := profileKey(r)
	body := struct {
		Text string `json:"text"`
	}{}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeErr(w, http.StatusBadRequest, "text is required")
		return
	}
	if err := s.Sessions.Speak(r.Context(), id, strings.TrimSpace(body.Text)); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func readSample(w http.ResponseWriter, r *http.Request) (signal.Sample, bool) {
	var sample signal.Sample
	if !decode(w, r, &sample) {
		return sample, false
	}
	clamp := func(v int) int { return max(-1000, min(1000, v)) }
	sample.X, sample.Y = clamp(sample.X), clamp(sample.Y)
	return sample, true
}

// injectSample feeds one joystick sample in over HTTP instead of MQTT. It
// registers the joystick on first use exactly like a real one; an
// unassigned joystick only shows up on the dashboard as moving.
func (s *Server) injectSample(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !mqtt.ValidDeviceID(id) {
		writeErr(w, http.StatusBadRequest, "invalid device id")
		return
	}
	sample, ok := readSample(w, r)
	if !ok {
		return
	}
	// Detached from the request: suggestions and speech outlive this call.
	s.Sessions.HandleSample(context.WithoutCancel(r.Context()), id, sample)
	w.WriteHeader(http.StatusNoContent)
}

// injectProfileSample backs the dashboard's on-screen test joystick: it
// drives the profile's session without a physical device.
func (s *Server) injectProfileSample(w http.ResponseWriter, r *http.Request) {
	sample, ok := readSample(w, r)
	if !ok {
		return
	}
	if err := s.Sessions.HandleProfileSample(context.WithoutCancel(r.Context()), chi.URLParam(r, "id"), sample); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- recordings ---------------------------------------------------------

func (s *Server) startRecording(w http.ResponseWriter, r *http.Request) {
	id := profileKey(r)
	body := struct {
		Label string `json:"label"`
	}{}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	rec, err := s.Store.StartRecording(r.Context(), id, body.Label)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Sessions.SetRecording(id, rec.ID)
	writeJSON(w, http.StatusCreated, rec)
}

func (s *Server) stopRecording(w http.ResponseWriter, r *http.Request) {
	rec, err := s.Store.StopRecording(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	if rec.ProfileID != nil {
		s.Sessions.SetRecording(*rec.ProfileID, "")
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) getRecording(w http.ResponseWriter, r *http.Request) {
	rec, err := s.Store.GetRecording(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) listRecordings(w http.ResponseWriter, r *http.Request) {
	out, err := s.Store.ListRecordings(r.Context(), limit(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// patternFromRecording turns one recorded gesture into a pattern: the
// "record a gesture, then say what it means" flow of the dashboard.
func (s *Server) patternFromRecording(w http.ResponseWriter, r *http.Request) {
	rec, err := s.Store.GetRecording(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err)
		return
	}
	body := struct {
		Index int `json:"index"`
		model.Pattern
	}{}
	if !decode(w, r, &body) {
		return
	}
	if body.Index < 0 || body.Index >= len(rec.Gestures) {
		writeErr(w, http.StatusBadRequest, "index is outside the recorded gestures")
		return
	}
	p := body.Pattern
	p.Sequence, p.Source, p.Enabled = rec.Gestures[body.Index], "recorded", true
	s.savePattern(w, r, p, true)
}

// ---- actions ------------------------------------------------------------

func (s *Server) listActions(w http.ResponseWriter, r *http.Request) {
	out, err := s.Store.ListActions(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) upsertAction(w http.ResponseWriter, r *http.Request) {
	var a model.Action
	if !decode(w, r, &a) {
		return
	}
	a.Key = chi.URLParam(r, "key")
	if a.Label == "" {
		writeErr(w, http.StatusBadRequest, "label is required")
		return
	}
	if err := s.Store.UpsertAction(r.Context(), a); err != nil {
		s.fail(w, err)
		return
	}
	s.Sessions.Invalidate()
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) deleteAction(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteAction(r.Context(), chi.URLParam(r, "key")); err != nil {
		s.fail(w, err)
		return
	}
	s.Sessions.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

// ---- patterns -----------------------------------------------------------

var validModes = map[string]bool{
	"any": true, "nav": true, "nav.pointer": true, "nav.gesture": true,
	"text": true, "text.twodir": true, "text.corners": true, "text.grid": true,
}

var validTokens = map[string]bool{gesture.Clockwise: true, gesture.Counterclockwise: true}

func init() {
	for _, d := range []string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"} {
		validTokens[d], validTokens[d+"+"] = true, true
	}
}

func (s *Server) validPattern(w http.ResponseWriter, r *http.Request, p *model.Pattern) bool {
	p.Sequence = gesture.NormalizeSequence(p.Sequence)
	tokens := gesture.ParseSequence(p.Sequence)
	if len(tokens) == 0 {
		writeErr(w, http.StatusBadRequest, "sequence is required")
		return false
	}
	isCircle := func(t string) bool { return t == gesture.Clockwise || t == gesture.Counterclockwise }
	for _, t := range tokens {
		if isCircle(t) != isCircle(tokens[0]) {
			writeErr(w, http.StatusBadRequest, "a pattern must contain either circles or directions")
			return false
		}
		if !validTokens[t] {
			writeErr(w, http.StatusBadRequest, "unknown token "+strconv.Quote(t)+" in sequence")
			return false
		}
	}
	if !validModes[p.Mode] {
		writeErr(w, http.StatusBadRequest, "mode must be one of any, nav, nav.pointer, nav.gesture, text, text.twodir, text.corners, text.grid")
		return false
	}
	if p.OutputValue == "" {
		writeErr(w, http.StatusBadRequest, "output_value is required")
		return false
	}
	switch p.OutputKind {
	case model.OutChar, model.OutPhrase, model.OutControl:
	case model.OutAction:
		actions, err := s.Store.ListActions(r.Context())
		if err != nil {
			s.fail(w, err)
			return false
		}
		found := false
		for _, a := range actions {
			found = found || a.Key == p.OutputValue
		}
		if !found {
			writeErr(w, http.StatusBadRequest, "output_value is not an action in the catalogue")
			return false
		}
	default:
		writeErr(w, http.StatusBadRequest, "output_kind must be action, char, phrase or control")
		return false
	}
	return true
}

func (s *Server) savePattern(w http.ResponseWriter, r *http.Request, p model.Pattern, create bool) (model.Pattern, bool) {
	if !s.validPattern(w, r, &p) {
		return p, false
	}
	var out model.Pattern
	var err error
	if create {
		out, err = s.Store.CreatePattern(r.Context(), p)
	} else {
		out, err = s.Store.UpdatePattern(r.Context(), p)
	}
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "a pattern with this sequence already exists for this mode and scope")
			return p, false
		}
		s.fail(w, err)
		return p, false
	}
	s.reloadLibrary(r.Context())
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
	return out, true
}

func (s *Server) listPatterns(w http.ResponseWriter, r *http.Request) {
	all, err := s.Store.ListPatterns(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	mode, profile := r.URL.Query().Get("mode"), r.URL.Query().Get("profile_id")
	out := []model.Pattern{}
	for _, p := range all {
		if mode != "" && p.Mode != mode {
			continue
		}
		if profile != "" && (p.ProfileID == nil || *p.ProfileID != profile) {
			continue
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createPattern(w http.ResponseWriter, r *http.Request) {
	p := model.Pattern{Enabled: true}
	if !decode(w, r, &p) {
		return
	}
	p.Source = "manual"
	s.savePattern(w, r, p, true)
}

func (s *Server) updatePattern(w http.ResponseWriter, r *http.Request) {
	p := model.Pattern{Enabled: true}
	if !decode(w, r, &p) {
		return
	}
	p.ID = chi.URLParam(r, "id")
	s.savePattern(w, r, p, false)
}

func (s *Server) deletePattern(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeletePattern(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.fail(w, err)
		return
	}
	s.reloadLibrary(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// ---- phrases ------------------------------------------------------------

func (s *Server) listPhrases(w http.ResponseWriter, r *http.Request) {
	var pid *string
	lang := r.URL.Query().Get("language")
	if v := r.URL.Query().Get("profile_id"); v != "" {
		pid = &v
		if lang == "" {
			if p, err := s.Store.GetProfile(r.Context(), v); err == nil {
				lang = p.Language
			}
		}
	}
	out, err := s.Store.ListPhrases(r.Context(), pid, lang)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) createPhrase(w http.ResponseWriter, r *http.Request) {
	var p model.Phrase
	if !decode(w, r, &p) {
		return
	}
	if p.Text = strings.TrimSpace(p.Text); p.Text == "" {
		writeErr(w, http.StatusBadRequest, "text is required")
		return
	}
	out, err := s.Store.CreatePhrase(r.Context(), p)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.Sessions.Invalidate()
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) deletePhrase(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeletePhrase(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.fail(w, err)
		return
	}
	s.Sessions.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

// ---- suggestions --------------------------------------------------------

func (s *Server) listSuggestions(w http.ResponseWriter, r *http.Request) {
	out, err := s.Store.ListSuggestions(r.Context(), r.URL.Query().Get("status"), limit(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveSuggestion lets a carer answer a pending suggestion from the
// dashboard. If the device is still waiting on it, the session acts on it.
func (s *Server) resolveSuggestion(accept bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		sug, err := s.Store.GetSuggestion(r.Context(), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		if sug.Status != model.SugPending {
			writeErr(w, http.StatusConflict, "suggestion is already "+sug.Status)
			return
		}
		if sug.ProfileID == nil || !s.Sessions.ResolveExternally(r.Context(), *sug.ProfileID, id, accept) {
			status, chosen := model.SugRejected, ""
			if accept && len(sug.Candidates) > 0 {
				status, chosen = model.SugAccepted, sug.Candidates[0].Value
			}
			if err := s.Store.ResolveSuggestion(r.Context(), id, status, chosen); err != nil {
				s.fail(w, err)
				return
			}
		}
		out, err := s.Store.GetSuggestion(r.Context(), id)
		if err != nil {
			s.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// editSuggestion puts a pending correction back into the person's message
// so it can be changed before it is spoken.
func (s *Server) editSuggestion(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sug, err := s.Store.GetSuggestion(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if sug.Status != model.SugPending || sug.Kind != model.SugCorrect {
		writeErr(w, http.StatusConflict, "only a pending correction can be edited")
		return
	}
	if sug.ProfileID == nil || !s.Sessions.EditExternally(r.Context(), *sug.ProfileID, id) {
		writeErr(w, http.StatusConflict, "the device is no longer waiting on this suggestion")
		return
	}
	out, err := s.Store.GetSuggestion(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// promoteSuggestion turns an interpreted gesture into a permanent pattern,
// so next time it matches without the AI.
func (s *Server) promoteSuggestion(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sug, err := s.Store.GetSuggestion(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if sug.Kind != model.SugInterpret {
		writeErr(w, http.StatusBadRequest, "only gesture interpretations can become patterns")
		return
	}
	if sug.PatternID != nil {
		writeErr(w, http.StatusConflict, "suggestion was already promoted")
		return
	}
	body := struct {
		Value  string `json:"value"`  // defaults to the chosen / top candidate
		Global bool   `json:"global"` // default: scoped to the person's profile
	}{}
	if r.ContentLength != 0 && !decode(w, r, &body) {
		return
	}
	value := body.Value
	if value == "" {
		value = sug.Chosen
	}
	if value == "" && len(sug.Candidates) > 0 {
		value = sug.Candidates[0].Value
	}
	kind := model.OutChar
	if strings.HasPrefix(sug.Context, model.ModeNav) {
		kind = model.OutAction
	}
	p := model.Pattern{
		Mode: sug.Context, Sequence: sug.Input, OutputKind: kind, OutputValue: value,
		Source: "ai_promoted", Enabled: true,
	}
	if !body.Global {
		p.ProfileID = sug.ProfileID
	}
	out, ok := s.savePattern(w, r, p, true)
	if !ok {
		return
	}
	if err := s.Store.SetSuggestionPattern(r.Context(), id, out.ID); err != nil {
		s.Log.Error("link suggestion to pattern failed", "err", err)
	}
}

func (s *Server) listUtterances(w http.ResponseWriter, r *http.Request) {
	out, err := s.Store.ListUtterances(r.Context(), limit(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
