// Package ai is the assistive interpreter. It never executes anything: it
// only proposes candidates, which the session engine checks against the
// allowed vocabulary and stores as suggestions for human review.
package ai

import (
	"context"
	"errors"

	"github.com/actspeed/actspeed-backend/internal/model"
)

// VocabItem is one allowed answer (an action key or a character).
type VocabItem struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// KnownPattern shows the model how existing gestures map to outputs.
type KnownPattern struct {
	Sequence string `json:"sequence"`
	Value    string `json:"value"`
}

// InterpretRequest asks what an unmatched gesture most likely meant.
type InterpretRequest struct {
	Context    string // e.g. "nav.gesture" or "text.corners"
	Sequence   string // e.g. "NE SE NW"
	Vocabulary []VocabItem
	Known      []KnownPattern
	Recent     string // text typed so far, or recent actions
	Memory     []string
	Language   string
}

// CorrectRequest asks for a cleaned-up version of a composed message.
type CorrectRequest struct {
	Text     string
	Memory   []string
	Language string
}

// CompleteRequest asks how a message being typed most likely continues.
type CompleteRequest struct {
	Text     string   // the message so far, possibly abbreviated
	Memory   []string // recent session notes
	Recent   []string // the person's recent messages, newest first
	Language string
	App      string // the app being typed into, empty when speaking
}

// AssistRequest asks what the person most likely wants to do on a screen.
type AssistRequest struct {
	Image        []byte // JPEG or PNG, already downscaled by the client
	MIME         string
	ImageWidth   int // pixels of Image
	ImageHeight  int
	ScreenWidth  int // pixels of the real screen, the space of node bounds
	ScreenHeight int
	App          string
	Title        string
	Nodes        []model.ScreenNode
	Actions      []VocabItem    // system actions Claude may propose
	Gestures     []KnownPattern // the person's stored navigation gestures
	Memory       []string       // oldest first
	Composed     string         // text the person is composing
	Goal         string         // optional request typed or spoken by the person
	Offered      []string       // labels already proposed for this screen ("more")
	Habits       []string       // what the person usually picks in this app, most often first
	Language     string
}

// AssistProposal is one proposed step, in screen pixels.
type AssistProposal struct {
	Label      string  `json:"label"`
	Kind       string  `json:"kind"`
	NodeID     int     `json:"node_id"`
	X          int     `json:"x"`
	Y          int     `json:"y"`
	Text       string  `json:"text"`
	Direction  string  `json:"direction"`
	ActionKey  string  `json:"action_key"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// AssistShortcut is one of the person's stored gestures worth using on
// the current screen.
type AssistShortcut struct {
	ActionKey string `json:"action_key"`
	Label     string `json:"label"`
}

// AssistResult is the reading of a screen.
type AssistResult struct {
	Summary   string           `json:"summary"`
	Memory    string           `json:"memory"`
	Proposals []AssistProposal `json:"proposals"`
	Shortcuts []AssistShortcut `json:"shortcuts"`
}

// CorrectResult is the proposed message.
type CorrectResult struct {
	Text  string `json:"text"`
	Notes string `json:"notes"`
}

// Interpreter is implemented by Claude and by test fakes.
type Interpreter interface {
	// Enabled reports whether calls can succeed at all.
	Enabled() bool
	Interpret(ctx context.Context, r InterpretRequest) (cands []model.Candidate, modelID string, err error)
	Correct(ctx context.Context, r CorrectRequest) (res CorrectResult, modelID string, err error)
	Assist(ctx context.Context, r AssistRequest) (res AssistResult, modelID string, err error)
	// Complete returns up to three whole messages that start with, or
	// expand, what has been typed so far.
	Complete(ctx context.Context, r CompleteRequest) (phrases []string, modelID string, err error)
}

// ErrDisabled is returned by Noop for calls that have no offline fallback.
var ErrDisabled = errors.New("ai is disabled")

// Noop is used when no API credentials are configured.
type Noop struct{}

func (Noop) Enabled() bool { return false }
func (Noop) Interpret(context.Context, InterpretRequest) ([]model.Candidate, string, error) {
	return nil, "", nil
}
func (Noop) Correct(_ context.Context, r CorrectRequest) (CorrectResult, string, error) {
	return CorrectResult{Text: r.Text}, "", nil
}
func (Noop) Assist(context.Context, AssistRequest) (AssistResult, string, error) {
	return AssistResult{}, "", ErrDisabled
}
func (Noop) Complete(context.Context, CompleteRequest) ([]string, string, error) {
	return nil, "", ErrDisabled
}

// FilterToVocabulary drops candidates the model was not allowed to return
// and sorts nothing: order is the model's ranking.
func FilterToVocabulary(cands []model.Candidate, vocab []VocabItem) []model.Candidate {
	allowed := make(map[string]bool, len(vocab))
	for _, v := range vocab {
		allowed[v.Value] = true
	}
	out := cands[:0:0]
	for _, c := range cands {
		if allowed[c.Value] && c.Confidence > 0 {
			if c.Confidence > 1 {
				c.Confidence = 1
			}
			out = append(out, c)
		}
	}
	return out
}
