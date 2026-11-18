package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/actspeed/actspeed-backend/internal/ai"
	"github.com/actspeed/actspeed-backend/internal/model"
	"github.com/actspeed/actspeed-backend/internal/resolver"
	"github.com/actspeed/actspeed-backend/internal/scheme"
	"github.com/actspeed/actspeed-backend/internal/seed"
	"github.com/actspeed/actspeed-backend/internal/signal"
)

// ---- fakes ---------------------------------------------------------------

type fakeStore struct {
	learnedGap  int
	mu          sync.Mutex
	profile     *model.Profile
	loose       map[string]bool // joysticks with no profile
	suggestions []*model.Suggestion
	utterances  []*model.Utterance
	recentTexts []string // newest first
	recorded    []string
	memories    []*model.Memory
	assists     []*model.Assist
}

func (f *fakeStore) TouchDevice(_ context.Context, id, kind string) (model.Device, error) {
	d := model.Device{ID: id, Kind: kind}
	if f.profile != nil && !f.loose[id] {
		d.ProfileID = &f.profile.ID
	}
	return d, nil
}
func (f *fakeStore) GetProfile(context.Context, string) (model.Profile, error) {
	if f.profile == nil {
		return model.Profile{}, errors.New("no profile")
	}
	return *f.profile, nil
}
func (f *fakeStore) SetProfileState(_ context.Context, _, mode string, paused bool) error {
	f.profile.Mode, f.profile.Paused = mode, paused
	return nil
}
func (f *fakeStore) ListPhrases(_ context.Context, _ *string, language string) ([]model.Phrase, error) {
	if language == "ro" {
		return []model.Phrase{{Text: "Da"}, {Text: "Nu"}}, nil
	}
	return []model.Phrase{{Text: "Yes"}, {Text: "No"}}, nil
}
func (f *fakeStore) RecentUtteranceTexts(context.Context, *string, int) ([]string, error) {
	return f.recentTexts, nil
}
func (f *fakeStore) ListActions(context.Context) ([]model.Action, error) { return seed.Actions(), nil }
func (f *fakeStore) CreateSuggestion(_ context.Context, s *model.Suggestion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s.ID = fmt.Sprintf("sug-%d", len(f.suggestions)+1)
	f.suggestions = append(f.suggestions, s)
	return nil
}
func (f *fakeStore) ResolveSuggestion(_ context.Context, id, status, chosen string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.suggestions {
		if s.ID == id {
			s.Status, s.Chosen = status, chosen
		}
	}
	return nil
}
func (f *fakeStore) CreateUtterance(_ context.Context, u *model.Utterance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u.ID = fmt.Sprintf("utt-%d", len(f.utterances)+1)
	f.utterances = append(f.utterances, u)
	return nil
}
func (f *fakeStore) AppendRecordingGesture(_ context.Context, _, seq string) error {
	f.recorded = append(f.recorded, seq)
	return nil
}

func (f *fakeStore) CreateMemory(_ context.Context, m *model.Memory) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m.ID = fmt.Sprintf("mem-%d", len(f.memories)+1)
	m.CreatedAt = time.Now()
	f.memories = append(f.memories, m)
	return nil
}
func (f *fakeStore) RecentMemories(_ context.Context, _ string, _ *string, limit int) ([]model.Memory, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Memory
	for i := len(f.memories) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, *f.memories[i])
	}
	return out, nil
}
func (f *fakeStore) CreateAssist(_ context.Context, a *model.Assist) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a.ID = fmt.Sprintf("assist-%d", len(f.assists)+1)
	f.assists = append(f.assists, a)
	return nil
}
func (f *fakeStore) SetProfileGap(_ context.Context, _ string, gapMs int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.learnedGap = gapMs
	return nil
}
func (f *fakeStore) HiddenLabels(_ context.Context, deviceID string, _ *string, app string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.memories {
		if m.DeviceID == deviceID && m.App == app && m.Kind == model.MemNote && strings.HasPrefix(m.Content, model.HiddenPrefix) {
			out = append(out, strings.TrimPrefix(m.Content, model.HiddenPrefix))
		}
	}
	return out, nil
}
func (f *fakeStore) ChosenProposals(_ context.Context, deviceID string, _ *string, app string, limit int) ([]model.Proposal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Proposal
	for i := len(f.assists) - 1; i >= 0 && len(out) < limit; i-- {
		a := f.assists[i]
		if a.DeviceID == deviceID && a.App == app && a.Status == model.AssistChosen && a.Chosen != nil && *a.Chosen < len(a.Proposals) {
			out = append(out, a.Proposals[*a.Chosen])
		}
	}
	return out, nil
}
func (f *fakeStore) ResolveAssist(_ context.Context, id, status string, chosen *int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.assists {
		if a.ID == id {
			a.Status, a.Chosen = status, chosen
		}
	}
	return nil
}

