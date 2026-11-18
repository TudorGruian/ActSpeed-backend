package session

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/actspeed/actspeed-backend/internal/ai"
	"github.com/actspeed/actspeed-backend/internal/alert"
	"github.com/actspeed/actspeed-backend/internal/gesture"
	"github.com/actspeed/actspeed-backend/internal/model"
	"github.com/actspeed/actspeed-backend/internal/predict"
	"github.com/actspeed/actspeed-backend/internal/scheme"
	"github.com/actspeed/actspeed-backend/internal/signal"
)

const (
	pointerIntervalMs = 30
	predictionCount   = 3
	asyncTimeout      = 30 * time.Second
	inputTimeoutMs    = 2500
)

// State is the externally visible state of a session.
type State struct {
	ProfileID     string            `json:"profile_id"`
	Language      string            `json:"language"`       // of the person: text, voice and interface
	PrimaryClient string            `json:"primary_client"` // the client that acts; "" if none
	Mode          string            `json:"mode"`
	Context       string            `json:"context"`
	Paused        bool              `json:"paused"`
	Buffer        string            `json:"buffer"`
	Predictions   []string          `json:"predictions"`
	Scheme        any               `json:"scheme,omitempty"`
	PointerSlot   int               `json:"pointer_slot"`
	Scrolling     bool              `json:"scrolling"`
	CornerGapMs   int               `json:"corner_gap_ms"`
	Pending       *model.Suggestion `json:"pending_suggestion,omitempty"`
	Assist        *model.Assist     `json:"assist,omitempty"`
	Field         *Field            `json:"field,omitempty"`
	Suggest       *TextSuggest      `json:"suggest,omitempty"` // text mode only
}

