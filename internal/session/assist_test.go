package session

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/actspeed/actspeed-backend/internal/ai"
	"github.com/actspeed/actspeed-backend/internal/model"
	"github.com/actspeed/actspeed-backend/internal/signal"
)

func screen() AssistInput {
	return AssistInput{
		Image: []byte{0xff, 0xd8}, MIME: "image/jpeg", ImageWidth: 540, ImageHeight: 1200,
		ScreenWidth: 1080, ScreenHeight: 2400, App: "com.example.chat",
		Nodes: []model.ScreenNode{
			{ID: 1, Text: "Send", Bounds: [4]int{900, 2200, 1060, 2300}, Clickable: true},
			{ID: 2, Text: "Message", Bounds: [4]int{20, 2200, 880, 2300}, Editable: true},
		},
	}
}

func assistRig(t *testing.T, settings model.ProfileSettings) (*rig, *fakeAI) {
	fake := &fakeAI{assist: ai.AssistResult{
		Summary: "A chat with Ana.", Memory: "Chatting with Ana.",
		Proposals: []ai.AssistProposal{
			{Label: "Tap Send", Kind: "tap", NodeID: 1, X: -1, Y: -1, Confidence: 0.8},
			{Label: "Type the composed message", Kind: "type", NodeID: 2, X: -1, Y: -1, Text: "hi", Confidence: 0.6},
			{Label: "Go back", Kind: "action", NodeID: -1, X: -1, Y: -1, ActionKey: "system.back", Confidence: 0.3},
			{Label: "Tap the logo", Kind: "tap", NodeID: -1, X: 100, Y: 50, Confidence: 0.2},
			{Label: "Invented action", Kind: "action", NodeID: -1, ActionKey: "rm.rf", Confidence: 0.9},
			{Label: "Tap in screen pixels", Kind: "tap", NodeID: -1, X: 900, Y: 1500, Confidence: 0.1},
			{Label: "Tap nothing", Kind: "tap", NodeID: 99, X: -1, Y: -1},
		},
	}}
	r := newRig(t, settings, fake)
	r.sample(0, 0, 0, 20) // register the device
	return r, fake
}

func TestAssistBindsProposalsToGestures(t *testing.T) {
	r, fake := assistRig(t, model.ProfileSettings{NavStyle: model.NavGesture})
	a, err := r.m.Assist(context.Background(), "p1", screen())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Proposals) != 5 {
		t.Fatalf("proposals = %+v, want 5 (invalid ones dropped)", a.Proposals)
	}
	if p := a.Proposals[4]; p.X != 900 || p.Y != 1500 {
		t.Errorf("coordinates outside the screenshot are screen pixels: %+v", p)
	}
	send, back, logo := a.Proposals[0], a.Proposals[2], a.Proposals[3]
	if send.X != 980 || send.Y != 2250 || send.Sequence != "N E" || send.Existing {
		t.Errorf("send = %+v", send)
	}
	if back.Sequence != "W+" || !back.Existing {
		t.Errorf("back should show the stored gesture: %+v", back)
	}
	if logo.X != 200 || logo.Y != 100 {
		t.Errorf("screenshot coordinates should scale to the screen: %+v", logo)
	}
	if len(fake.lastAsk.Gestures) == 0 || len(fake.lastAsk.Actions) == 0 {
		t.Error("the stored gestures and actions should be shown to Claude")
	}
	if r.state().Assist == nil {
		t.Error("state should carry the active reading")
	}

	r.play("N E |")
	var tapped map[string]any
	for _, d := range r.out.of("action") {
		if m := d.(map[string]any); m["key"] == "screen.tap" {
			tapped = m["params"].(map[string]any)
		}
	}
	if tapped == nil || tapped["x"] != 980 {
		t.Fatalf("gesture N E should tap Send, actions = %v", r.out.actions())
	}
	if strings.Contains(strings.Join(r.out.actions(), ","), "key.up") {
		t.Error("the proposal gesture must not also run the stored N flick")
	}
	if r.state().Assist != nil || r.store.assists[0].Status != model.AssistChosen {
		t.Error("the reading should close once a proposal is chosen")
	}
	var kinds []string
	for _, m := range r.store.memories {
		kinds = append(kinds, m.Kind+":"+m.Content)
	}
	slices.Sort(kinds) // written in the background, in any order
	if got := strings.Join(kinds, "|"); got != "action:Chose: Tap Send|screen:Chatting with Ana." {
		t.Errorf("memories = %s", got)
	}
}

