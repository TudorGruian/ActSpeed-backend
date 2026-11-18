// Package model holds the domain types shared by the store, the session
// engine and the HTTP API.
package model

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/actspeed/actspeed-backend/internal/signal"
)

// Modes.
const (
	ModeNav  = "nav"
	ModeText = "text"
)

// Navigation styles and text schemes; together with the mode they form the
// session context ("nav.pointer", "text.corners", ...).
const (
	NavPointer = "pointer"
	NavGesture = "gesture"

	SchemeTwoDir  = "twodir"
	SchemeCorners = "corners"
	SchemeGrid    = "grid"
)

// Pattern output kinds.
const (
	OutAction  = "action"  // navigation action from the catalogue
	OutChar    = "char"    // one character appended to the text buffer
	OutPhrase  = "phrase"  // whole phrase (spoken immediately)
	OutControl = "control" // backend control: mode switch, pause, speak, ...
)

// Control values.
const (
	CtlModeToggle          = "control.mode_toggle"
	CtlPauseToggle         = "control.pause_toggle"
	CtlSpeedCycle          = "control.speed_cycle"
	CtlScrollToggle        = "control.scroll_toggle"
	CtlAlert               = "alert.call"
	CtlTextSpace           = "text.space"
	CtlTextBackspace       = "text.backspace"
	CtlTextClear           = "text.clear"
	CtlTextSpeak           = "text.speak"
	CtlTextSpeakAsTyped    = "text.speak_as_typed" // compatibility with existing bindings
	CtlTextCorrectAndSpeak = "text.correct_and_speak"
	CtlTextAcceptPredic    = "text.accept_prediction" // output_params {"index": n}, default 0
	CtlTextAcceptPhrase    = "text.accept_phrase"     // output_params {"index": n}, default 0
	CtlGridNextPage        = "grid.next_page"
	CtlGridSelect          = "grid.select"
)

// ProfileSettings is the tunable behaviour of one person's setup.
type ProfileSettings struct {
	Signal             signal.Settings `json:"signal"`
	GapMs              int             `json:"gap_ms"`
	CornerGapMs        int             `json:"corner_gap_ms"`
	NavStyle           string          `json:"nav_style"`
	TextScheme         string          `json:"text_scheme"`
	PointerSpeeds      []float64       `json:"pointer_speeds"`  // px/s at full deflection, one per slot
	PointerSlot        *int            `json:"pointer_slot"`    // index into PointerSpeeds; default = middle
	DwellSelectMs      int             `json:"dwell_select_ms"` // grid: select after resting this long (0 = circle only)
	AutoApplyThreshold float64         `json:"auto_apply_threshold"`
	// AdaptiveTiming lets the engine tune gap_ms to the person's own pace
	// (on unless set to false).
	AdaptiveTiming *bool `json:"adaptive_timing,omitempty"`
}

// LearnsTiming reports whether gap_ms adapts to the person.
func (s ProfileSettings) LearnsTiming() bool { return s.AdaptiveTiming == nil || *s.AdaptiveTiming }

// Normalize fills in defaults.
func (s ProfileSettings) Normalize() ProfileSettings {
	s.Signal = s.Signal.Normalize()
	if s.GapMs <= 0 {
		s.GapMs = 500
	}
	if s.CornerGapMs <= 0 {
		s.CornerGapMs = 300
	}
	if s.NavStyle != NavPointer {
		s.NavStyle = NavGesture
	}
	switch s.TextScheme {
	case SchemeTwoDir, SchemeGrid:
	default:
		s.TextScheme = SchemeCorners
	}
	if len(s.PointerSpeeds) == 0 {
		s.PointerSpeeds = []float64{150, 500, 1200} // precise, normal, fast
	}
	if s.PointerSlot == nil || *s.PointerSlot < 0 || *s.PointerSlot >= len(s.PointerSpeeds) {
		mid := len(s.PointerSpeeds) / 2
		s.PointerSlot = &mid
	}
	if s.AutoApplyThreshold <= 0 || s.AutoApplyThreshold > 1 {
		s.AutoApplyThreshold = 0.9
	}
	return s
}