// Field is a text field open with the ActSpeed keyboard on the device.
// While one is open, text mode types into it instead of speaking.
type Field struct {
	Active bool   `json:"active"`
	App    string `json:"app,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// Session is the engine for one person (profile), fed by any of their
// joysticks. All methods expect mu to be held.
type Session struct {
	mu  sync.Mutex
	m   *Manager
	id  string // profile id
	via string // device that last gave input, for history

	profile *model.Profile
	set     model.ProfileSettings
	stale   bool

	mode   string
	paused bool

	proc       *signal.Processor
	stroke     gesture.Stroke
	compound   gesture.Gesture
	lastSample int64
	hasSample  bool
	scheme     scheme.Scheme
	pred       *predict.Predictor

	buffer       string
	predictions  []string
	suggest      TextSuggest
	history      []string       // finished messages, newest first
	historyUses  map[string]int // by lower-cased message
	savedPhrases []string
	aiPhrases    []string // Claude's completions for aiFor
	aiFor        string
	gen          int // bumped whenever the buffer or mode changes; stale AI answers are dropped
	pending      *model.Suggestion
	assist       *model.Assist // active screen reading
	assistAt     int64
	lastScreen   *AssistInput // screen of the active reading, for "more"
	field        *Field       // text field open with the ActSpeed keyboard
	fieldMode    bool         // text mode was switched on by the field
	timing       timingLearner

	slot        int // active pointer speed slot
	scrolling   bool
	lastPointer int64
	remX, remY  float64
}

func (s *Session) profileID() *string {
	if s.profile == nil {
		return nil
	}
	return &s.profile.ID
}

// primary is the client that carries out actions, speech and screen
// readings; the person's other clients mirror the panel.
func (s *Session) primary() string {
	if s.profile == nil || s.profile.PrimaryClient == nil {
		return ""
	}
	return *s.profile.PrimaryClient
}

func (s *Session) language() string {
	if s.profile == nil || s.profile.Language == "" {
		return "en"
	}
	return s.profile.Language
}

func (s *Session) context() string { return s.set.Context(s.mode) }

func (s *Session) aiOn() bool {
	return s.m.d.AI.Enabled() && (s.profile == nil || s.profile.AIEnabled)
}

// load (re)reads the profile and everything derived from it. Text in
// progress and the current mode survive a reload.
func (s *Session) load(ctx context.Context, first bool) error {
	st := s.m.d.Store
	p, err := st.GetProfile(ctx, s.id)
	if err != nil {
		return err
	}
	s.profile = &p
	s.set = p.Settings.Normalize()
	if first || s.slot >= len(s.set.PointerSpeeds) {
		s.slot = *s.set.PointerSlot
	}

	if first {
		s.mode, s.paused = p.Mode, p.Paused
		if s.mode != model.ModeText {
			s.mode = model.ModeNav
		}
		s.proc = signal.NewProcessor(s.set.Signal)
	}

	s.pred = predict.New(s.language())
	s.history, s.historyUses = nil, map[string]int{}
	if texts, err := st.RecentUtteranceTexts(ctx, s.profileID(), 500); err == nil {
		for i := len(texts) - 1; i >= 0; i-- { // oldest first, so the newest ends up first
			s.learnText(texts[i])
		}
	}
	phrases, err := st.ListPhrases(ctx, s.profileID(), s.language())
	if err != nil {
		return err
	}
	texts := make([]string, len(phrases))
	for i, p := range phrases {
		texts[i] = p.Text
	}
	s.savedPhrases = texts
	s.scheme = scheme.New(s.set.TextScheme, scheme.Options{Phrases: texts, DwellSelectMs: s.set.DwellSelectMs})
	s.applySignalSettings()
	s.refreshPredictions()
	s.stale = false
	return nil
}

// refresh reloads configuration if it changed through the API.
func (s *Session) refresh(ctx context.Context) {
	if !s.stale {
		return
	}
	if err := s.load(ctx, false); err != nil {
		s.m.d.Log.Error("session reload failed", "profile", s.id, "err", err)
	}
}

// applySignalSettings enables diagonals only where corners are meaningful.
func (s *Session) applySignalSettings() {
	sig := s.set.Signal
	sig.Diagonals = s.context() == "text.corners" || (s.context() == "nav.gesture" && sig.Diagonals)
	s.proc.SetSettings(sig)
}

func (s *Session) publish(channel, typ string, data any) {
	s.m.d.Out.Publish(s.id, s.primary(), channel, typ, data, true)
}

func (s *Session) state() State {
	st := State{
		ProfileID: s.id, Language: s.language(), PrimaryClient: s.primary(), Mode: s.mode, Context: s.context(), Paused: s.paused,
		Buffer: s.buffer, Predictions: s.predictions, PointerSlot: s.slot, Scrolling: s.scrolling, CornerGapMs: s.set.CornerGapMs,
		Pending: s.pending, Assist: s.assist, Field: s.field,
	}
	if s.mode == model.ModeText {
		st.Scheme = s.scheme.State()
		s.buildSuggest()
		sg := s.suggest
		st.Suggest = &sg
	}
	return st
}

// ---- token and gesture flow -------------------------------------------

// stickGestures reports whether directional sequences use the pattern library.
func (s *Session) stickGestures() bool {
	return s.context() == "nav.gesture" || s.context() == "text.corners"
}

func (s *Session) input(ctx context.Context, sample signal.Sample, now int64) {
	// Flush a completed stroke before a newly arriving sample can join it.
	s.advanceGestures(ctx, now)
	s.lastSample, s.hasSample = now, true
	sample.B = 0 // accept older wire packets without using their button field
	s.proc.Feed(sample, now)
	x, y := s.proc.Calibrate(sample)
	s.stroke.Feed(x, y, now, s.proc.Settings())
	if s.m.recordingFor(s.id) != "" {
		s.m.d.Out.Publish(s.id, s.primary(), ChanEvents, "sample", sample, false)
	}
}

func (s *Session) advanceGestures(ctx context.Context, now int64) {
	if s.hasSample && now-s.lastSample > inputTimeoutMs {
		s.hasSample = false
		s.stroke.Reset()
		s.compound = gesture.Gesture{}
		s.proc = signal.NewProcessor(s.proc.Settings())
	}
	if g, done := s.stroke.Finish(now, s.proc.Settings()); done && len(g.Tokens) > 0 {
		s.acceptStroke(ctx, g)
	}
	if len(s.compound.Tokens) > 0 && !s.stroke.Active() &&
		now-s.compound.End >= s.compoundGap() {
		s.flushCompound(ctx)
	}
}

func (s *Session) compoundGap() int64 {
	if s.context() == "text.corners" && !s.compound.Circular() {
		return int64(s.set.CornerGapMs)
	}
	return int64(s.set.GapMs)
}

func (s *Session) flushCompound(ctx context.Context) {
	g := s.compound
	s.compound = gesture.Gesture{}
	s.timing.lastSeq, s.timing.lastEnd, s.timing.lastCtx = g.Sequence(), g.End, s.context()
	s.handleGesture(ctx, g)
}

func (s *Session) acceptStroke(ctx context.Context, g gesture.Gesture) {
	if len(s.compound.Tokens) > 0 &&
		(g.Start-s.compound.End >= s.compoundGap() || s.compound.Circular() != g.Circular()) {
		s.flushCompound(ctx)
	}
	if !g.Circular() {
		if s.paused {
			return
		}
		if s.pending != nil && (g.Sequence() == "E" || g.Sequence() == "W" || (g.Sequence() == "S" && s.pending.Kind == model.SugCorrect)) {
			s.finishCornerStroke(ctx, g)
			return
		}
		switch s.context() {
		case "text.corners":
			s.acceptCornerStroke(ctx, g)
			return
		case "text.grid", "text.twodir":
			if len(g.Tokens) == 1 {
				name := strings.TrimSuffix(g.Tokens[0], "+")
				for _, d := range []signal.Direction{signal.N, signal.E, signal.S, signal.W} {
					if d.String() != name {
						continue
					}
					if s.context() == "text.twodir" && name != g.Tokens[0] {
						out := s.scheme.(*scheme.TwoDir).Held(d)
						s.applySchemeOutputs(ctx, out)
					} else {
						out, _ := s.scheme.Token(signal.Token{Kind: signal.KindMove, Dir: d, T: g.End})
						s.applySchemeOutputs(ctx, out)
					}
					break
				}
			}
			return
		case "nav.pointer":
			return // analog pointer motion is handled separately
		}
	}
	if len(s.compound.Tokens) == 0 {
		s.noteNearMiss(ctx, g)
		s.compound = g
	} else {
		s.noteGap(ctx, g.Start-s.compound.End)
		s.compound.Tokens = append(s.compound.Tokens, g.Tokens...)
		s.compound.End = g.End
	}
	seq := s.compound.Sequence()
	if !s.m.d.Library.HasContinuation(s.profileID(), s.context(), seq) && !s.assistContinues(seq) {
		s.flushCompound(ctx)
	}
}

// acceptCornerStroke extends a pending letter only when the combined
// strokes can still match a configured pattern.
func (s *Session) acceptCornerStroke(ctx context.Context, g gesture.Gesture) {
	seq := s.gestureSequence(g)
	if !slices.ContainsFunc(g.Tokens, func(token string) bool {
		switch strings.TrimSuffix(token, "+") {
		case "NE", "NW", "SE", "SW":
			return true
		}
		return false
	}) {
		s.finishCornerStroke(ctx, g)
		return
	}
	profile, mode := s.profileID(), s.context()
	library := s.m.d.Library
	if len(s.compound.Tokens) > 0 {
		combined := gesture.Gesture{
			Tokens: slices.Concat(s.compound.Tokens, g.Tokens),
			Start:  s.compound.Start, End: g.End,
		}
		next := s.gestureSequence(combined)
		_, match := library.Match(profile, mode, next)
		if next != s.gestureSequence(s.compound) && (match || library.HasContinuation(profile, mode, next)) {
			g, seq = combined, next
			s.compound = gesture.Gesture{}
		} else {
			s.flushCompound(ctx)
		}
	}
	if library.HasContinuation(profile, mode, seq) {
		s.compound = g
		return
	}
	s.handleGesture(ctx, g)
}

func (s *Session) finishCornerStroke(ctx context.Context, g gesture.Gesture) {
	if len(s.compound.Tokens) > 0 {
		s.flushCompound(ctx)
	}
	s.handleGesture(ctx, g)
}

func (s *Session) tick(ctx context.Context, now int64) {
	s.advanceGestures(ctx, now)
	s.expireAssist(ctx, now)
	if s.mode == model.ModeText && !s.paused && s.pending == nil && !s.stroke.Active() {
		s.applySchemeOutputs(ctx, s.scheme.Tick(now))
	}
	s.pointerTick(now)
}

func (s *Session) applySchemeOutputs(ctx context.Context, outs []scheme.Output) {
	for _, o := range outs {
		if o.Kind == "state" {
			s.publishText()
			continue
		}
		s.apply(ctx, o.Kind, o.Value, nil, o.Param)
	}
}

func (s *Session) gestureSequence(g gesture.Gesture) string {
	raw := g.Sequence()
	seq := raw
	if s.mode == model.ModeText && !g.Circular() {
		if _, exact := s.m.d.Library.Match(s.profileID(), s.context(), raw); !exact {
			seq = strings.Join(s.scheme.NormalizeGesture(g.Tokens), " ")
			if s.context() == "text.corners" && len(g.Tokens) == 1 && seq == raw {
				short := strings.TrimSuffix(raw, "+")
				if _, ok := s.m.d.Library.Match(s.profileID(), s.context(), short); ok {
					seq = short
				}
			}
		}
	}
	return seq
}

func (s *Session) handleGesture(ctx context.Context, g gesture.Gesture) {
	raw, seq := g.Sequence(), s.gestureSequence(g)
	if rec := s.m.recordingFor(s.id); rec != "" {
		if err := s.m.d.Store.AppendRecordingGesture(ctx, rec, seq); err != nil {
			s.m.d.Log.Error("recording append failed", "err", err)
		}
	}
	s.publish(ChanEvents, "gesture", map[string]any{
		"tokens": g.Tokens, "sequence": seq, "raw_sequence": raw, "context": s.context(), "duration_ms": g.End - g.Start,
	})

	if s.paused {
		if p, ok := s.m.d.Library.Match(s.profileID(), s.context(), raw); ok && p.OutputKind == model.OutControl &&
			(p.OutputValue == model.CtlPauseToggle || p.OutputValue == model.CtlAlert) {
			s.apply(ctx, p.OutputKind, p.OutputValue, nil, 0)
		}
		return
	}

	// A waiting suggestion is answered with a right or left flick.
	if s.pending != nil {
		switch raw {
		case "E":
			s.resolvePending(ctx, true)
			return
		case "W":
			s.resolvePending(ctx, false)
			return
		case "S":
			if s.pending.Kind == model.SugCorrect {
				s.editPending(ctx)
				return
			}
		}
		s.expirePending(ctx)
	}

	if s.assistGesture(ctx, seq) {
		return
	}

	if p, ok := s.m.d.Library.Match(s.profileID(), s.context(), seq); ok {
		s.apply(ctx, p.OutputKind, p.OutputValue, p.OutputParams, 0)
		s.noteStoredAction(ctx, p.OutputKind, p.OutputValue)
		return
	}

	s.publish(ChanOutput, "unmatched", map[string]any{"sequence": seq, "context": s.context()})
	if s.aiOn() && s.stickGestures() && !g.Circular() {
		s.interpret(seq)
	}
}

// ---- applying outputs --------------------------------------------------

func (s *Session) apply(ctx context.Context, kind, value string, params json.RawMessage, param int) {
	switch kind {
	case model.OutAction:
		a := s.m.actionCatalogue(ctx)[value]
		if len(params) == 0 {
			params = a.Params
		}
		// label is the catalogue's (English) name; clients show it translated.
		s.publish(ChanOutput, "action", map[string]any{"key": value, "params": params, "label": a.Label})
	case model.OutChar:
		if s.mode == model.ModeText {
			s.setBuffer(s.buffer + value)
		}
	case model.OutPhrase:
		s.speak(value, value, true)
	case model.OutControl:
		if len(params) > 0 {
			param = paramIndex(params)
		}
		s.control(ctx, value, param)
	}
}

func (s *Session) control(ctx context.Context, value string, param int) {
	switch value {
	case model.CtlModeToggle:
		if s.mode == model.ModeNav {
			s.setMode(ctx, model.ModeText)
		} else {
			s.setMode(ctx, model.ModeNav)
		}
		return
	case model.CtlPauseToggle:
		s.setPaused(ctx, !s.paused)
		return
	case model.CtlAlert:
		s.raiseAlert()
		return
	case model.CtlAssistLook:
		s.publish(ChanOutput, "assist_request", map[string]any{})
		return
	case model.CtlAssistMore:
		if s.assist == nil || s.lastScreen == nil {
			s.publish(ChanOutput, "assist_request", map[string]any{}) // nothing to extend: read the screen
			return
		}
		s.m.async(func(ctx context.Context) {
			if _, err := s.m.More(ctx, s.id); err != nil && !errors.Is(err, ErrNoActiveAssist) {
				s.m.d.Log.Warn("more proposals failed", "err", err)
			}
		})
		return
	case model.CtlSpeedCycle:
		s.slot = (s.slot + 1) % len(s.set.PointerSpeeds)
		s.publish(ChanOutput, "pointer_speed", map[string]any{
			"slot": s.slot, "speed": s.set.PointerSpeeds[s.slot],
		})
		return
	case model.CtlScrollToggle:
		s.scrolling = !s.scrolling
		s.publish(ChanOutput, "scroll_mode", map[string]any{"scrolling": s.scrolling})
		return
	}
	if s.mode != model.ModeText {
		return
	}
	switch value {
	case model.CtlTextSpace:
		if s.buffer != "" && !strings.HasSuffix(s.buffer, " ") {
			s.setBuffer(s.buffer + " ")
		}
	case model.CtlTextBackspace:
		if s.buffer != "" {
			_, size := utf8.DecodeLastRuneInString(s.buffer)
			s.setBuffer(s.buffer[:len(s.buffer)-size])
		}
	case model.CtlTextClear:
		s.setBuffer("")
	case model.CtlTextAcceptPredic:
		if param >= 0 && param < len(s.predictions) {
			s.setBuffer(predict.Complete(s.buffer, s.predictions[param]))
		}
	case model.CtlTextAcceptPhrase:
		s.acceptPhrase(param)
	case model.CtlTextSpeak, model.CtlTextSpeakAsTyped:
		s.commit(false)
	case model.CtlTextCorrectAndSpeak:
		s.commit(true)
	case model.CtlGridSelect:
		if grid, ok := s.scheme.(*scheme.Grid); ok {
			s.applySchemeOutputs(ctx, grid.Select())
		}
	case model.CtlGridNextPage:
		s.publishText()
	}
}

func (s *Session) setMode(ctx context.Context, mode string) {
	if mode == s.mode {
		return
	}
	s.mode = mode
	s.gen++
	s.scrolling = false
	s.stroke.Reset()
	s.compound = gesture.Gesture{}
	s.applySignalSettings()
	s.expirePending(ctx)
	s.persistState(ctx)
	s.publishMode()
	if mode == model.ModeText {
		s.publishText()
	}
}

func (s *Session) setPaused(ctx context.Context, paused bool) {
	s.paused = paused
	s.stroke.Reset()
	s.compound = gesture.Gesture{}
	s.expirePending(ctx)
	s.persistState(ctx)
	s.publishMode()
}

func (s *Session) persistState(ctx context.Context) {
	if err := s.m.d.Store.SetProfileState(ctx, s.id, s.mode, s.paused); err != nil {
		s.m.d.Log.Error("persist profile state failed", "profile", s.id, "err", err)
	}
}

func (s *Session) publishMode() {
	s.publish(ChanOutput, "mode_changed", map[string]any{
		"mode": s.mode, "context": s.context(), "paused": s.paused,
	})
}

// ---- text ---------------------------------------------------------------

func (s *Session) refreshPredictions() {
	s.predictions = s.pred.Predict(s.buffer, predictionCount)
	if s.predictions == nil {
		s.predictions = []string{}
	}
	s.scheme.SetPredictions(s.predictions)
}

func (s *Session) setBuffer(b string) {
	s.buffer = b
	s.gen++
	if strings.TrimSpace(b) == "" {
		s.aiPhrases, s.aiFor = nil, ""
	}
	s.refreshPredictions()
	s.publishText()
	s.scheduleComplete()
}

func (s *Session) publishText() {
	s.buildSuggest() // gestures depend on the context, which changes with the mode
	s.publish(ChanOutput, "text_update", map[string]any{
		"buffer": s.buffer, "predictions": s.predictions, "suggest": s.suggest,
		"scheme": s.scheme.Name(), "scheme_state": s.scheme.State(),
	})
}

// commit finishes the message, requesting a correction only when asked.
func (s *Session) commit(correct bool) {
	text := strings.TrimSpace(s.buffer)
	if text == "" {
		return
	}
	if !correct || !s.aiOn() {
		s.speak(text, text, true)
		return
	}
	gen := s.gen
	lang := s.language()
	profileID, client := s.profileID(), s.primary()
	s.publish(ChanOutput, "correcting", map[string]any{"text": text})
	s.m.async(func(ctx context.Context) {
		memory := s.m.recentMemory(ctx, client, profileID)
		res, modelID, err := s.m.d.AI.Correct(ctx, ai.CorrectRequest{Text: text, Memory: memory, Language: lang})
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.gen != gen {
			return // the person kept typing; this answer is out of date
		}
		if err != nil {
			s.m.d.Log.Warn("correction unavailable, speaking as typed", "err", err)
		}
		if err != nil || strings.TrimSpace(res.Text) == text {
			s.speak(text, text, true)
			return
		}
		sug := &model.Suggestion{
			DeviceID: s.via, ProfileID: s.profileID(), Kind: model.SugCorrect, Context: s.context(),
			Input: text, Model: modelID, Status: model.SugPending,
			Candidates: []model.Candidate{{Value: strings.TrimSpace(res.Text), Confidence: 1, Reason: res.Notes}},
		}
		s.offer(ctx, sug)
	})
}

// speak emits the speech event. learn adds the text to the person's
// prediction model and history; it clears the buffer when it was the
// composed message.
func (s *Session) speak(composed, final string, learn bool) {
	if learn && s.field != nil {
		s.typeIntoField(composed, final)
		return
	}
	if learn {
		s.learnText(final)
		if strings.TrimSpace(s.buffer) == composed {
			s.buffer = ""
			s.gen++
			s.refreshPredictions()
			s.publishText()
		}
	}
	u := &model.Utterance{
		DeviceID: s.via, ProfileID: s.profileID(), Composed: composed, Final: final, Corrected: composed != final,
	}
	lang, client := s.language(), s.primary()
	s.m.async(func(ctx context.Context) {
		data := map[string]any{"text": final, "language": lang, "provider": s.m.d.TTS.Name()}
		audio, err := s.m.d.TTS.Synthesize(ctx, final, lang)
		if err != nil {
			// Speech must not be lost: clients fall back to their own voice.
			s.m.d.Log.Error("tts failed, falling back to client speech", "err", err)
			data["provider"] = "client"
		} else if audio != nil {
			u.AudioFile = audio.File
			data["audio_url"] = s.m.d.AudioURL(audio.File)
			data["audio_mime"] = audio.MIME
		}
		if learn {
			mem := &model.Memory{DeviceID: client, ProfileID: u.ProfileID, Kind: model.MemText, Content: "Said: " + final}
			if err := s.m.d.Store.CreateMemory(ctx, mem); err != nil {
				s.m.d.Log.Error("save memory failed", "err", err)
			}
			if err := s.m.d.Store.CreateUtterance(ctx, u); err != nil {
				s.m.d.Log.Error("save utterance failed", "err", err)
			}
			data["utterance_id"] = u.ID
		}
		s.m.d.Out.Publish(s.id, client, ChanOutput, "speak", data, true)
	})
}

// typeIntoField sends the finished message to the open text field. It is
// learned like speech, but nothing is said aloud.
func (s *Session) typeIntoField(composed, final string) {
	s.learnText(final)
	if strings.TrimSpace(s.buffer) == composed {
		s.buffer = ""
		s.gen++
		s.refreshPredictions()
		s.publishText()
	}
	app := s.field.App
	s.publish(ChanOutput, "commit_text", map[string]any{"text": final, "app": app})
	s.remember(model.MemText, app, "Typed: "+final)
}

// setField records that a text field opened or closed. Opening one from
// navigation switches to text mode; closing it switches back if the field
// caused the switch and nothing is left half-written.
func (s *Session) setField(ctx context.Context, f Field) {
	if f.Active {
		s.field = &f
		if s.mode == model.ModeNav && !s.paused {
			s.setMode(ctx, model.ModeText)
			s.fieldMode = true
		}
	} else {
		s.field = nil
		if s.fieldMode && s.mode == model.ModeText && strings.TrimSpace(s.buffer) == "" {
			s.setMode(ctx, model.ModeNav)
		}
		s.fieldMode = false
	}
	s.publish(ChanOutput, "field", f)
}

func (s *Session) raiseAlert() {
	msg := s.m.d.AlertMessage
	if m := s.m.d.AlertMessages[s.language()]; m != "" {
		msg = m
	}
	info := alert.Info{DeviceID: s.via, Message: msg, Time: time.Now().UTC()}
	if s.profile != nil {
		info.ProfileName = s.profile.Name
	}
	s.publish(ChanOutput, "alert", info)
	s.speak(info.Message, info.Message, false)
	if s.m.d.Alert != nil {
		s.m.async(func(ctx context.Context) { _ = s.m.d.Alert.Send(ctx, info) })
	}
}

// ---- AI suggestions -----------------------------------------------------

// interpret asks the AI what an unmatched gesture meant. The answer is
// restricted to the vocabulary of the current context.
func (s *Session) interpret(seq string) {
	ctxName := s.context()
	var vocab []ai.VocabItem
	var known []ai.KnownPattern
	seen := map[string]bool{}
	outKind := model.OutChar
	if s.mode == model.ModeNav {
		outKind = model.OutAction
		for _, a := range s.m.actionCatalogue(context.Background()) {
			if a.Key == "pointer.move" || a.Key == "scroll" {
				continue
			}
			vocab = append(vocab, ai.VocabItem{Value: a.Key, Label: a.Label})
		}
	}
	for _, p := range s.m.d.Library.ForContext(s.profileID(), ctxName) {
		if p.OutputKind != outKind {
			continue
		}
		known = append(known, ai.KnownPattern{Sequence: p.Sequence, Value: p.OutputValue})
		if outKind == model.OutChar && !seen[p.OutputValue] {
			seen[p.OutputValue] = true
			vocab = append(vocab, ai.VocabItem{Value: p.OutputValue})
		}
	}
	if len(vocab) == 0 {
		return
	}
	req := ai.InterpretRequest{
		Context: ctxName, Sequence: seq, Vocabulary: vocab, Known: known,
		Recent: s.buffer, Language: s.language(),
	}
	gen := s.gen
	threshold := s.set.AutoApplyThreshold
	profileID := s.profileID()
	s.m.async(func(ctx context.Context) {
		req.Memory = s.m.recentMemory(ctx, s.primary(), profileID)
		cands, modelID, err := s.m.d.AI.Interpret(ctx, req)
		if err != nil {
			s.m.d.Log.Warn("interpretation unavailable", "err", err)
			return
		}
		cands = ai.FilterToVocabulary(cands, vocab)
		if len(cands) == 0 {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		sug := &model.Suggestion{
			DeviceID: s.via, ProfileID: s.profileID(), Kind: model.SugInterpret, Context: ctxName,
			Input: seq, Candidates: cands, Model: modelID, Status: model.SugPending,
		}
		if s.gen != gen || s.context() != ctxName {
			// Too late to act on, but still worth reviewing in the dashboard.
			sug.Status = model.SugExpired
			_ = s.m.d.Store.CreateSuggestion(ctx, sug)
			return
		}
		if cands[0].Confidence >= threshold {
			sug.Status, sug.Chosen = model.SugAutoApplied, cands[0].Value
			if err := s.m.d.Store.CreateSuggestion(ctx, sug); err != nil {
				s.m.d.Log.Error("save suggestion failed", "err", err)
			}
			s.publish(ChanOutput, "suggestion_applied", sug)
			s.apply(ctx, outKind, cands[0].Value, nil, 0)
			return
		}
		s.offer(ctx, sug)
	})
}

// offer stores a suggestion and waits for the person to confirm or reject.
func (s *Session) offer(ctx context.Context, sug *model.Suggestion) {
	s.expirePending(ctx)
	if err := s.m.d.Store.CreateSuggestion(ctx, sug); err != nil {
		s.m.d.Log.Error("save suggestion failed", "err", err)
		if sug.Kind == model.SugCorrect {
			s.speak(sug.Input, sug.Input, true)
		}
		return
	}
	s.pending = sug
	s.publish(ChanOutput, "suggestion", sug)
}

func (s *Session) resolvePending(ctx context.Context, accept bool) {
	sug := s.pending
	s.pending = nil
	status, chosen := model.SugRejected, ""
	if accept {
		status, chosen = model.SugAccepted, sug.Candidates[0].Value
	}
	if err := s.m.d.Store.ResolveSuggestion(ctx, sug.ID, status, chosen); err != nil {
		s.m.d.Log.Error("resolve suggestion failed", "err", err)
	}
	s.publish(ChanOutput, "suggestion_resolved", map[string]any{"id": sug.ID, "status": status, "chosen": chosen})

	switch sug.Kind {
	case model.SugCorrect:
		if accept {
			s.speak(sug.Input, chosen, true)
		} else {
			s.speak(sug.Input, sug.Input, true)
		}
	case model.SugInterpret:
		if !accept {
			return
		}
		kind := model.OutChar
		if strings.HasPrefix(sug.Context, model.ModeNav) {
			kind = model.OutAction
		}
		s.apply(ctx, kind, chosen, nil, 0)
	}
}

// editPending puts a proposed correction into the message instead of
// speaking it, so the person can change it before it is said.
func (s *Session) editPending(ctx context.Context) {
	sug := s.pending
	s.pending = nil
	text := sug.Candidates[0].Value
	if err := s.m.d.Store.ResolveSuggestion(ctx, sug.ID, model.SugEdited, text); err != nil {
		s.m.d.Log.Error("resolve suggestion failed", "err", err)
	}
	s.publish(ChanOutput, "suggestion_resolved", map[string]any{"id": sug.ID, "status": model.SugEdited, "chosen": text})
	if s.mode != model.ModeText {
		s.setMode(ctx, model.ModeText)
	}
	s.setBuffer(text)
}

func (s *Session) expirePending(ctx context.Context) {
	if s.pending == nil {
		return
	}
	sug := s.pending
	s.pending = nil
	if err := s.m.d.Store.ResolveSuggestion(ctx, sug.ID, model.SugExpired, ""); err != nil {
		s.m.d.Log.Error("expire suggestion failed", "err", err)
	}
	s.publish(ChanOutput, "suggestion_resolved", map[string]any{"id": sug.ID, "status": model.SugExpired})
}

// ---- pointer ------------------------------------------------------------

// pointerTick turns stick deflection into relative pointer (or scroll)
// movement, proportional to deflection and the active speed slot.
func (s *Session) pointerTick(now int64) {
	if s.paused || s.pending != nil || s.mode != model.ModeNav || s.set.NavStyle != model.NavPointer {
		s.lastPointer, s.remX, s.remY = 0, 0, 0
		return
	}
	x, y := s.proc.Position()
	if x == 0 && y == 0 {
		s.lastPointer, s.remX, s.remY = 0, 0, 0
		return
	}
	if s.lastPointer == 0 {
		s.lastPointer = now
		return
	}
	dt := now - s.lastPointer
	if dt < pointerIntervalMs {
		return
	}
	if dt > 100 {
		dt = 100
	}
	s.lastPointer = now
	speed := s.set.PointerSpeeds[s.slot]
	s.remX += x * speed * float64(dt) / 1000
	s.remY += -y * speed * float64(dt) / 1000 // screen Y grows downwards
	dx, dy := math.Trunc(s.remX), math.Trunc(s.remY)
	if dx == 0 && dy == 0 {
		return
	}
	s.remX -= dx
	s.remY -= dy
	key := "pointer.move"
	if s.scrolling {
		key = "scroll"
	}
	s.m.d.Out.Publish(s.id, s.primary(), ChanOutput, "action", map[string]any{
		"key": key, "params": map[string]int{"dx": int(dx), "dy": int(dy)},
	}, false)
}
