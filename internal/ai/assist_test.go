package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/actspeed/actspeed-backend/internal/model"
)

func TestAssistSendsScreenshotAndElements(t *testing.T) {
	c, body, _ := stub(t, `{"summary":"Settings","memory":"","proposals":[
		{"label":"Open Wi-Fi","kind":"tap","node_id":3,"x":-1,"y":-1,"text":"","direction":"","action_key":"","confidence":0.7,"reason":"top item"}]}`, "end_turn")

	res, modelID, err := c.Assist(context.Background(), AssistRequest{
		Image: []byte{0xff, 0xd8, 0xff}, MIME: "image/jpeg", ImageWidth: 540, ImageHeight: 1200,
		ScreenWidth: 1080, ScreenHeight: 2400, App: "com.android.settings",
		Nodes:  []model.ScreenNode{{ID: 3, Text: "Network & internet", Bounds: [4]int{0, 300, 1080, 420}, Clickable: true}},
		Memory: []string{"[10:00 text] Said: turn on wifi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if modelID != "claude-haiku-5-5" || body == nil || (*body)["model"] != "claude-haiku-5-5" {
		t.Errorf("model = %s / %v", modelID, (*body)["model"])
	}
	if len(res.Proposals) != 1 || res.Proposals[0].NodeID != 3 {
		t.Errorf("proposals = %+v", res.Proposals)
	}
	content := (*body)["messages"].([]any)[0].(map[string]any)["content"].([]any)
	img := content[0].(map[string]any)
	if img["type"] != "image" || img["source"].(map[string]any)["media_type"] != "image/jpeg" {
		t.Errorf("first block should be the screenshot: %v", img)
	}
	text := content[1].(map[string]any)["text"].(string)
	for _, want := range []string{"com.android.settings", "Network & internet", "Said: turn on wifi", `"f":"c"`} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt is missing %q:\n%s", want, text)
		}
	}
}
