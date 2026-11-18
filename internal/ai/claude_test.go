package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stub serves a canned Messages API response and captures the request.
func stub(t *testing.T, replyText, stopReason string) (*Claude, *map[string]any, *http.Header) {
	t.Helper()
	body := map[string]any{}
	header := http.Header{}
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		header = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_test", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
			"stop_reason": stopReason,
			"content": []map[string]any{
				{"type": "thinking", "thinking": "", "signature": "sig"},
				{"type": "text", "text": replyText},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 10},
		})
		return w.Result(), nil
	})
	c := &Claude{
		client: anthropic.NewClient(option.WithAPIKey("test-key"), option.WithHTTPClient(&http.Client{Transport: transport}), option.WithMaxRetries(0)),
		cfg:    ClaudeConfig{InterpretModel: "claude-haiku-5-5", CorrectModel: "claude-sonnet-5-5", AssistModel: "claude-haiku-5-5", Timeout: 5 * time.Second},
	}
	return c, &body, &header
}

func TestInterpretRequestShapeAndVocabularyFilter(t *testing.T) {
	c, body, _ := stub(t, `{"candidates":[
		{"value":"key.up","confidence":0.7,"reason":"near miss"},
		{"value":"rm.everything","confidence":0.99,"reason":"invented"}]}`, "end_turn")

	cands, modelID, err := c.Interpret(context.Background(), InterpretRequest{
		Context: "nav.gesture", Sequence: "N W",
		Vocabulary: []VocabItem{{Value: "key.up", Label: "Arrow up"}},
		Known:      []KnownPattern{{Sequence: "N", Value: "key.up"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if modelID != "claude-haiku-5-5" || len(cands) != 1 || cands[0].Value != "key.up" {
		t.Fatalf("model = %s, candidates = %+v", modelID, cands)
	}

	b := *body
	if b["model"] != "claude-haiku-5-5" {
		t.Errorf("model = %v", b["model"])
	}
	oc, _ := b["output_config"].(map[string]any)
	if oc["effort"] != "low" {
		t.Errorf("effort = %v", oc["effort"])
	}
	format, _ := oc["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"] == nil {
		t.Errorf("format = %v", format)
	}
	if _, ok := b["fallbacks"]; ok {
		t.Errorf("fallbacks = %v", b["fallbacks"])
	}
	for _, banned := range []string{"temperature", "top_p", "tool_choice", "thinking"} {
		if _, ok := b[banned]; ok {
			t.Errorf("request must not send %q", banned)
		}
	}
	system, _ := b["system"].([]any)
	if len(system) != 2 {
		t.Fatalf("system blocks = %d, want 2", len(system))
	}
	last, _ := system[1].(map[string]any)
	if last["cache_control"] == nil {
		t.Errorf("vocabulary block is not marked cacheable")
	}
}

func TestCorrectUsesItsOwnModelAndKeepsTextOnEmptyReply(t *testing.T) {
	c, body, _ := stub(t, `{"text":"How are you?","notes":"expanded"}`, "end_turn")
	res, modelID, err := c.Correct(context.Background(), CorrectRequest{Text: "hw r u", Language: "en"})
	if err != nil || res.Text != "How are you?" || modelID != "claude-sonnet-5-5" {
		t.Fatalf("res = %+v, model = %s, err = %v", res, modelID, err)
	}
	if (*body)["model"] != "claude-sonnet-5-5" {
		t.Errorf("model = %v", (*body)["model"])
	}

	c, _, _ = stub(t, `{"text":"  ","notes":""}`, "end_turn")
	if res, _, _ := c.Correct(context.Background(), CorrectRequest{Text: "hello"}); res.Text != "hello" {
		t.Errorf("empty reply replaced the message: %q", res.Text)
	}
}

func TestRefusalAndBadJSONAreErrors(t *testing.T) {
	c, _, _ := stub(t, "", "refusal")
	if _, _, err := c.Correct(context.Background(), CorrectRequest{Text: "hello"}); err == nil {
		t.Error("refusal should be an error")
	}
	c, _, _ = stub(t, "not json", "end_turn")
	if res, _, err := c.Correct(context.Background(), CorrectRequest{Text: "hello"}); err == nil || res.Text != "hello" {
		t.Errorf("bad JSON: res = %+v, err = %v", res, err)
	}
}