func TestAssistKeepsStoredGesturesWorking(t *testing.T) {
	r, _ := assistRig(t, model.ProfileSettings{NavStyle: model.NavGesture})
	if _, err := r.m.Assist(context.Background(), "p1", screen()); err != nil {
		t.Fatal(err)
	}
	r.play("N | W+ |")
	if got := strings.Join(r.out.actions(), ","); got != "key.up,system.back" {
		t.Errorf("actions = %s", got)
	}
	if a := r.store.assists[0]; a.Status != model.AssistChosen || a.Chosen == nil || *a.Chosen != 2 {
		t.Errorf("using a proposed stored gesture should answer the reading: %+v", a)
	}
}

func TestAssistUsesDiagonalsWhenEnabled(t *testing.T) {
	r, _ := assistRig(t, model.ProfileSettings{NavStyle: model.NavGesture, Signal: signal.Settings{Diagonals: true}})
	a, err := r.m.Assist(context.Background(), "p1", screen())
	if err != nil {
		t.Fatal(err)
	}
	if a.Proposals[0].Sequence != "NE" || a.Proposals[1].Sequence != "NW" {
		t.Errorf("sequences = %s, %s", a.Proposals[0].Sequence, a.Proposals[1].Sequence)
	}
	r.play("NW |")
	found := false
	for _, d := range r.out.of("action") {
		if m := d.(map[string]any); m["key"] == "screen.type" {
			found = m["params"].(map[string]any)["text"] == "hi"
		}
	}
	if !found {
		t.Errorf("NW should type into the field, actions = %v", r.out.actions())
	}
}

func TestLookGestureRequestsScreen(t *testing.T) {
	r := newRig(t, model.ProfileSettings{NavStyle: model.NavGesture}, nil)
	r.play("N S |")
	if len(r.out.of("assist_request")) != 1 {
		t.Errorf("N S should ask for a screen reading; actions = %v", r.out.actions())
	}
}

func TestAssistNeedsAI(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	if _, err := r.m.Assist(context.Background(), "p1", screen()); err != ErrAIDisabled {
		t.Errorf("err = %v, want ErrAIDisabled", err)
	}
}

func TestSpokenTextBecomesMemoryForCorrections(t *testing.T) {
	fake := &fakeAI{corrected: "How are you?"}
	r := newRig(t, model.ProfileSettings{TextScheme: model.SchemeCorners}, fake)
	r.sample(0, 0, 0, 20)
	ctx := context.Background()
	if err := r.m.Speak(ctx, "p1", "unused"); err != nil {
		t.Fatal(err)
	}
	s, _ := r.m.get(ctx, "p1")
	s.mu.Lock()
	s.mode, s.buffer = model.ModeText, "hello"
	s.commit(false)
	s.mu.Unlock()
	r.m.Wait()
	s.mu.Lock()
	s.buffer = "hw r u"
	s.commit(true)
	s.mu.Unlock()
	r.m.Wait()
	if len(fake.lastFix.Memory) == 0 || !strings.Contains(fake.lastFix.Memory[len(fake.lastFix.Memory)-1], "Said: hello") {
		t.Errorf("correction memory = %v", fake.lastFix.Memory)
	}
}

