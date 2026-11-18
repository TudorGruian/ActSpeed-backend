package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/actspeed/actspeed-backend/internal/model"
)

// ClaudeConfig selects models and limits.
type ClaudeConfig struct {
	APIKey         string // empty = let the SDK resolve credentials from the environment
	InterpretModel string
	CorrectModel   string
	AssistModel    string
	Timeout        time.Duration
	AssistTimeout  time.Duration // screen readings carry an image and take longer
}

// Claude implements Interpreter with the Anthropic API.
type Claude struct {
	client anthropic.Client
	cfg    ClaudeConfig
}

// NewClaude builds the interpreter.
func NewClaude(cfg ClaudeConfig) *Claude {
	var opts []option.RequestOption
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	// One retry at most: a late answer is useless to someone waiting on a gesture.
	opts = append(opts, option.WithMaxRetries(1))
	return &Claude{client: anthropic.NewClient(opts...), cfg: cfg}
}

func (c *Claude) Enabled() bool { return true }

const interpretSystem = `You help interpret input from ActSpeed, an assistive joystick used by a person with a motor disability, operated with the mouth or a thumb.

The joystick produces gestures: sequences of tokens. Direction tokens are N, NE, E, SE, S, SW, W, NW. A trailing "+" means the direction was held.

A gesture did not match any stored pattern exactly. This usually means a slip: an extra or missing token, a neighbouring direction, a hold that was meant as a flick, or a tremor. Work out which allowed outputs the person most plausibly intended, using the known patterns and the recent context.

Rules:
- Only answer with values from the allowed vocabulary, copied exactly.
- Return at most three candidates, best first, each with a confidence between 0 and 1 that reflects real uncertainty. Use high confidence only when one known pattern is a clear near-miss and no other is comparably close.
- If nothing is plausible, return an empty list. A wrong action costs the person more than no action.
- Keep each reason to a few words.`

const correctSystem = `You tidy up short messages typed by a person using ActSpeed, an assistive joystick. Typing is slow and effortful, so messages contain abbreviations, missing letters, missing spaces and no punctuation. The message will be spoken aloud by a text-to-speech voice on the person's behalf.

Rewrite the message as the person most plausibly meant it: expand abbreviations, fix spelling, add capitalisation and punctuation. Keep their words, tone and meaning. Do not add content, politeness or detail they did not write, and do not answer the message. If you are unsure what a fragment means, leave it as typed. If the message is already fine, return it unchanged.

Write in the language of the message. In "notes", say in a few words what you changed, or leave it empty.`

var interpretSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"candidates": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value":      map[string]any{"type": "string"},
					"confidence": map[string]any{"type": "number"},
					"reason":     map[string]any{"type": "string"},
				},
				"required":             []string{"value", "confidence", "reason"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"candidates"},
	"additionalProperties": false,
}

var correctSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"text":  map[string]any{"type": "string"},
		"notes": map[string]any{"type": "string"},
	},
	"required":             []string{"text", "notes"},
	"additionalProperties": false,
}

// call sends one structured-output request. The stable instructions and the
// vocabulary go first and are marked cacheable; the varying input goes last.
func (c *Claude) call(ctx context.Context, modelID, system, stable, input string, maxTokens int64, schema map[string]any, out any) error {
	return c.callBlocks(ctx, c.cfg.Timeout, modelID, system, stable,
		[]anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(input)}, maxTokens, schema, out)
}

func (c *Claude) callBlocks(ctx context.Context, timeout time.Duration, modelID, system, stable string,
	input []anthropic.BetaContentBlockParamUnion, maxTokens int64, schema map[string]any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sys := []anthropic.BetaTextBlockParam{{Text: system}}
	if stable != "" {
		sys = append(sys, anthropic.BetaTextBlockParam{Text: stable})
	}
	sys[len(sys)-1].CacheControl = anthropic.NewBetaCacheControlEphemeralParam()

	resp, err := c.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
		Model:     modelID,
		MaxTokens: maxTokens,
		System:    sys,
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(input...),
		},
		OutputConfig: anthropic.BetaOutputConfigParam{
			// Low effort: these are small, latency-sensitive judgements.
			Effort: anthropic.BetaOutputConfigEffortLow,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: schema},
		},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return fmt.Errorf("claude api status %d: %w", apiErr.StatusCode, err)
		}
		return fmt.Errorf("claude request: %w", err)
	}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return errors.New("claude declined the request")
	}
	if resp.StopReason == anthropic.BetaStopReasonMaxTokens {
		return errors.New("claude response truncated")
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if tb, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(tb.Text)
		}
	}
	if err := json.Unmarshal([]byte(text.String()), out); err != nil {
		return fmt.Errorf("claude returned invalid JSON: %w", err)
	}
	return nil
}