// Context returns the session context for a mode.
func (s ProfileSettings) Context(mode string) string {
	if mode == ModeText {
		return ModeText + "." + s.TextScheme
	}
	return ModeNav + "." + s.NavStyle
}

// ContextMatches reports whether a pattern mode applies to a context.
// "any" matches everything, "text" matches "text.corners", and so on.
func ContextMatches(patternMode, context string) bool {
	return patternMode == "any" || patternMode == context || strings.HasPrefix(context, patternMode+".")
}

type Profile struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	InputMethod string          `json:"input_method"` // mouth | thumb
	Language    string          `json:"language"`
	AIEnabled   bool            `json:"ai_enabled"`
	Settings    ProfileSettings `json:"settings"`
	// Live state of the person, shared by all their devices. Read-only in
	// profile updates; changed through the mode and primary endpoints.
	Mode          string    `json:"mode"`
	Paused        bool      `json:"paused"`
	PrimaryClient *string   `json:"primary_client"` // the client that acts on the screen
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Languages a profile can use: what the person writes and hears, and the
// language of the panel, the app and the dashboard while it is selected.
var Languages = []string{"en", "ro"}

// LanguageName is the English name of a language code, for prompts.
func LanguageName(code string) string {
	switch code {
	case "ro":
		return "Romanian (ro)"
	case "en", "":
		return "English (en)"
	}
	return code
}

// Device kinds: a joystick gives input, a client (Android) shows the panel
// and acts on its screen.
const (
	KindJoystick = "joystick"
	KindClient   = "client"
)

type Device struct {
	ID         string     `json:"id"`
	ProfileID  *string    `json:"profile_id"`
	Name       string     `json:"name"`
	FWVersion  string     `json:"fw_version"`
	Kind       string     `json:"kind"`   // joystick | client
	Status     string     `json:"status"` // online | offline | unknown
	LastSeenAt *time.Time `json:"last_seen_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Action struct {
	Key      string          `json:"key"`
	Label    string          `json:"label"`
	Category string          `json:"category"`
	Params   json.RawMessage `json:"params,omitempty"` // default parameters
	Builtin  bool            `json:"builtin"`
}

type Pattern struct {
	ID           string          `json:"id"`
	ProfileID    *string         `json:"profile_id"` // nil = global
	Mode         string          `json:"mode"`       // any | nav | nav.pointer | text | text.corners | ...
	Sequence     string          `json:"sequence"`   // "NW NE" or "CW"
	OutputKind   string          `json:"output_kind"`
	OutputValue  string          `json:"output_value"`
	OutputParams json.RawMessage `json:"output_params,omitempty"`
	Source       string          `json:"source"` // builtin | manual | recorded | ai_promoted
	Enabled      bool            `json:"enabled"`
	CreatedAt    time.Time       `json:"created_at"`
}

type Phrase struct {
	ID        string  `json:"id"`
	ProfileID *string `json:"profile_id"`
	Text      string  `json:"text"`
	SortOrder int     `json:"sort_order"`
	Language  *string `json:"language"` // shared phrases: the language they are for; nil = all
}

// Candidate is one ranked AI proposal.
type Candidate struct {
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason,omitempty"`
}

// Suggestion statuses.
const (
	SugPending     = "pending"
	SugAccepted    = "accepted"
	SugRejected    = "rejected"
	SugAutoApplied = "auto_applied"
	SugExpired     = "expired"
	SugEdited      = "edited" // a correction put back in the message to change it
)

// Suggestion kinds.
const (
	SugInterpret = "interpret" // unmatched gesture -> action / character
	SugCorrect   = "correct"   // composed text -> corrected text
)

type Suggestion struct {
	ID         string      `json:"id"`
	DeviceID   string      `json:"device_id"`
	ProfileID  *string     `json:"profile_id"`
	Kind       string      `json:"kind"`
	Context    string      `json:"context"`
	Input      string      `json:"input"` // gesture sequence or composed text
	Candidates []Candidate `json:"candidates"`
	Model      string      `json:"model"`
	Status     string      `json:"status"`
	Chosen     string      `json:"chosen"`
	PatternID  *string     `json:"pattern_id"` // set once promoted to a pattern
	CreatedAt  time.Time   `json:"created_at"`
	ResolvedAt *time.Time  `json:"resolved_at"`
}