func TestMoreKeepsGesturesAndAddsNewOnes(t *testing.T) {
	r, fake := assistRig(t, model.ProfileSettings{NavStyle: model.NavGesture})
	first, err := r.m.Assist(context.Background(), "p1", screen())
	if err != nil {
		t.Fatal(err)
	}
	fake.assist = ai.AssistResult{Summary: "A chat.", Proposals: []ai.AssistProposal{
		{Label: "Tap Send", Kind: "tap", NodeID: 1, X: -1, Y: -1}, // repeat: dropped
		{Label: "Scroll up", Kind: "scroll", NodeID: -1, X: -1, Y: -1, Direction: "up"},
	}}
	r.play("CW CCW |") // the "more" gesture
	if len(fake.lastAsk.Offered) == 0 {
		t.Fatalf("more should tell Claude what was offered; calls = %d", fake.calls)
	}
	a := r.state().Assist
	if a == nil {
		t.Fatal("reading should stay active")
	}
	for i, p := range first.Proposals {
		if a.Proposals[i].Sequence != p.Sequence || a.Proposals[i].Label != p.Label {
			t.Errorf("proposal %d moved: %+v -> %+v", i, p, a.Proposals[i])
		}
	}
	last := a.Proposals[len(a.Proposals)-1]
	if last.Label != "Scroll up" || last.Sequence == "" || last.Index != len(a.Proposals)-1 {
		t.Errorf("new proposal = %+v", last)
	}
	n := 0
	for _, p := range a.Proposals {
		if p.Label == "Tap Send" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("repeats should be dropped: %+v", a.Proposals)
	}
}

func TestMoreWithoutReadingLooks(t *testing.T) {
	r := newRig(t, model.ProfileSettings{NavStyle: model.NavGesture}, &fakeAI{})
	r.play("CW CCW |")
	if len(r.out.of("assist_request")) != 1 {
		t.Errorf("more with nothing on screen should read the screen")
	}
}

func TestShortcutsShowStoredGestures(t *testing.T) {
	r, fake := assistRig(t, model.ProfileSettings{NavStyle: model.NavGesture})
	fake.assist.Shortcuts = []ai.AssistShortcut{
		{ActionKey: "system.home", Label: "Home"},
		{ActionKey: "system.back", Label: "Back"}, // already a proposal
		{ActionKey: "rm.rf", Label: "Nope"},       // not stored
	}
	a, err := r.m.Assist(context.Background(), "p1", screen())
	if err != nil {
		t.Fatal(err)
	}
	var sc []model.Proposal
	for _, p := range a.Proposals {
		if p.Shortcut {
			sc = append(sc, p)
		}
	}
	if len(sc) != 1 || sc[0].ActionKey != "system.home" || sc[0].Sequence != "N+" || !sc[0].Existing {
		t.Fatalf("shortcuts = %+v", sc)
	}
	r.play("N+ |")
	if a := r.store.assists[0]; a.Status != model.AssistChosen {
		t.Errorf("using a shortcut should answer the reading: %+v", a)
	}
}