func (c *Claude) Interpret(ctx context.Context, r InterpretRequest) ([]model.Candidate, string, error) {
	vocab, _ := json.Marshal(r.Vocabulary)
	known, _ := json.Marshal(r.Known)
	stable := fmt.Sprintf("Input context: %s\n\nAllowed vocabulary (JSON):\n%s\n\nKnown patterns in this context (JSON):\n%s",
		r.Context, vocab, known)
	input := fmt.Sprintf("Unmatched gesture: %s\nRecent context: %q\nLanguage: %s", r.Sequence, r.Recent, model.LanguageName(r.Language)) +
		memoryText(r.Memory)

	var out struct {
		Candidates []model.Candidate `json:"candidates"`
	}
	if err := c.call(ctx, c.cfg.InterpretModel, interpretSystem, stable, input, 512, interpretSchema, &out); err != nil {
		return nil, c.cfg.InterpretModel, err
	}
	return FilterToVocabulary(out.Candidates, r.Vocabulary), c.cfg.InterpretModel, nil
}

func (c *Claude) Correct(ctx context.Context, r CorrectRequest) (CorrectResult, string, error) {
	input := fmt.Sprintf("Language hint: %s\nMessage as typed:\n%s", model.LanguageName(r.Language), r.Text) + memoryText(r.Memory)
	var out CorrectResult
	if err := c.call(ctx, c.cfg.CorrectModel, correctSystem, "", input, 4096, correctSchema, &out); err != nil {
		return CorrectResult{Text: r.Text}, c.cfg.CorrectModel, err
	}
	if strings.TrimSpace(out.Text) == "" {
		out.Text = r.Text
	}
	return out, c.cfg.CorrectModel, nil
}

const completeSystem = `You help a person who types slowly with an assistive joystick (ActSpeed) finish what they are writing. Every letter costs them effort, so a good guess of the whole message saves a lot.

Given what they have typed so far, propose up to three complete messages they most plausibly mean. Each must begin with the typed words, completing the last word if it is unfinished. If the typed text looks like abbreviations or word-initial letters (for example "hay" for "how are you", or "wt" for "water"), expand it instead. Prefer short, everyday messages in their own voice, informed by their recent messages and context. Most likely first, all different, each at most 12 words. Do not answer the message, add politeness they did not write, or invent specific facts such as names or times that nothing hints at.

Write in the language given by the language hint, unless the typed text is clearly in another language. Short fragments are not clear evidence of another language.`

var completeSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"phrases": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
	"required":             []string{"phrases"},
	"additionalProperties": false,
}

// Complete proposes whole messages for what has been typed. It uses the
// fast interpretation model: suggestions must arrive while the person types.
func (c *Claude) Complete(ctx context.Context, r CompleteRequest) ([]string, string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Language hint: %s\n", model.LanguageName(r.Language))
	if r.App != "" {
		fmt.Fprintf(&b, "They are typing into the app %s.\n", r.App)
	} else {
		b.WriteString("The message will be spoken aloud for them.\n")
	}
	if len(r.Recent) > 0 {
		b.WriteString("Their recent messages, newest first:\n")
		for _, t := range r.Recent {
			b.WriteString("- " + t + "\n")
		}
	}
	fmt.Fprintf(&b, "Typed so far:\n%s", r.Text)
	b.WriteString(memoryText(r.Memory))
	var out struct {
		Phrases []string `json:"phrases"`
	}
	if err := c.call(ctx, c.cfg.InterpretModel, completeSystem, "", b.String(), 512, completeSchema, &out); err != nil {
		return nil, c.cfg.InterpretModel, err
	}
	var phrases []string
	for _, p := range out.Phrases {
		if p = strings.TrimSpace(p); p != "" && len(phrases) < 3 {
			phrases = append(phrases, p)
		}
	}
	return phrases, c.cfg.InterpretModel, nil
}

// memoryText renders the person's recent context, oldest first.
func memoryText(memory []string) string {
	if len(memory) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nRecent context from this person's session, oldest first (use it only to resolve ambiguity):\n")
	for _, m := range memory {
		b.WriteString("- ")
		b.WriteString(m)
		b.WriteString("\n")
	}
	return b.String()
}
