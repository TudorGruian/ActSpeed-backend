package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/actspeed/actspeed-backend/internal/ai"
	"github.com/actspeed/actspeed-backend/internal/model"
)

const (
	maxProposals     = 5
	maxOnScreen      = 8 // proposals after asking for more
	maxShortcuts     = 3
	assistLifetimeMs = 3 * 60 * 1000
	memoryWindow     = 6 * time.Hour
	memoryPromptSize = 20
)

// Errors returned by the screen assistant.
var (
	ErrAIDisabled      = errors.New("the screen assistant needs Claude, which is off for this person")
	ErrNoActiveAssist  = errors.New("no active screen reading")
	ErrUnknownProposal = errors.New("no such proposal")
)

// proposalPool lists gestures offered for proposals, in order of effort.
// Diagonal flicks come first when the person has diagonals enabled.
var (
	diagonalPool = []string{"NE", "NW", "SE", "SW"}
	cardinalPool = []string{"N E", "N W", "S E", "S W", "E N", "W N", "E S", "W S"}
)

// AssistInput is a screen as a client reports it.
type AssistInput struct {
	Image        []byte
	MIME         string
	ImageWidth   int
	ImageHeight  int
	ScreenWidth  int
	ScreenHeight int
	App          string
	Title        string
	Nodes        []model.ScreenNode
	Goal         string
	Auto         bool // read without being asked, when the screen settled
}

// ---- memory -------------------------------------------------------------

// remember stores one context entry without blocking the input path.
func (s *Session) remember(kind, app, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	client := s.primary()
	mem := &model.Memory{DeviceID: client, ProfileID: s.profileID(), Kind: kind, App: app, Content: content}
	s.m.async(func(ctx context.Context) {
		if err := s.m.d.Store.CreateMemory(ctx, mem); err != nil {
			s.m.d.Log.Error("save memory failed", "err", err)
			return
		}
		s.m.d.Out.Publish(s.id, client, ChanOutput, "memory", mem, true)
	})
}

// recentMemory returns the context shown to Claude, oldest first.
func (m *Manager) recentMemory(ctx context.Context, deviceID string, profileID *string) []string {
	list, err := m.d.Store.RecentMemories(ctx, deviceID, profileID, memoryPromptSize)
	if err != nil {
		m.d.Log.Warn("load memory failed", "err", err)
		return nil
	}
	cutoff := time.Now().Add(-memoryWindow)
	var out []string
	for i := len(list) - 1; i >= 0; i-- {
		mem := list[i]
		if mem.CreatedAt.Before(cutoff) {
			continue
		}
		tag := mem.Kind
		if mem.App != "" {
			tag += " · " + mem.App
		}
		out = append(out, fmt.Sprintf("[%s %s] %s", mem.CreatedAt.Local().Format("15:04"), tag, mem.Content))
	}
	return out
}

// ---- reading a screen ---------------------------------------------------

// Assist reads a screen with Claude and makes its proposals available to
// the joystick. It blocks until the reading is ready.
func (m *Manager) Assist(ctx context.Context, profileID string, in AssistInput) (model.Assist, error) {
	s, err := m.get(ctx, profileID)
	if err != nil {
		return model.Assist{}, err
	}
	return m.read(ctx, s, in, false)
}

// More asks Claude for further proposals on the screen being read; they
// are added after the ones already shown, which keep their gestures.
func (m *Manager) More(ctx context.Context, profileID string) (model.Assist, error) {
	s, err := m.get(ctx, profileID)
	if err != nil {
		return model.Assist{}, err
	}
	s.mu.Lock()
	in := s.lastScreen
	active := s.assist != nil
	s.mu.Unlock()
	if in == nil || !active {
		return model.Assist{}, ErrNoActiveAssist
	}
	return m.read(ctx, s, *in, true)
}

