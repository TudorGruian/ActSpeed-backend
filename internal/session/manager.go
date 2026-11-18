// Package session is the per-person engine: one session per profile, fed by
// any of the profile's joysticks. It feeds samples through the
// signal processor, the active text scheme and the gesture segmenter,
// resolves gestures against the pattern library, and falls back to the AI
// interpreter — whose proposals are always stored as reviewable suggestions.
package session

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/actspeed/actspeed-backend/internal/ai"
	"github.com/actspeed/actspeed-backend/internal/alert"
	"github.com/actspeed/actspeed-backend/internal/model"
	"github.com/actspeed/actspeed-backend/internal/resolver"
	"github.com/actspeed/actspeed-backend/internal/signal"
	"github.com/actspeed/actspeed-backend/internal/tts"
)

// Channels a message can be published on.
const (
	ChanEvents = "events" // normalized gestures
	ChanOutput = "output" // actions, text, suggestions, speech, mode changes
)

// Publisher delivers messages to a profile's clients and dashboards (MQTT
// and WebSocket). client names the primary client, the one that acts on the
// message; the others only mirror it. reliable selects QoS 1; high-rate
// pointer movement is sent unreliably. An empty profile reaches dashboards
// only (input from an unassigned joystick).
type Publisher interface {
	Publish(profileID, client, channel, msgType string, data any, reliable bool)
}

// Store is the persistence the engine needs.
type Store interface {
	TouchDevice(ctx context.Context, id, kind string) (model.Device, error)
	GetProfile(ctx context.Context, id string) (model.Profile, error)
	SetProfileState(ctx context.Context, id, mode string, paused bool) error
	ListPhrases(ctx context.Context, profileID *string, language string) ([]model.Phrase, error)
	RecentUtteranceTexts(ctx context.Context, profileID *string, limit int) ([]string, error)
	ListActions(ctx context.Context) ([]model.Action, error)
	CreateSuggestion(ctx context.Context, s *model.Suggestion) error
	ResolveSuggestion(ctx context.Context, id, status, chosen string) error
	CreateUtterance(ctx context.Context, u *model.Utterance) error
	AppendRecordingGesture(ctx context.Context, recordingID, sequence string) error
	CreateMemory(ctx context.Context, m *model.Memory) error
	RecentMemories(ctx context.Context, deviceID string, profileID *string, limit int) ([]model.Memory, error)
	CreateAssist(ctx context.Context, a *model.Assist) error
	ResolveAssist(ctx context.Context, id, status string, chosen *int) error
	SetProfileGap(ctx context.Context, id string, gapMs int) error
	HiddenLabels(ctx context.Context, deviceID string, profileID *string, app string) ([]string, error)
	ChosenProposals(ctx context.Context, deviceID string, profileID *string, app string, limit int) ([]model.Proposal, error)
}

// Deps are the collaborators of the engine.
type Deps struct {
	Store        Store
	Library      *resolver.Library
	Out          Publisher
	AI           ai.Interpreter
	TTS          tts.Provider
	Alert        *alert.Notifier
	AlertMessage string // spoken when the attention call fires (English)
	// AlertMessages are the attention messages in other languages, by code.
	AlertMessages map[string]string
	AudioURL      func(file string) string
	Log           *slog.Logger
	Now           func() int64 // ms; defaults to wall clock
	// CompleteDelay is the pause in typing before Claude is asked for
	// phrase completions (default 700 ms).
	CompleteDelay time.Duration
}

// Manager owns one Session per profile and knows which profile each
// device belongs to.
type Manager struct {
	d  Deps
	wg sync.WaitGroup

	mu         sync.Mutex
	sessions   map[string]*Session     // by profile id
	devices    map[string]model.Device // by device id; cleared by Invalidate
	unassigned map[string]int64        // device id -> last "unassigned input" notice
	recordings map[string]string       // profile id -> active recording id
	actions    map[string]model.Action
}

// NewManager builds a manager.
func NewManager(d Deps) *Manager {
	if d.Now == nil {
		d.Now = func() int64 { return time.Now().UnixMilli() }
	}
	if d.AI == nil {
		d.AI = ai.Noop{}
	}
	if d.CompleteDelay == 0 {
		d.CompleteDelay = completeDelay
	}
	if d.TTS == nil {
		d.TTS = tts.Client{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.AlertMessage == "" {
		d.AlertMessage = "I need help, please come."
	}
	if d.AlertMessages == nil {
		d.AlertMessages = map[string]string{}
	}
	if d.AlertMessages["ro"] == "" {
		d.AlertMessages["ro"] = "Am nevoie de ajutor, vă rog să veniți."
	}
	if d.AudioURL == nil {
		d.AudioURL = func(f string) string { return "/v1/tts/" + f }
	}
	return &Manager{d: d, sessions: map[string]*Session{}, devices: map[string]model.Device{},
		unassigned: map[string]int64{}, recordings: map[string]string{}}
}

// async runs slow work (AI, speech synthesis, webhooks) off the input path.
func (m *Manager) async(fn func(ctx context.Context)) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), asyncTimeout)
		defer cancel()
		fn(ctx)
	}()
}

