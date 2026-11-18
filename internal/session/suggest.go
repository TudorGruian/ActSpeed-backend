package session

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/actspeed/actspeed-backend/internal/ai"
	"github.com/actspeed/actspeed-backend/internal/model"
)

// Text suggestions while writing, at three levels: the next letters, whole
// words and whole phrases. Each carries the gesture that picks it, so the
// person never has to learn a separate selection step (letters show their
// own corner path). Lists stay short and stable, because scanning a long or
// reshuffling list can cost more time than the letters it saves.
const (
	letterCount   = 4
	phraseCount   = 3
	historyLimit  = 200
	completeDelay = 700 * time.Millisecond // pause in typing before asking Claude
	completeMin   = 2                      // letters typed before asking Claude
)

// Choice is one suggestion with the gesture that picks it, if it has one.
type Choice struct {
	Text     string `json:"text"`
	Sequence string `json:"sequence,omitempty"`
	Source   string `json:"source,omitempty"` // phrases: history | saved | ai
}

// TextSuggest is what the panel offers while writing.
type TextSuggest struct {
	Letters []Choice `json:"letters"`
	Words   []Choice `json:"words"`
	Phrases []Choice `json:"phrases"`
}

// learnText records a finished message for word prediction and phrases.
func (s *Session) learnText(final string) {
	s.pred.Learn(final)
	final = strings.TrimSpace(final)
	if final == "" {
		return
	}
	s.history = slices.DeleteFunc(s.history, func(h string) bool { return strings.EqualFold(h, final) })
	s.history = append([]string{final}, s.history...)
	if len(s.history) > historyLimit {
		s.history = s.history[:historyLimit]
	}
	s.historyUses[strings.ToLower(final)]++
}

// buildSuggest computes the letters, words and phrases for the buffer.
func (s *Session) buildSuggest() {
	sg := TextSuggest{Letters: []Choice{}, Words: []Choice{}, Phrases: []Choice{}}
	for _, l := range s.pred.NextLetters(s.buffer, letterCount) {
		sg.Letters = append(sg.Letters, Choice{Text: l, Sequence: s.gestureFor(model.OutChar, l, 0)})
	}
	for i, w := range s.predictions {
		sg.Words = append(sg.Words, Choice{Text: w, Sequence: s.gestureFor(model.OutControl, model.CtlTextAcceptPredic, i)})
	}
	phrases := s.rankPhrases()
	for i, p := range phrases {
		p.Sequence = s.gestureFor(model.OutControl, model.CtlTextAcceptPhrase, i)
		sg.Phrases = append(sg.Phrases, p)
	}
	s.suggest = sg
}

// rankPhrases merges the person's own past messages, Claude's completions
// and saved phrases. Items still valid keep their place, so a phrase does
// not jump while the person is reaching for it.
func (s *Session) rankPhrases() []Choice {
	typed := strings.ToLower(strings.TrimLeft(s.buffer, " "))
	var own, saved []Choice
	match := func(text string) bool {
		t := strings.ToLower(text)
		if typed == "" {
			return true
		}
		if len(t) > len(typed) && strings.HasPrefix(t, typed) {
			return true
		}
		return initialsMatch(t, typed)
	}
	for _, h := range s.history {
		if match(h) {
			own = append(own, Choice{Text: h, Source: "history"})
		}
	}
	// Most used first; the history is newest first, so ties go to the newest.
	slices.SortStableFunc(own, func(a, b Choice) int {
		return s.historyUses[strings.ToLower(b.Text)] - s.historyUses[strings.ToLower(a.Text)]
	})
	for _, p := range s.savedPhrases {
		if match(p) {
			saved = append(saved, Choice{Text: p, Source: "saved"})
		}
	}
	var fromAI []Choice
	if s.aiFor != "" && strings.HasPrefix(typed, strings.ToLower(strings.TrimSpace(s.aiFor))) {
		for _, p := range s.aiPhrases {
			if typed == "" || match(p) || strings.ToLower(strings.TrimSpace(s.aiFor)) == strings.TrimSpace(typed) {
				fromAI = append(fromAI, Choice{Text: p, Source: "ai"})
			}
		}
	}

	var out []Choice
	seen := map[string]bool{}
	add := func(list []Choice, max int) {
		n := 0
		for _, c := range list {
			k := strings.ToLower(strings.TrimRight(c.Text, ".!? "))
			if seen[k] || strings.EqualFold(strings.TrimSpace(c.Text), strings.TrimSpace(s.buffer)) || len(out) >= phraseCount || n >= max {
				continue
			}
			seen[k] = true
			out = append(out, c)
			n++
		}
	}
	if typed == "" {
		add(own, 2)
		add(saved, phraseCount)
	} else {
		add(own, 2)
		add(fromAI, phraseCount)
		add(saved, phraseCount)
		add(own, phraseCount)
	}
	return keepPlaces(s.suggest.Phrases, out)
}