func TestFieldTypesInsteadOfSpeaking(t *testing.T) {
	r := newRig(t, model.ProfileSettings{NavStyle: model.NavGesture, TextScheme: model.SchemeCorners}, nil)
	r.sample(0, 0, 0, 20)
	ctx := context.Background()
	st, err := r.m.SetField(ctx, "p1", Field{Active: true, App: "com.android.chrome"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != model.ModeText || st.Field == nil {
		t.Fatalf("an open field should switch to text mode: %+v", st)
	}
	s, _ := r.m.get(ctx, "p1")
	s.mu.Lock()
	s.buffer = "weather"
	s.commit(false)
	s.mu.Unlock()
	r.m.Wait()
	if len(r.out.of("speak")) != 0 {
		t.Error("typing into a field must not speak aloud")
	}
	got := r.out.of("commit_text")
	if len(got) != 1 || got[0].(map[string]any)["text"] != "weather" {
		t.Fatalf("commit_text = %v", got)
	}
	if st := r.state(); st.Buffer != "" {
		t.Errorf("buffer should clear, got %q", st.Buffer)
	}
	st, _ = r.m.SetField(ctx, "p1", Field{Active: false})
	if st.Mode != model.ModeNav || st.Field != nil {
		t.Errorf("closing the field should return to navigation: %+v", st)
	}
}

func TestHistoryRanksUsualChoicesFirst(t *testing.T) {
	r, fake := assistRig(t, model.ProfileSettings{NavStyle: model.NavGesture})
	ctx := context.Background()
	for range 3 {
		a, err := r.m.Assist(ctx, "p1", screen())
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(a.Proposals, func(p model.Proposal) bool { return p.Label == "Type the composed message" })
		if err := r.m.ChooseProposal(ctx, "p1", a.ID, i); err != nil {
			t.Fatal(err)
		}
		r.m.Wait()
	}
	a, err := r.m.Assist(ctx, "p1", screen())
	if err != nil {
		t.Fatal(err)
	}
	if a.Proposals[0].Label != "Type the composed message" || a.Proposals[0].Sequence != "N E" {
		t.Errorf("usual choice should come first with the easiest gesture: %+v", a.Proposals[0])
	}
	if len(fake.lastAsk.Habits) == 0 || !strings.Contains(fake.lastAsk.Habits[0], "Type the composed message (3×)") {
		t.Errorf("habits = %v", fake.lastAsk.Habits)
	}
}

func TestHiddenProposalStaysHidden(t *testing.T) {
	r, fake := assistRig(t, model.ProfileSettings{NavStyle: model.NavGesture})
	ctx := context.Background()
	a, err := r.m.Assist(ctx, "p1", screen())
	if err != nil {
		t.Fatal(err)
	}
	send := a.Proposals[0]
	after, err := r.m.HideProposal(ctx, "p1", a.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	r.m.Wait()
	if after.Proposals[0].Label == send.Label || after.Proposals[0].Sequence != a.Proposals[1].Sequence {
		t.Errorf("hiding should remove the proposal and keep the others' gestures: %+v", after.Proposals)
	}
	again, err := r.m.Assist(ctx, "p1", screen())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range again.Proposals {
		if p.Label == send.Label {
			t.Errorf("hidden proposal came back: %+v", again.Proposals)
		}
	}
	if !slices.Contains(fake.lastAsk.Offered, send.Label) {
		t.Errorf("Claude should be told what is hidden: %v", fake.lastAsk.Offered)
	}
}

func TestCorrectionCanBeEdited(t *testing.T) {
	fake := &fakeAI{corrected: "How are you?"}
	r := newRig(t, model.ProfileSettings{TextScheme: model.SchemeCorners}, fake)
	r.sample(0, 0, 0, 20)
	ctx := context.Background()
	s, _ := r.m.get(ctx, "p1")
	s.mu.Lock()
	s.mode, s.buffer = model.ModeText, "hw r u"
	s.commit(true)
	s.mu.Unlock()
	r.m.Wait()
	if r.state().Pending == nil {
		t.Fatal("a correction should be waiting")
	}
	r.play("S |") // down flick: edit
	st := r.state()
	if st.Pending != nil || st.Buffer != "How are you?" {
		t.Errorf("edit should put the correction in the message: pending=%v buffer=%q", st.Pending, st.Buffer)
	}
	if len(r.out.of("speak")) != 0 {
		t.Error("editing must not speak")
	}
}

func TestTidyDropsStrayBrackets(t *testing.T) {
	for in, want := range map[string]string{
		" Ecranul Setări arată categorii.} ": "Ecranul Setări arată categorii.",
		"Deschide Apps]":                     "Deschide Apps",
		"Alege {nume}":                       "Alege {nume}",
	} {
		if got := tidy(in); got != want {
			t.Errorf("tidy(%q) = %q, want %q", in, got, want)
		}
	}
}