func (m *Manager) read(ctx context.Context, s *Session, in AssistInput, more bool) (model.Assist, error) {
	s.mu.Lock()
	s.refresh(ctx)
	deviceID := s.primary()
	if !s.aiOn() {
		s.mu.Unlock()
		return model.Assist{}, ErrAIDisabled
	}
	profileID, lang, composed := s.profileID(), s.language(), s.buffer
	var actions []ai.VocabItem
	for _, a := range m.actionCatalogue(ctx) {
		if a.Category == "system" || a.Category == "key" {
			actions = append(actions, ai.VocabItem{Value: a.Key, Label: a.Label})
		}
	}
	slices.SortFunc(actions, func(a, b ai.VocabItem) int { return strings.Compare(a.Value, b.Value) })
	var gestures []ai.KnownPattern
	for _, p := range m.d.Library.ForContext(profileID, "nav.gesture") {
		if p.OutputKind == model.OutAction {
			gestures = append(gestures, ai.KnownPattern{Sequence: p.Sequence, Value: p.OutputValue})
		}
	}
	var offered []string
	var kept []model.Proposal
	if more && s.assist != nil {
		for _, p := range s.assist.Proposals {
			offered = append(offered, p.Label)
			if !p.Shortcut {
				kept = append(kept, p)
			}
		}
	}
	s.publish(ChanOutput, "assist_thinking", map[string]any{"app": in.App, "more": more})
	s.mu.Unlock()

	habits := m.habits(ctx, deviceID, profileID, in.App)
	hidden, err := m.d.Store.HiddenLabels(ctx, deviceID, profileID, in.App)
	if err != nil {
		m.d.Log.Warn("load hidden proposals failed", "err", err)
	}

	res, modelID, err := m.d.AI.Assist(ctx, ai.AssistRequest{
		Image: in.Image, MIME: in.MIME, ImageWidth: in.ImageWidth, ImageHeight: in.ImageHeight,
		ScreenWidth: in.ScreenWidth, ScreenHeight: in.ScreenHeight, App: in.App, Title: in.Title,
		Nodes: in.Nodes, Actions: actions, Gestures: gestures,
		Memory: m.recentMemory(ctx, deviceID, profileID), Composed: composed, Goal: in.Goal, Offered: append(offered, hidden...), Habits: habits.top(5), Language: lang,
	})
	if err != nil {
		s.mu.Lock()
		s.publish(ChanOutput, "assist_failed", map[string]any{"error": "The screen could not be read."})
		s.mu.Unlock()
		if errors.Is(err, ai.ErrDisabled) {
			return model.Assist{}, ErrAIDisabled
		}
		return model.Assist{}, err
	}

	allowed := map[string]bool{}
	for _, a := range actions {
		allowed[a.Value] = true
	}
	fresh := habits.rank(buildProposals(res.Proposals, in, allowed))
	fresh = dropRepeats(fresh, hidden)
	if more {
		fresh = dropRepeats(fresh, offered)
		if room := maxOnScreen - len(kept); len(fresh) > room {
			fresh = fresh[:max(0, room)]
		}
	}
	a := model.Assist{
		DeviceID: deviceID, ProfileID: profileID, App: in.App, Summary: tidy(res.Summary),
		Model: modelID, Status: model.AssistActive, Width: in.ScreenWidth, Height: in.ScreenHeight,
		Proposals: append(kept, fresh...), Auto: in.Auto,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if more && s.assist == nil {
		return model.Assist{}, ErrNoActiveAssist // answered or dismissed meanwhile
	}
	s.assignGestures(a.Proposals)
	a.Proposals = append(a.Proposals, s.shortcuts(res.Shortcuts, a.Proposals)...)
	for i := range a.Proposals {
		a.Proposals[i].Index = i
	}
	s.endAssist(ctx, model.AssistExpired, nil)
	if err := m.d.Store.CreateAssist(ctx, &a); err != nil {
		return model.Assist{}, err
	}
	s.assist, s.assistAt = &a, m.d.Now()
	screen := in
	s.lastScreen = &screen
	s.publish(ChanOutput, "assist", a)
	if more {
		return a, nil // the screen was remembered on the first reading
	}
	note := a.Summary
	if tidy(res.Memory) != "" {
		note = tidy(res.Memory)
	}
	s.remember(model.MemScreen, in.App, note)
	return a, nil
}

// buildProposals turns Claude's answer into executable proposals in
// screen pixels, dropping anything that does not resolve.
func buildProposals(in []ai.AssistProposal, screen AssistInput, allowedActions map[string]bool) []model.Proposal {
	nodes := map[int]model.ScreenNode{}
	for _, n := range screen.Nodes {
		nodes[n.ID] = n
	}
	scaleX, scaleY := 1.0, 1.0
	if screen.ImageWidth > 0 && screen.ScreenWidth > 0 {
		scaleX = float64(screen.ScreenWidth) / float64(screen.ImageWidth)
	}
	if screen.ImageHeight > 0 && screen.ScreenHeight > 0 {
		scaleY = float64(screen.ScreenHeight) / float64(screen.ImageHeight)
	}
	var out []model.Proposal
	for _, p := range in {
		if len(out) == maxProposals {
			break
		}
		label := tidy(p.Label)
		if label == "" {
			continue
		}
		prop := model.Proposal{Label: label, Kind: p.Kind, Confidence: max(0, min(1, p.Confidence)), Reason: p.Reason}
		switch p.Kind {
		case model.PropTap, model.PropLongPress, model.PropType:
			if n, ok := nodes[p.NodeID]; ok {
				prop.X, prop.Y = (n.Bounds[0]+n.Bounds[2])/2, (n.Bounds[1]+n.Bounds[3])/2
			} else if p.X >= 0 && p.Y >= 0 {
				prop.X, prop.Y = int(float64(p.X)*scaleX), int(float64(p.Y)*scaleY)
				if (screen.ImageWidth > 0 && p.X >= screen.ImageWidth) || (screen.ImageHeight > 0 && p.Y >= screen.ImageHeight) {
					// Outside the screenshot: the model answered in screen pixels.
					prop.X, prop.Y = p.X, p.Y
				}
			} else {
				continue
			}
			if screen.ScreenWidth > 0 && (prop.X >= screen.ScreenWidth || prop.Y >= screen.ScreenHeight) {
				continue
			}
			if p.Kind == model.PropType {
				if prop.Text = p.Text; strings.TrimSpace(prop.Text) == "" {
					continue
				}
			}
		case model.PropScroll:
			switch p.Direction {
			case "up", "down", "left", "right":
				prop.Direction = p.Direction
			default:
				continue
			}
			prop.X, prop.Y = screen.ScreenWidth/2, screen.ScreenHeight/2
			if n, ok := nodes[p.NodeID]; ok {
				prop.X, prop.Y = (n.Bounds[0]+n.Bounds[2])/2, (n.Bounds[1]+n.Bounds[3])/2
			}
		case model.PropAction:
			if !allowedActions[p.ActionKey] {
				continue
			}
			prop.ActionKey = p.ActionKey
		default:
			continue
		}
		prop.Index = len(out)
		out = append(out, prop)
	}
	return out
}

// habitWindow is how many past choices in an app shape the ranking.
const habitWindow = 200

// habitCounts counts a person's past choices in one app.
type habitCounts struct {
	counts map[string]int
	labels map[string]string
}

func habitKey(p model.Proposal) string {
	if p.Kind == model.PropAction {
		return "action:" + p.ActionKey
	}
	return p.Kind + ":" + strings.ToLower(strings.TrimSpace(p.Label))
}

func (m *Manager) habits(ctx context.Context, deviceID string, profileID *string, app string) habitCounts {
	h := habitCounts{counts: map[string]int{}, labels: map[string]string{}}
	if app == "" {
		return h
	}
	past, err := m.d.Store.ChosenProposals(ctx, deviceID, profileID, app, habitWindow)
	if err != nil {
		m.d.Log.Warn("load choice history failed", "err", err)
		return h
	}
	for _, p := range past {
		k := habitKey(p)
		h.counts[k]++
		if _, ok := h.labels[k]; !ok {
			h.labels[k] = p.Label
		}
	}
	return h
}

// top lists the most frequent choices, for the prompt.
func (h habitCounts) top(n int) []string {
	keys := make([]string, 0, len(h.counts))
	for k := range h.counts {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if h.counts[a] != h.counts[b] {
			return h.counts[b] - h.counts[a]
		}
		return strings.Compare(a, b)
	})
	var out []string
	for _, k := range keys[:min(n, len(keys))] {
		out = append(out, fmt.Sprintf("%s (%d×)", h.labels[k], h.counts[k]))
	}
	return out
}