type Utterance struct {
	ID        string    `json:"id"`
	DeviceID  string    `json:"device_id"`
	ProfileID *string   `json:"profile_id"`
	Composed  string    `json:"composed"`
	Final     string    `json:"final"`
	Corrected bool      `json:"corrected"`
	AudioFile string    `json:"audio_file"`
	CreatedAt time.Time `json:"created_at"`
}

type Recording struct {
	ID        string     `json:"id"`
	DeviceID  string     `json:"device_id"`
	ProfileID *string    `json:"profile_id"`
	Label     string     `json:"label"`
	Status    string     `json:"status"` // recording | done
	Gestures  []string   `json:"gestures"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

// CtlAssistLook asks the person's screen client to capture the screen and
// request proposals from the screen assistant.
const (
	CtlAssistLook = "assist.look"
	CtlAssistMore = "assist.more" // more proposals for the screen being read
)

// Memory kinds.
const (
	MemScreen = "screen" // what the screen assistant saw
	MemAction = "action" // what was done on screen
	MemText   = "text"   // what the person said
	MemNote   = "note"   // added by a carer or a client
)

// Memory is one entry of a person's running context. Recent entries are
// shown to Claude so it can read screens, gestures and messages in light of
// what happened just before.
type Memory struct {
	ID        string    `json:"id"`
	DeviceID  string    `json:"device_id"`
	ProfileID *string   `json:"profile_id"`
	Kind      string    `json:"kind"`
	App       string    `json:"app"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// ScreenNode is one element of the accessibility tree a screen client
// reports alongside a screenshot. Bounds are screen pixels [l, t, r, b].
type ScreenNode struct {
	ID        int    `json:"id"`
	Text      string `json:"text,omitempty"`
	Desc      string `json:"desc,omitempty"`
	Class     string `json:"class,omitempty"`
	Bounds    [4]int `json:"bounds"`
	Clickable bool   `json:"clickable,omitempty"`
	Editable  bool   `json:"editable,omitempty"`
	Scrolls   bool   `json:"scrollable,omitempty"`
}

// Proposal kinds.
const (
	PropTap       = "tap"
	PropLongPress = "long_press"
	PropType      = "type"
	PropScroll    = "scroll"
	PropAction    = "action"
)

// Proposal is one thing the screen assistant offers to do, bound to the
// gesture that performs it.
type Proposal struct {
	Index      int     `json:"index"`
	Label      string  `json:"label"`
	Kind       string  `json:"kind"`
	X          int     `json:"x,omitempty"` // screen pixels
	Y          int     `json:"y,omitempty"`
	Text       string  `json:"text,omitempty"`      // for type
	Direction  string  `json:"direction,omitempty"` // for scroll: up | down | left | right
	ActionKey  string  `json:"action_key,omitempty"`
	Sequence   string  `json:"sequence"`           // gesture that performs it
	Existing   bool    `json:"existing"`           // the gesture is a stored pattern
	Shortcut   bool    `json:"shortcut,omitempty"` // a stored gesture suggested for this screen
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason,omitempty"`
}

// Assist statuses.
const (
	AssistActive    = "active"
	AssistChosen    = "chosen"
	AssistDismissed = "dismissed"
	AssistExpired   = "expired"
)

// Assist is one screen reading with its proposals.
type Assist struct {
	ID         string     `json:"id"`
	DeviceID   string     `json:"device_id"`
	ProfileID  *string    `json:"profile_id"`
	App        string     `json:"app"`
	Summary    string     `json:"summary"`
	Proposals  []Proposal `json:"proposals"`
	Model      string     `json:"model"`
	Status     string     `json:"status"`
	Chosen     *int       `json:"chosen"`
	Width      int        `json:"width"`
	Height     int        `json:"height"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
	// Auto marks a reading the client made on its own, not asked for. Not
	// stored; clients use it to back off where readings go unused.
	Auto bool `json:"auto,omitempty"`
}

// HiddenPrefix starts a note that hides a proposal in one app for good.
// Deleting the note (Memory page) brings the proposal back.
const HiddenPrefix = "Do not suggest: "