// Wait blocks until all background work has finished (shutdown and tests).
func (m *Manager) Wait() { m.wg.Wait() }

// ErrUnassigned means the device belongs to no profile yet.
var ErrUnassigned = errors.New("device is not assigned to a profile")

// Device returns a device, registering it with the given kind on first
// contact. Lookups are cached until the next Invalidate.
func (m *Manager) Device(ctx context.Context, deviceID, kind string) (model.Device, error) {
	m.mu.Lock()
	d, ok := m.devices[deviceID]
	m.mu.Unlock()
	if ok {
		return d, nil
	}
	d, err := m.d.Store.TouchDevice(ctx, deviceID, kind)
	if err != nil {
		return d, err
	}
	m.mu.Lock()
	m.devices[deviceID] = d
	m.mu.Unlock()
	return d, nil
}

// ProfileOf returns the profile a device belongs to.
func (m *Manager) ProfileOf(ctx context.Context, deviceID, kind string) (string, error) {
	d, err := m.Device(ctx, deviceID, kind)
	if err != nil {
		return "", err
	}
	if d.ProfileID == nil {
		return "", ErrUnassigned
	}
	return *d.ProfileID, nil
}

// get returns the session for a profile, creating it on first use.
func (m *Manager) get(ctx context.Context, profileID string) (*Session, error) {
	m.mu.Lock()
	s, ok := m.sessions[profileID]
	m.mu.Unlock()
	if ok {
		return s, nil
	}
	s = &Session{m: m, id: profileID}
	s.mu.Lock()
	err := s.load(ctx, true)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if existing, ok := m.sessions[profileID]; ok {
		s = existing
	} else {
		m.sessions[profileID] = s
	}
	m.mu.Unlock()
	return s, nil
}

// unassignedNoticeMs spaces out "unassigned joystick moved" notices.
const unassignedNoticeMs = 2000

// HandleSample processes one raw sample from a joystick. It drives the
// session of the joystick's profile; a joystick with no profile only tells
// the dashboards that it is moving, so it can be recognised and assigned.
func (m *Manager) HandleSample(ctx context.Context, deviceID string, sample signal.Sample) {
	profileID, err := m.ProfileOf(ctx, deviceID, model.KindJoystick)
	if errors.Is(err, ErrUnassigned) {
		now := m.d.Now()
		m.mu.Lock()
		last, seen := m.unassigned[deviceID]
		notify := !seen || now-last >= unassignedNoticeMs
		if notify {
			m.unassigned[deviceID] = now
		}
		m.mu.Unlock()
		if notify && (sample.X != 0 || sample.Y != 0) {
			m.d.Out.Publish("", "", ChanEvents, "unassigned_input", map[string]any{"device_id": deviceID}, false)
		}
		return
	}
	if err != nil {
		m.d.Log.Error("device lookup failed", "device", deviceID, "err", err)
		return
	}
	m.feed(ctx, profileID, deviceID, sample)
}

// HandleProfileSample feeds a sample straight into a profile's session (the
// dashboard's on-screen test joystick).
func (m *Manager) HandleProfileSample(ctx context.Context, profileID string, sample signal.Sample) error {
	return m.feed(ctx, profileID, "", sample)
}

func (m *Manager) feed(ctx context.Context, profileID, via string, sample signal.Sample) error {
	s, err := m.get(ctx, profileID)
	if err != nil {
		m.d.Log.Error("session load failed", "profile", profileID, "err", err)
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(ctx)
	s.via = via
	now := m.d.Now()
	s.input(ctx, sample, now)
	s.tick(ctx, now)
	return nil
}

// Run drives time-based behaviour (holds, gesture gaps, pointer motion)
// until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.TickAll(ctx)
		}
	}
}

// TickAll advances every session to the current time.
func (m *Manager) TickAll(ctx context.Context) {
	m.mu.Lock()
	list := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.Unlock()
	now := m.d.Now()
	for _, s := range list {
		s.mu.Lock()
		s.tick(ctx, now)
		s.mu.Unlock()
	}
}

// Invalidate makes every session reload its profile, phrases and settings
// on the next sample, and forgets cached device assignments. Called after
// configuration changes through the API.
func (m *Manager) Invalidate() {
	m.mu.Lock()
	m.actions = nil
	m.devices = map[string]model.Device{}
	list := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.Unlock()
	for _, s := range list {
		s.mu.Lock()
		s.stale = true
		s.mu.Unlock()
	}
}