// rank moves what the person usually picks ahead, so it gets the easiest
// gesture. Claude's confidence still counts; history adds up to 0.6.
func (h habitCounts) rank(props []model.Proposal) []model.Proposal {
	if len(h.counts) == 0 {
		return props
	}
	score := func(p model.Proposal) float64 { return p.Confidence + 0.15*float64(min(h.counts[habitKey(p)], 4)) }
	slices.SortStableFunc(props, func(a, b model.Proposal) int {
		sa, sb := score(a), score(b)
		switch {
		case sa > sb:
			return -1
		case sa < sb:
			return 1
		}
		return 0
	})
	return props
}

// dropRepeats removes proposals whose label was already offered.
func dropRepeats(props []model.Proposal, offered []string) []model.Proposal {
	seen := map[string]bool{}
	for _, l := range offered {
		seen[strings.ToLower(strings.TrimSpace(l))] = true
	}
	out := props[:0]
	for _, p := range props {
		if !seen[strings.ToLower(p.Label)] {
			out = append(out, p)
		}
	}
	return out
}

// shortcuts turns Claude's picks among the stored gestures into proposals
// that show the stored gesture. Picks that are not stored, or that repeat
// a proposal, are dropped. Back is offered when nothing else is.
func (s *Session) shortcuts(picks []ai.AssistShortcut, props []model.Proposal) []model.Proposal {
	stored := s.storedActions()
	have := map[string]bool{}
	for _, p := range props {
		if p.Kind == model.PropAction {
			have[p.ActionKey] = true
		}
	}
	if len(picks) == 0 {
		back := "Back"
		if s.language() == "ro" {
			back = "Înapoi"
		}
		picks = []ai.AssistShortcut{{ActionKey: "system.back", Label: back}}
	}
	var out []model.Proposal
	for _, sc := range picks {
		seq, ok := stored[sc.ActionKey]
		label := tidy(sc.Label)
		if !ok || have[sc.ActionKey] || label == "" || len(out) == maxShortcuts {
			continue
		}
		have[sc.ActionKey] = true
		out = append(out, model.Proposal{
			Label: label, Kind: model.PropAction, ActionKey: sc.ActionKey,
			Sequence: seq, Existing: true, Shortcut: true, Confidence: 0.5,
		})
	}
	return out
}

