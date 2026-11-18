// Package seed defines the built-in action catalogue and default patterns.
// Everything here is editable afterwards through the API; seeding only
// inserts rows that do not exist yet.
package seed

import (
	"encoding/json"
	"fmt"

	"github.com/actspeed/actspeed-backend/internal/model"
)

func act(key, label, category string) model.Action {
	return model.Action{Key: key, Label: label, Category: category, Builtin: true}
}

func actP(key, label, category string, params any) model.Action {
	a := act(key, label, category)
	a.Params, _ = json.Marshal(params)
	return a
}

// Actions is the navigation action catalogue. Keys are HID-shaped so any
// client (or a BLE HID bridge) can execute them without knowing gestures.
func Actions() []model.Action {
	return []model.Action{
		act("pointer.move", "Move pointer", "pointer"),
		act("pointer.click", "Left click", "pointer"),
		act("pointer.double", "Double click", "pointer"),
		act("pointer.right", "Right click", "pointer"),
		act("pointer.drag_lock", "Toggle drag lock", "pointer"),
		act("scroll", "Scroll", "pointer"),

		actP("key.up", "Arrow up", "key", map[string]any{"usage": "ArrowUp"}),
		actP("key.down", "Arrow down", "key", map[string]any{"usage": "ArrowDown"}),
		actP("key.left", "Arrow left", "key", map[string]any{"usage": "ArrowLeft"}),
		actP("key.right", "Arrow right", "key", map[string]any{"usage": "ArrowRight"}),
		actP("key.enter", "Enter", "key", map[string]any{"usage": "Enter"}),
		actP("key.escape", "Escape", "key", map[string]any{"usage": "Escape"}),
		actP("key.tab", "Tab", "key", map[string]any{"usage": "Tab"}),
		actP("key.backspace", "Backspace", "key", map[string]any{"usage": "Backspace"}),
		actP("key.combo.copy", "Copy", "key", map[string]any{"mods": []string{"primary"}, "usage": "KeyC"}),
		actP("key.combo.paste", "Paste", "key", map[string]any{"mods": []string{"primary"}, "usage": "KeyV"}),
		actP("key.combo.undo", "Undo", "key", map[string]any{"mods": []string{"primary"}, "usage": "KeyZ"}),
		actP("key.combo.switch_window", "Switch window", "key", map[string]any{"mods": []string{"alt"}, "usage": "Tab"}),

		act("system.home", "Home", "system"),
		act("system.back", "Back", "system"),
		act("system.app_switch", "App switcher", "system"),
		act("system.volume_up", "Volume up", "system"),
		act("system.volume_down", "Volume down", "system"),
		act("system.mute", "Mute", "system"),
		act("system.media_play_pause", "Play / pause", "system"),
		act("system.media_next", "Next track", "system"),
		act("system.media_previous", "Previous track", "system"),

		act("screen.tap", "Tap on screen", "screen"),
		act("screen.long_press", "Long press on screen", "screen"),
		act("screen.type", "Type text into a field", "screen"),
		act("screen.scroll", "Scroll the screen", "screen"),

		act("scan.next", "Scan: next item", "scan"),
		act("scan.prev", "Scan: previous item", "scan"),
		act("scan.select", "Scan: select item", "scan"),
	}
}

// withIndex sets {"index": i} as the pattern's output params.
func withIndex(p model.Pattern, i int) model.Pattern {
	p.OutputParams = json.RawMessage(fmt.Sprintf(`{"index":%d}`, i))
	return p
}

func pat(mode, seq, kind, value string) model.Pattern {
	return model.Pattern{Mode: mode, Sequence: seq, OutputKind: kind, OutputValue: value, Source: "builtin", Enabled: true}
}

// cornerLetters assigns the shortest corner paths to the most frequent
// letters. This is ActSpeed's own alphabet (not EdgeWrite's) and is meant
// to be edited per language and per person.
const cornerLetters = "etaoinshrdlcumwfgypbvkjxqz"

// CornerAlphabet generates letter -> corner-path patterns: 4 paths of one
// corner, 12 of two, then paths of three.
func CornerAlphabet() []model.Pattern {
	corners := []string{"NW", "NE", "SW", "SE"}
	var paths []string
	paths = append(paths, corners...)
	for _, a := range corners {
		for _, b := range corners {
			if a != b {
				paths = append(paths, a+" "+b)
			}
		}
	}
	for _, a := range corners {
		for _, b := range corners {
			for _, c := range corners {
				if a != b && b != c {
					paths = append(paths, a+" "+b+" "+c)
				}
			}
		}
	}
	out := make([]model.Pattern, 0, len(cornerLetters))
	for i, r := range cornerLetters {
		out = append(out, pat("text.corners", paths[i], model.OutChar, string(r)))
	}
	return out
}