// Forget drops a deleted profile's session.
func (m *Manager) Forget(profileID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, profileID)
	delete(m.recordings, profileID)
}

// DevicesChanged reloads a profile after its devices or primary client
// changed and tells its clients, so each knows whether it is the one that
// acts.
func (m *Manager) DevicesChanged(ctx context.Context, profileID string) {
	m.Invalidate()
	m.mu.Lock()
	s, ok := m.sessions[profileID]
	m.mu.Unlock()
	if !ok {
		m.d.Out.Publish(profileID, "", ChanOutput, "devices_changed", map[string]any{}, true)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(ctx)
	s.publish(ChanOutput, "devices_changed", map[string]any{"primary_client": s.primary()})
}

// ProfileChanged reloads a profile after its settings changed (its
// language, for example) and tells its clients and dashboards, so they
// switch at once rather than at the next joystick input.
func (m *Manager) ProfileChanged(ctx context.Context, profileID string) {
	m.Invalidate()
	m.mu.Lock()
	s, ok := m.sessions[profileID]
	m.mu.Unlock()
	if !ok {
		m.d.Out.Publish(profileID, "", ChanOutput, "profile_changed", map[string]any{}, true)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(ctx)
	s.publish(ChanOutput, "profile_changed", map[string]any{"language": s.language()})
	if s.mode == model.ModeText { // predictions and phrases in the new language
		s.aiPhrases, s.aiFor = nil, ""
		s.refreshPredictions()
		s.publishText()
	}
}

// SetRecording starts (non-empty id) or stops capturing a profile's gestures.
func (m *Manager) SetRecording(profileID, recordingID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if recordingID == "" {
		delete(m.recordings, profileID)
	} else {
		m.recordings[profileID] = recordingID
	}
}

func (m *Manager) recordingFor(profileID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.recordings[profileID]
}

func (m *Manager) actionCatalogue(ctx context.Context) map[string]model.Action {
	m.mu.Lock()
	cached := m.actions
	m.mu.Unlock()
	if cached != nil {
		return cached
	}
	list, err := m.d.Store.ListActions(ctx)
	if err != nil {
		m.d.Log.Error("load actions failed", "err", err)
		return map[string]model.Action{}
	}
	out := make(map[string]model.Action, len(list))
	for _, a := range list {
		out[a.Key] = a
	}
	m.mu.Lock()
	m.actions = out
	m.mu.Unlock()
	return out
}

// SetMode switches a profile's mode and/or pause state from the API.
func (m *Manager) SetMode(ctx context.Context, profileID, mode string, paused *bool) (State, error) {
	s, err := m.get(ctx, profileID)
	if err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(ctx)
	if mode == model.ModeNav || mode == model.ModeText {
		s.setMode(ctx, mode)
	}
	if paused != nil && *paused != s.paused {
		s.setPaused(ctx, *paused)
	}
	return s.state(), nil
}

// State returns a profile's current session state.
func (m *Manager) State(ctx context.Context, profileID string) (State, error) {
	s, err := m.get(ctx, profileID)
	if err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(ctx)
	return s.state(), nil
}

// SetField records that a text field opened or closed with the ActSpeed
// keyboard on the device.
func (m *Manager) SetField(ctx context.Context, profileID string, f Field) (State, error) {
	s, err := m.get(ctx, profileID)
	if err != nil {
		return State{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(ctx)
	s.setField(ctx, f)
	return s.state(), nil
}

// Speak says arbitrary text on the person's behalf (API-initiated).
func (m *Manager) Speak(ctx context.Context, profileID, text string) error {
	s, err := m.get(ctx, profileID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(ctx)
	s.speak(text, text, false)
	return nil
}

// ResolveExternally tells a session that a pending suggestion was accepted
// or rejected through the API rather than by gesture.
// EditExternally puts a pending correction back into the message, from a
// client (overlay or dashboard), so it can be changed before speaking.
func (m *Manager) EditExternally(ctx context.Context, profileID, suggestionID string) bool {
	m.mu.Lock()
	s, ok := m.sessions[profileID]
	m.mu.Unlock()
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil || s.pending.ID != suggestionID || s.pending.Kind != model.SugCorrect {
		return false
	}
	s.editPending(ctx)
	return true
}

func (m *Manager) ResolveExternally(ctx context.Context, profileID, suggestionID string, accept bool) bool {
	m.mu.Lock()
	s, ok := m.sessions[profileID]
	m.mu.Unlock()
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil || s.pending.ID != suggestionID {
		return false
	}
	s.resolvePending(ctx, accept)
	return true
}