// storedActions maps each action to its shortest stored navigation gesture.
func (s *Session) storedActions() map[string]string {
	stored := map[string]string{}
	for _, p := range s.m.d.Library.ForContext(s.profileID(), "nav.gesture") {
		if p.OutputKind == model.OutAction {
			if cur, ok := stored[p.OutputValue]; !ok || len(p.Sequence) < len(cur) {
				stored[p.OutputValue] = p.Sequence
			}
		}
	}
	return stored
}

// assignGestures binds every proposal to a gesture. A proposal that is one
// of the person's stored actions shows that stored gesture; the others get
// free gestures that clash with nothing stored in gesture navigation.
func (s *Session) assignGestures(props []model.Proposal) {
	const ctxName = "nav.gesture"
	pid := s.profileID()
	stored := s.storedActions()
	var pool []string
	if s.set.Signal.Diagonals {
		pool = append(pool, diagonalPool...)
	}
	pool = append(pool, cardinalPool...)
	used := map[string]bool{}
	for _, p := range props {
		if p.Sequence != "" {
			used[p.Sequence] = true // kept from before "more": gestures stay put
		}
	}
	for i := range props {
		if props[i].Sequence != "" {
			continue
		}
		if props[i].Kind == model.PropAction {
			if seq, ok := stored[props[i].ActionKey]; ok {
				props[i].Sequence, props[i].Existing = seq, true
				continue
			}
		}
		for _, seq := range pool {
			if used[seq] {
				continue
			}
			if _, clash := s.m.d.Library.Match(pid, ctxName, seq); clash {
				continue
			}
			used[seq] = true
			props[i].Sequence = seq
			break
		}
	}
}

// assistContinues reports whether a sequence can still grow into a
// proposal's gesture, so the engine waits for the next stroke.
func (s *Session) assistContinues(seq string) bool {
	if s.assist == nil || s.context() != "nav.gesture" {
		return false
	}
	for _, p := range s.assist.Proposals {
		if !p.Existing && strings.HasPrefix(p.Sequence, seq+" ") {
			return true
		}
	}
	return false
}

// assistGesture performs the proposal bound to a gesture, if any.
func (s *Session) assistGesture(ctx context.Context, seq string) bool {
	if s.assist == nil || s.context() != "nav.gesture" {
		return false
	}
	for _, p := range s.assist.Proposals {
		if !p.Existing && p.Sequence != "" && p.Sequence == seq {
			s.performProposal(ctx, p)
			return true
		}
	}
	return false
}

// noteStoredAction marks the reading as answered when the person used a
// stored gesture that was one of the proposals.
func (s *Session) noteStoredAction(ctx context.Context, kind, value string) {
	if s.assist == nil || kind != model.OutAction {
		return
	}
	for _, p := range s.assist.Proposals {
		if p.Existing && p.ActionKey == value {
			s.rememberProposal(p)
			idx := p.Index
			s.endAssist(ctx, model.AssistChosen, &idx)
			return
		}
	}
}