type msg struct {
	profile, client, channel, typ string
	data                          any
}

type fakeOut struct {
	mu   sync.Mutex
	msgs []msg
}

func (f *fakeOut) Publish(profile, client, channel, typ string, data any, _ bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg{profile, client, channel, typ, data})
}

// of returns the data of every message of a type, in order.
func (f *fakeOut) of(typ string) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []any
	for _, m := range f.msgs {
		if m.typ == typ {
			out = append(out, m.data)
		}
	}
	return out
}

func (f *fakeOut) actions() []string {
	var keys []string
	for _, d := range f.of("action") {
		keys = append(keys, d.(map[string]any)["key"].(string))
	}
	return keys
}

type fakeAI struct {
	cands     []model.Candidate
	corrected string
	assist    ai.AssistResult
	calls     int
	lastAsk   ai.AssistRequest
	lastFix   ai.CorrectRequest

	mu           sync.Mutex
	phrases      []string
	completions  int
	lastComplete ai.CompleteRequest
}

func (f *fakeAI) Assist(_ context.Context, r ai.AssistRequest) (ai.AssistResult, string, error) {
	f.calls++
	f.lastAsk = r
	return f.assist, "fake-haiku", nil
}

func (f *fakeAI) Complete(_ context.Context, r ai.CompleteRequest) ([]string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completions++
	f.lastComplete = r
	return f.phrases, "fake-haiku", nil
}

func (f *fakeAI) Enabled() bool { return true }
func (f *fakeAI) Interpret(context.Context, ai.InterpretRequest) ([]model.Candidate, string, error) {
	f.calls++
	return f.cands, "fake-model", nil
}
func (f *fakeAI) Correct(_ context.Context, r ai.CorrectRequest) (ai.CorrectResult, string, error) {
	f.calls++
	f.lastFix = r
	if f.corrected == "" {
		return ai.CorrectResult{Text: r.Text}, "fake-model", nil
	}
	return ai.CorrectResult{Text: f.corrected, Notes: "expanded"}, "fake-model", nil
}

// ---- harness -------------------------------------------------------------

type rig struct {
	t     *testing.T
	m     *Manager
	store *fakeStore
	out   *fakeOut
	now   atomic.Int64 // read by background work (AI answers) too
}

func newRig(t *testing.T, settings model.ProfileSettings, interp ai.Interpreter) *rig {
	r := &rig{t: t, store: &fakeStore{}, out: &fakeOut{}}
	phone := "phone"
	r.store.profile = &model.Profile{ID: "p1", Name: "Test", Language: "en", AIEnabled: true, Settings: settings,
		Mode: model.ModeNav, PrimaryClient: &phone}
	r.m = NewManager(Deps{
		Store: r.store, Library: resolver.NewLibrary(seed.Patterns()), Out: r.out, AI: interp,
		Now: r.now.Load, CompleteDelay: time.Millisecond,
	})
	return r
}

var vec = map[string][2]int{
	"N": {0, 900}, "S": {0, -900}, "E": {900, 0}, "W": {-900, 0},
	"NE": {650, 650}, "NW": {-650, 650}, "SE": {650, -650}, "SW": {-650, -650},
}

func (r *rig) sample(x, y, b int, advance int64) {
	r.m.HandleSample(context.Background(), "dev", signal.Sample{X: x, Y: y, B: b})
	for end := r.now.Load() + advance; r.now.Load() < end; {
		r.now.Add(20)
		r.m.TickAll(context.Background())
	}
}

// play runs a script: "N" flick, "N+" hold, "CW" / "CCW" circle,
// "|" gesture-ending pause.
func (r *rig) play(script string) {
	for _, tok := range strings.Fields(script) {
		switch tok {
		case "|":
			r.sample(0, 0, 0, 700)
		case "CW", "CCW":
			sign := 1.0
			if tok == "CW" {
				sign = -1
			}
			for i := 0; i <= 16; i++ {
				angle := sign * float64(i) * 2 * math.Pi / 16
				r.sample(int(850*math.Cos(angle)), int(850*math.Sin(angle)), 0, 20)
			}
			r.sample(0, 0, 0, 100)
		default:
			d := int64(100)
			if strings.HasSuffix(tok, "+") {
				d, tok = 800, strings.TrimSuffix(tok, "+")
			}
			v := vec[tok]
			r.sample(v[0], v[1], 0, d)
			r.sample(0, 0, 0, 100)
		}
	}
	r.m.Wait()
}