// initialsMatch reports whether typed is the first letters of the phrase's
// words, as in "hay" for "how are you".
func initialsMatch(phrase, typed string) bool {
	if len(typed) < 2 || strings.ContainsRune(typed, ' ') {
		return false
	}
	words := strings.Fields(phrase)
	if len(words) < len(typed) {
		return false
	}
	for i, r := range typed {
		if i >= len(words) || !strings.HasPrefix(words[i], string(r)) {
			return false
		}
	}
	return true
}

// keepPlaces reorders next so items that were already shown keep their slot.
func keepPlaces(prev, next []Choice) []Choice {
	out := slices.Clone(next)
	for i, p := range prev {
		j := slices.IndexFunc(out, func(c Choice) bool { return c.Text == p.Text })
		if j < 0 || i >= len(out) || j == i {
			continue
		}
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// gestureFor finds the stored gesture that produces an output in the
// current context, shortest first.
func (s *Session) gestureFor(kind, value string, index int) string {
	best := ""
	for _, p := range s.m.d.Library.ForContext(s.profileID(), s.context()) {
		if !p.Enabled || p.OutputKind != kind || p.OutputValue != value || paramIndex(p.OutputParams) != index {
			continue
		}
		if best == "" || len(p.Sequence) < len(best) {
			best = p.Sequence
		}
	}
	return best
}

// paramIndex reads {"index": n} from a pattern's output params (0 if absent).
func paramIndex(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var p struct {
		Index int `json:"index"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return 0
	}
	return p.Index
}

// acceptPhrase puts a suggested phrase in the message, ready to speak or type.
func (s *Session) acceptPhrase(i int) {
	if i < 0 || i >= len(s.suggest.Phrases) {
		return
	}
	s.setBuffer(s.suggest.Phrases[i].Text + " ")
}

// scheduleComplete asks Claude for whole-message completions once the
// person pauses. Answers for an older buffer are dropped.
func (s *Session) scheduleComplete() {
	typed := strings.TrimSpace(s.buffer)
	if s.mode != model.ModeText || !s.aiOn() || len([]rune(typed)) < completeMin || typed == strings.TrimSpace(s.aiFor) {
		return
	}
	gen, lang, profileID, client := s.gen, s.language(), s.profileID(), s.primary()
	recent := s.history[:min(len(s.history), 10)]
	recent = slices.Clone(recent)
	app := ""
	if s.field != nil {
		app = s.field.App
	}
	delay := s.m.d.CompleteDelay
	s.m.async(func(ctx context.Context) {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
		s.mu.Lock()
		stale := s.gen != gen
		s.mu.Unlock()
		if stale {
			return // still typing
		}
		memory := s.m.recentMemory(ctx, client, profileID)
		phrases, _, err := s.m.d.AI.Complete(ctx, ai.CompleteRequest{Text: typed, Memory: memory, Recent: recent, Language: lang, App: app})
		if err != nil {
			s.m.d.Log.Warn("phrase completion failed", "err", err)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.gen != gen {
			return
		}
		s.aiPhrases, s.aiFor = phrases, typed
		s.buildSuggest()
		s.publishText()
	})
}