// performProposal executes a proposal and closes the reading.
func (s *Session) performProposal(ctx context.Context, p model.Proposal) {
	switch p.Kind {
	case model.PropAction:
		s.apply(ctx, model.OutAction, p.ActionKey, nil, 0)
	case model.PropTap:
		s.publish(ChanOutput, "action", map[string]any{"key": "screen.tap", "params": map[string]any{"x": p.X, "y": p.Y, "label": p.Label}})
	case model.PropLongPress:
		s.publish(ChanOutput, "action", map[string]any{"key": "screen.long_press", "params": map[string]any{"x": p.X, "y": p.Y, "label": p.Label}})
	case model.PropType:
		s.publish(ChanOutput, "action", map[string]any{"key": "screen.type", "params": map[string]any{"x": p.X, "y": p.Y, "text": p.Text, "label": p.Label}})
	case model.PropScroll:
		s.publish(ChanOutput, "action", map[string]any{"key": "screen.scroll", "params": map[string]any{"x": p.X, "y": p.Y, "direction": p.Direction, "label": p.Label}})
	}
	s.rememberProposal(p)
	idx := p.Index
	s.endAssist(ctx, model.AssistChosen, &idx)
}

func (s *Session) rememberProposal(p model.Proposal) {
	app := ""
	if s.assist != nil {
		app = s.assist.App
	}
	s.remember(model.MemAction, app, "Chose: "+p.Label)
}

// endAssist closes the active reading, if any.
func (s *Session) endAssist(ctx context.Context, status string, chosen *int) {
	if s.assist == nil {
		return
	}
	a := s.assist
	s.assist = nil
	if err := s.m.d.Store.ResolveAssist(ctx, a.ID, status, chosen); err != nil {
		s.m.d.Log.Error("resolve assist failed", "err", err)
	}
	s.publish(ChanOutput, "assist_resolved", map[string]any{"id": a.ID, "status": status, "chosen": chosen})
}

// expireAssist drops a reading nobody acted on.
func (s *Session) expireAssist(ctx context.Context, now int64) {
	if s.assist != nil && now-s.assistAt > assistLifetimeMs {
		s.endAssist(ctx, model.AssistExpired, nil)
	}
}

// ChooseProposal performs a proposal picked on a client (touch, switch or
// pointer on the overlay) instead of by gesture.
func (m *Manager) ChooseProposal(ctx context.Context, profileID, assistID string, index int) error {
	s, err := m.get(ctx, profileID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assist == nil || (assistID != "" && s.assist.ID != assistID) {
		return ErrNoActiveAssist
	}
	if index < 0 || index >= len(s.assist.Proposals) {
		return ErrUnknownProposal
	}
	s.performProposal(ctx, s.assist.Proposals[index])
	return nil
}

// HideProposal removes one proposal from the reading and never offers it
// again in this app. The proposals left keep their gestures.
func (m *Manager) HideProposal(ctx context.Context, profileID, assistID string, index int) (model.Assist, error) {
	s, err := m.get(ctx, profileID)
	if err != nil {
		return model.Assist{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assist == nil || (assistID != "" && s.assist.ID != assistID) {
		return model.Assist{}, ErrNoActiveAssist
	}
	if index < 0 || index >= len(s.assist.Proposals) {
		return model.Assist{}, ErrUnknownProposal
	}
	a := *s.assist
	p := a.Proposals[index]
	a.Proposals = append(slices.Clone(a.Proposals[:index]), a.Proposals[index+1:]...)
	for i := range a.Proposals {
		a.Proposals[i].Index = i
	}
	s.assist = &a
	s.remember(model.MemNote, a.App, model.HiddenPrefix+p.Label)
	s.publish(ChanOutput, "assist", a)
	return a, nil
}

// DismissAssist closes the active reading without acting on it.
func (m *Manager) DismissAssist(ctx context.Context, profileID string) error {
	s, err := m.get(ctx, profileID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.assist == nil {
		return ErrNoActiveAssist
	}
	s.endAssist(ctx, model.AssistDismissed, nil)
	return nil
}

// RequestLook asks the device's screen clients to capture and read the screen.
func (m *Manager) RequestLook(ctx context.Context, profileID, goal string) error {
	s, err := m.get(ctx, profileID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publish(ChanOutput, "assist_request", map[string]any{"goal": goal})
	return nil
}

// tidy trims a text from the model, including a stray closing bracket of
// the JSON around it that now and then ends up inside the string.
func tidy(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasSuffix(s, "}") && !strings.Contains(s, "{") || strings.HasSuffix(s, "]") && !strings.Contains(s, "[") {
		s = strings.TrimSpace(s[:len(s)-1])
	}
	return s
}