func (r *rig) state() State {
	st, err := r.m.State(context.Background(), "p1")
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

// ---- tests ---------------------------------------------------------------

func TestPointerModeMovesAndClicks(t *testing.T) {
	r := newRig(t, model.ProfileSettings{NavStyle: model.NavPointer}, nil)
	r.sample(900, 0, 0, 400) // push right for 400 ms
	r.sample(0, 0, 0, 100)

	var dx int
	for _, d := range r.out.of("action") {
		m := d.(map[string]any)
		if m["key"] == "pointer.move" {
			p := m["params"].(map[string]int)
			dx += p["dx"]
			if p["dy"] != 0 {
				t.Errorf("unexpected vertical movement: %v", p)
			}
		}
	}
	// Default slot is 500 px/s at full deflection, held for ~0.4 s.
	if dx < 100 || dx > 220 {
		t.Errorf("pointer moved %d px, want roughly 170", dx)
	}
	r.play("CCW |")
	keys := r.out.actions()
	if keys[len(keys)-1] != "pointer.click" {
		t.Errorf("last action = %s, want pointer.click", keys[len(keys)-1])
	}
}

func TestGestureNavigationMatchesPatterns(t *testing.T) {
	r := newRig(t, model.ProfileSettings{NavStyle: model.NavGesture}, nil)
	r.play("N | E E | W+ |")
	got := strings.Join(r.out.actions(), ",")
	if got != "key.up,scan.next,system.back" {
		t.Errorf("actions = %s", got)
	}
}

func TestModeToggleAndPauseIgnoreInput(t *testing.T) {
	r := newRig(t, model.ProfileSettings{NavStyle: model.NavGesture}, nil)
	r.play("CW |")
	if st := r.state(); st.Mode != model.ModeText {
		t.Fatalf("mode = %s, want text", st.Mode)
	}
	r.play("CW | CW CW |") // back to nav, then pause
	if st := r.state(); st.Mode != model.ModeNav || !st.Paused {
		t.Fatalf("state = %+v, want paused nav", st)
	}
	r.play("N | E | CCW |")
	if n := len(r.out.actions()); n != 0 {
		t.Errorf("%d actions fired while paused", n)
	}
	r.play("CW CW | N |")
	if got := strings.Join(r.out.actions(), ","); got != "key.up" {
		t.Errorf("after resume: actions = %q", got)
	}
}

func TestTwoDirSpellsPredictsAndSpeaks(t *testing.T) {
	r := newRig(t, model.ProfileSettings{TextScheme: model.SchemeTwoDir}, nil)
	r.play("CW |")
	// h = ...., e = .   (left = dot, right = dash, up = end of letter)
	r.play("W W W W N W N")
	st := r.state()
	if st.Buffer != "he" {
		t.Fatalf("buffer = %q, want he", st.Buffer)
	}
	if len(st.Predictions) == 0 || st.Predictions[0] != "help" {
		t.Fatalf("predictions = %v, want help first", st.Predictions)
	}
	r.play("N+") // hold up with no letter in progress: accept first prediction
	if st := r.state(); st.Buffer != "help " {
		t.Fatalf("buffer = %q, want %q", st.Buffer, "help ")
	}
	r.play("CCW |") // speak
	spoken := r.out.of("speak")
	if len(spoken) != 1 || spoken[0].(map[string]any)["text"] != "help" {
		t.Fatalf("speak = %v", spoken)
	}
	if st := r.state(); st.Buffer != "" {
		t.Errorf("buffer not cleared: %q", st.Buffer)
	}
	if len(r.store.utterances) != 1 {
		t.Errorf("utterances saved = %d", len(r.store.utterances))
	}
}

func TestCornersIgnoreEdgesBetweenCorners(t *testing.T) {
	r := newRig(t, model.ProfileSettings{TextScheme: model.SchemeCorners}, nil)
	r.play("CW |")
	// Sweep NW -> N -> NE without centring: the N in between must not count.
	r.sample(-650, 650, 0, 100)
	r.sample(0, 900, 0, 60)
	r.sample(650, 650, 0, 100)
	r.sample(0, 0, 0, 700)
	want := ""
	for _, p := range seed.CornerAlphabet() {
		if p.Sequence == "NW NE" {
			want = p.OutputValue
		}
	}
	if st := r.state(); st.Buffer != want || want == "" {
		t.Errorf("buffer = %q, want %q", st.Buffer, want)
	}
}

func TestGridSelectsLettersAndPhrases(t *testing.T) {
	r := newRig(t, model.ProfileSettings{TextScheme: model.SchemeGrid}, nil)
	r.play("CW |")
	r.play("CCW |")   // cursor starts on "e"
	r.play("E CCW |") // "t"
	if st := r.state(); st.Buffer != "et" {
		t.Fatalf("buffer = %q, want et", st.Buffer)
	}
	// Bottom row, last cell opens the phrase page; first phrase is "Yes".
	cells := r.state().Scheme.(map[string]any)["cells"].([][]scheme.Cell)
	for range len(cells) - 2 {
		r.play("S")
	}
	r.play("W W CCW |") // wraps from column 1 to "phrases"
	r.play("CCW |")
	spoken := r.out.of("speak")
	if len(spoken) != 1 || spoken[0].(map[string]any)["text"] != "Yes" {
		t.Fatalf("speak = %v", spoken)
	}
}

func TestUnmatchedGestureBecomesSuggestionAndNeedsConfirmation(t *testing.T) {
	fake := &fakeAI{cands: []model.Candidate{
		{Value: "key.up", Confidence: 0.6},
		{Value: "format.disk", Confidence: 0.9}, // not in the catalogue: must be dropped
	}}
	r := newRig(t, model.ProfileSettings{NavStyle: model.NavGesture}, fake)
	r.play("N W |") // no such pattern
	if n := len(r.out.actions()); n != 0 {
		t.Fatalf("action fired without confirmation: %v", r.out.actions())
	}
	st := r.state()
	if st.Pending == nil || len(st.Pending.Candidates) != 1 || st.Pending.Candidates[0].Value != "key.up" {
		t.Fatalf("pending = %+v", st.Pending)
	}
	r.play("E |") // accept
	if got := strings.Join(r.out.actions(), ","); got != "key.up" {
		t.Errorf("actions = %q, want key.up", got)
	}
	if s := r.store.suggestions[0]; s.Status != model.SugAccepted || s.Chosen != "key.up" {
		t.Errorf("stored suggestion = %+v", s)
	}
}

func TestConfidentSuggestionAutoAppliesAndRejectionDoesNothing(t *testing.T) {
	fake := &fakeAI{cands: []model.Candidate{{Value: "key.down", Confidence: 0.97}}}
	r := newRig(t, model.ProfileSettings{NavStyle: model.NavGesture}, fake)
	r.play("S W |")
	if got := strings.Join(r.out.actions(), ","); got != "key.down" {
		t.Fatalf("actions = %q, want key.down", got)
	}
	if s := r.store.suggestions[0]; s.Status != model.SugAutoApplied {
		t.Errorf("status = %s", s.Status)
	}

	fake.cands = []model.Candidate{{Value: "key.up", Confidence: 0.5}}
	r.play("N W |") // offered
	r.play("W |")   // rejected
	if got := strings.Join(r.out.actions(), ","); got != "key.down" {
		t.Errorf("rejected suggestion still acted: %q", got)
	}
	if s := r.store.suggestions[1]; s.Status != model.SugRejected {
		t.Errorf("status = %s", s.Status)
	}
}

func TestCorrectionIsOfferedBeforeSpeaking(t *testing.T) {
	fake := &fakeAI{corrected: "Hi."}
	r := newRig(t, model.ProfileSettings{TextScheme: model.SchemeTwoDir}, fake)
	r.play("CW |")
	r.play("W W W W N W W N") // "hi"
	r.play("CCW |")
	if n := len(r.out.of("speak")); n != 0 {
		t.Fatalf("spoke before confirmation")
	}
	r.play("E |") // accept the correction
	spoken := r.out.of("speak")
	if len(spoken) != 1 || spoken[0].(map[string]any)["text"] != "Hi." {
		t.Fatalf("speak = %v", spoken)
	}
	u := r.store.utterances[0]
	if u.Composed != "hi" || u.Final != "Hi." || !u.Corrected {
		t.Errorf("utterance = %+v", u)
	}
}

func TestAIDisabledForProfileNeverCallsModel(t *testing.T) {
	fake := &fakeAI{corrected: "Hi.", cands: []model.Candidate{{Value: "key.up", Confidence: 0.99}}}
	r := newRig(t, model.ProfileSettings{NavStyle: model.NavGesture}, fake)
	r.store.profile.AIEnabled = false
	r.play("N W |")
	if fake.calls != 0 || len(r.out.actions()) != 0 {
		t.Errorf("calls = %d, actions = %v", fake.calls, r.out.actions())
	}
}

func TestAlertWorksWhilePausedAndBypassesAI(t *testing.T) {
	fake := &fakeAI{}
	r := newRig(t, model.ProfileSettings{}, fake)
	r.play("CW CW |") // pause
	r.play("CCW CCW |")
	if n := len(r.out.of("alert")); n != 1 {
		t.Fatalf("alerts = %d, want 1", n)
	}
	if n := len(r.out.of("speak")); n != 1 {
		t.Errorf("speak events = %d, want 1", n)
	}
	if fake.calls != 0 {
		t.Errorf("AI was called %d times", fake.calls)
	}
}

func TestSeparateDiagonalFlicksAndButtonIgnored(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	r.sample(0, 0, 1, 800)
	r.sample(0, 0, 0, 700)
	if st := r.state(); st.Mode != model.ModeNav {
		t.Fatalf("legacy button switched mode to %s", st.Mode)
	}
	r.play("CW | NW | NE |")
	if got := r.state().Buffer; got != "et" {
		t.Fatalf("separate NW and NE flicks = %q, want et", got)
	}
	r.play("CCW |")
	if got := r.out.of("speak"); len(got) != 1 || got[0].(map[string]any)["text"] != "et" {
		t.Fatalf("speak = %v", got)
	}
}

func TestCornerDownSpeaksAsTypedWithAIEnabled(t *testing.T) {
	fake := &fakeAI{corrected: "Hello."}
	r := newRig(t, model.ProfileSettings{TextScheme: model.SchemeCorners}, fake)
	r.play("CW | NW | NE S")
	if n := len(r.out.of("correcting")); n != 0 {
		t.Fatalf("down flick requested %d corrections", n)
	}
	spoken := r.out.of("speak")
	if len(spoken) != 1 || spoken[0].(map[string]any)["text"] != "et" {
		t.Fatalf("speak = %v, want et", spoken)
	}

	r.play("NW | NE CCW |")
	if pending := r.state().Pending; pending == nil || pending.Kind != model.SugCorrect {
		t.Fatalf("pending = %+v, want a correction after CCW", pending)
	}
}

func TestLegacyProfileSpeakBindingDoesNotCorrect(t *testing.T) {
	fake := &fakeAI{corrected: "Hello."}
	r := newRig(t, model.ProfileSettings{TextScheme: model.SchemeCorners}, fake)
	profileID := r.store.profile.ID
	patterns := append(seed.Patterns(), model.Pattern{
		ProfileID: &profileID, Mode: "text.corners", Sequence: "S",
		OutputKind: model.OutControl, OutputValue: "text.speak", Enabled: true,
	})
	r.m.d.Library.Set(patterns)
	r.play("CW | NW S")
	spoken := r.out.of("speak")
	if fake.calls != 0 || len(spoken) != 1 || spoken[0].(map[string]any)["text"] != "e" {
		t.Fatalf("legacy binding: model calls = %d, speak = %v", fake.calls, spoken)
	}
}

func TestCornerFlicksJoinWithinLetterGap(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	r.play("CW | NW")
	if got := r.state().Buffer; got != "" {
		t.Fatalf("committed before the letter gap: %q", got)
	}
	r.play("NE |")
	if got := r.state().Buffer; got != "i" {
		t.Fatalf("joined flicks = %q, want i", got)
	}
}

func TestCornerFlicksJoinThreeStrokePattern(t *testing.T) {
	r := newRig(t, model.ProfileSettings{}, nil)
	r.play("CW | NW NE")
	if got := r.state().Buffer; got != "" {
		t.Fatalf("committed before the third stroke: %q", got)
	}
	r.play("NW |")
	pattern, ok := r.m.d.Library.Match(&r.store.profile.ID, "text.corners", "NW NE NW")
	if got := r.state().Buffer; !ok || got != pattern.OutputValue {
		t.Fatalf("joined flicks = %q, want %q", got, pattern.OutputValue)
	}
}