// Patterns returns the default global pattern set.
func Patterns() []model.Pattern {
	p := []model.Pattern{
		// Gesture navigation: discrete flicks.
		pat("nav.gesture", "N", model.OutAction, "key.up"),
		pat("nav.gesture", "S", model.OutAction, "key.down"),
		pat("nav.gesture", "W", model.OutAction, "key.left"),
		pat("nav.gesture", "E", model.OutAction, "key.right"),
		pat("nav.gesture", "W+", model.OutAction, "system.back"),
		pat("nav.gesture", "N+", model.OutAction, "system.home"),
		pat("nav.gesture", "E+", model.OutAction, "system.app_switch"),
		pat("nav.gesture", "S+", model.OutAction, "system.media_play_pause"),
		pat("nav.gesture", "N N", model.OutAction, "system.volume_up"),
		pat("nav.gesture", "S S", model.OutAction, "system.volume_down"),
		pat("nav.gesture", "E E", model.OutAction, "scan.next"),
		pat("nav.gesture", "W W", model.OutAction, "scan.prev"),
		// Ask the screen assistant what can be done on the current screen.
		pat("nav", "N S", model.OutControl, model.CtlAssistLook),
		pat("nav.gesture", "CW CCW", model.OutControl, model.CtlAssistMore),

		// Short cardinal strokes are controls in the corner alphabet.
		pat("text.corners", "E", model.OutControl, model.CtlTextSpace),
		pat("text.corners", "W", model.OutControl, model.CtlTextBackspace),
		pat("text.corners", "N", model.OutControl, model.CtlTextAcceptPredic),
		pat("text.corners", "S", model.OutControl, model.CtlTextSpeak),
		pat("text.corners", "W+", model.OutControl, model.CtlTextClear),
		// Second word, then the first two phrases: holds, so they never
		// collide with letters (diagonals) or the quick controls (flicks).
		withIndex(pat("text.corners", "N+", model.OutControl, model.CtlTextAcceptPredic), 1),
		withIndex(pat("text.corners", "E+", model.OutControl, model.CtlTextAcceptPhrase), 0),
		withIndex(pat("text.corners", "S+", model.OutControl, model.CtlTextAcceptPhrase), 1),
	}
	return append(append(p, CornerAlphabet()...), JoystickPatterns()...)
}

// JoystickPatterns provides button-free controls across all input schemes.
func JoystickPatterns() []model.Pattern {
	return []model.Pattern{
		pat("any", "CW", model.OutControl, model.CtlModeToggle),
		pat("any", "CW CW", model.OutControl, model.CtlPauseToggle),
		pat("any", "CCW CCW", model.OutControl, model.CtlAlert),
		pat("nav.pointer", "CCW", model.OutAction, "pointer.click"),
		pat("nav.pointer", "CCW CW", model.OutAction, "pointer.double"),
		pat("nav.pointer", "CCW CW CW", model.OutAction, "pointer.right"),
		pat("nav.pointer", "CCW CW CCW", model.OutAction, "pointer.drag_lock"),
		pat("nav.pointer", "CW CCW CW", model.OutControl, model.CtlSpeedCycle),
		pat("nav.pointer", "CW CCW", model.OutControl, model.CtlScrollToggle),
		pat("nav.gesture", "CCW", model.OutAction, "key.enter"),
		pat("nav.gesture", "CCW CW", model.OutAction, "key.escape"),
		pat("text", "CCW", model.OutControl, model.CtlTextCorrectAndSpeak),
		pat("text.grid", "CCW", model.OutControl, model.CtlGridSelect),
		pat("text.twodir", "CW CCW", model.OutControl, model.CtlTextSpace),
	}
}

// Phrases are the starter phrase banks per language.
func Phrases() map[string][]string {
	return map[string][]string{
		"en": {"Yes", "No", "Thank you", "Please help me", "I'm thirsty", "I'm hungry",
			"I'm in pain", "I need the toilet", "I'm tired", "Please wait"},
		"ro": {"Da", "Nu", "Mulțumesc", "Te rog, ajută-mă", "Mi-e sete", "Mi-e foame",
			"Mă doare", "Trebuie să merg la toaletă", "Vreau să mă odihnesc", "Te rog, așteaptă"},
	}
}
