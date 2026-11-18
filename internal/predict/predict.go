// Package predict is a small in-process word predictor: prefix completion
// weighted by word frequency, boosted by which word tends to follow the
// previous one. It learns from the person's own spoken messages, so it gets
// better with use and needs no network call.
package predict

import (
	_ "embed"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

//go:embed words_en.txt
var wordsEN string

//go:embed words_ro.txt
var wordsRO string

// baseLists are the frequency-ordered word lists per language.
var baseLists = map[string]*string{"en": &wordsEN, "ro": &wordsRO}

// fold removes the diacritics the joystick alphabet does not have, so
// "sa" finds "să" and "fata" finds "fată": the person spells plain letters
// and the prediction brings the correct spelling.
var fold = strings.NewReplacer(
	"ă", "a", "â", "a", "î", "i", "ș", "s", "ş", "s", "ț", "t", "ţ", "t",
	"é", "e", "è", "e", "ê", "e", "à", "a", "ç", "c", "ö", "o", "ü", "u",
).Replace

const (
	learnWeight  = 40 // one personal use outweighs a base-list rank
	bigramWeight = 200
	baseScale    = 230 // base-list ranks are scaled to 1..baseScale, whatever the list's length
	// Next letters sum over many words, so what usually follows the previous
	// word must count for more there than in word prediction.
	letterBigram = 4
)

// Predictor is safe for concurrent use.
type Predictor struct {
	mu  sync.RWMutex
	uni map[string]int
	bi  map[string]map[string]int
}

// New returns a predictor primed with the base list for a language
// (English and Romanian; "ro-RO" counts as Romanian). Other languages start
// empty and learn from use.
func New(language string) *Predictor {
	p := &Predictor{uni: map[string]int{}, bi: map[string]map[string]int{}}
	lang, _, _ := strings.Cut(strings.ToLower(language), "-")
	if lang == "" {
		lang = "en"
	}
	if list, ok := baseLists[lang]; ok {
		words := strings.Fields(*list)
		for i, w := range words {
			if _, seen := p.uni[w]; !seen { // a repeated word keeps its first (higher) rank
				p.uni[w] = 1 + (len(words)-i)*(baseScale-1)/len(words) // earlier = more frequent
			}
		}
	}
	return p
}

// Tokenize lower-cases and splits text into words (letters and apostrophes).
func Tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\''
	})
}

// Learn adds a spoken message to the model.
func (p *Predictor) Learn(text string) {
	words := Tokenize(text)
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, w := range words {
		p.uni[w] += learnWeight
		if i > 0 {
			m := p.bi[words[i-1]]
			if m == nil {
				m = map[string]int{}
				p.bi[words[i-1]] = m
			}
			m[w]++
		}
	}
}

// Predict returns up to n completions for the word being typed at the end
// of buffer. With a trailing space it predicts the next word.
func (p *Predictor) Predict(buffer string, n int) []string {
	lower := strings.ToLower(buffer)
	words := Tokenize(lower)
	prefix, prev := "", ""
	endsOpen := lower != "" && !strings.HasSuffix(lower, " ")
	if endsOpen && len(words) > 0 {
		prefix = words[len(words)-1]
		words = words[:len(words)-1]
	}
	if len(words) > 0 {
		prev = words[len(words)-1]
	}
	if prefix == "" && prev == "" {
		return nil
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	type scored struct {
		w string
		s int
	}
	var cands []scored
	follow := p.bi[prev]
	if prefix == "" {
		for w, c := range follow {
			cands = append(cands, scored{w, c*bigramWeight + p.uni[w]})
		}
	} else {
		fp := fold(prefix)
		for w, c := range p.uni {
			if w != prefix && strings.HasPrefix(fold(w), fp) {
				cands = append(cands, scored{w, c + follow[w]*bigramWeight})
			}
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].s != cands[j].s {
			return cands[i].s > cands[j].s
		}
		return cands[i].w < cands[j].w
	})
	if len(cands) > n {
		cands = cands[:n]
	}
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.w
	}
	return out
}

// NextLetters returns up to n letters most likely to come next, most likely
// first. Inside a word it weighs every known word that continues the prefix;
// at the start of a word it uses the words that tend to follow the previous
// one, then plain word frequency.
func (p *Predictor) NextLetters(buffer string, n int) []string {
	lower := strings.ToLower(buffer)
	words := Tokenize(lower)
	prefix, prev := "", ""
	if lower != "" && !strings.HasSuffix(lower, " ") && len(words) > 0 {
		prefix = words[len(words)-1]
		words = words[:len(words)-1]
	}
	if len(words) > 0 {
		prev = words[len(words)-1]
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	score := map[rune]int{}
	follow := p.bi[prev]
	fp := fold(prefix)
	for w, c := range p.uni {
		fw := fold(w)
		if len(fw) <= len(fp) || !strings.HasPrefix(fw, fp) {
			continue
		}
		r, _ := utf8.DecodeRuneInString(fw[len(fp):])
		if !unicode.IsLetter(r) {
			continue
		}
		score[r] += c + follow[w]*bigramWeight*letterBigram
	}
	letters := make([]rune, 0, len(score))
	for r := range score {
		letters = append(letters, r)
	}
	sort.Slice(letters, func(i, j int) bool {
		if score[letters[i]] != score[letters[j]] {
			return score[letters[i]] > score[letters[j]]
		}
		return letters[i] < letters[j]
	})
	if len(letters) > n {
		letters = letters[:n]
	}
	out := make([]string, len(letters))
	for i, r := range letters {
		out[i] = string(r)
	}
	return out
}

// Complete replaces the word being typed with a chosen prediction and adds
// a trailing space.
func Complete(buffer, word string) string {
	if buffer == "" || strings.HasSuffix(buffer, " ") {
		return buffer + word + " "
	}
	i := strings.LastIndex(buffer, " ")
	return buffer[:i+1] + word + " "
}
