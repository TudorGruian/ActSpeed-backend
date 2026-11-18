# ActSpeed protocol

This is the contract between the three parts of the system:

- **Firmware** (ESP32 + joystick) publishes raw samples over MQTT.
- **Backend** (this repository) turns samples into gestures, actions, text and speech.
- **Clients** (future OS and environment apps, the dashboard) subscribe to what the backend emits and use the REST API.

**Profiles and devices.** A profile is one person. It owns devices of two kinds:

- **Joysticks** (`kind: "joystick"`) give input. Any of the person's joysticks drives the same live session: one mode, one message being written, one set of proposals, one learned timing.
- **Clients** (`kind: "client"`), such as Android phones and tablets, show the panel. One of them is the profile's **primary client**: it taps, speaks, types into fields and reads the screen. The others mirror the panel. When a client joins a profile that has none, it becomes primary; when the primary client leaves, the most recently seen client takes over. `PUT /profiles/{id}/primary` changes it.

**Language.** Each profile has a `language`: `en` (English) or `ro` (Romanian); regional forms such as `ro-RO` are stored as `ro`, and other languages are refused. It decides the word predictions (Romanian matches words typed without diacritics, so `multum` finds `mulțumesc`), the shared starter phrases, the speech language, the attention-call message, the language Claude writes labels, summaries and corrections in, and the interface language of the dashboard and the Android app while that profile is selected. The live state carries it as `language`.

A new device belongs to no profile. A joystick registers on its first sample or status report; while unassigned, its samples drive nothing and the dashboard sees `unassigned_input` events so the moving joystick can be recognised. A client registers with `POST /devices`. Devices are assigned to a profile with `PATCH /devices/{id}` (`profile_id`).

Joystick topics live under `actspeed/v1/devices/{id}/`; outputs are published per profile under `actspeed/v1/profiles/{profile_id}/`. A device id is 1–64 characters of `A-Z a-z 0-9 _ -`.

## 1. Firmware → backend

### `…/raw` — joystick samples (QoS 0)

```json
{"seq": 1042, "t": 733120, "x": 815, "y": -40}
```

| Field | Meaning |
|---|---|
| `seq` | Counter, increments per sample |
| `t` | Device uptime in ms (informational; the backend timestamps on arrival) |
| `x`, `y` | Stick position, `-1000..1000`. **X positive = right, Y positive = up.** Centre is `0,0` |
| `b` | Legacy field accepted for compatibility. The session ignores it. |

Send a sample **whenever a value changes** by more than a small step (suggested: 20 units), and at least once per second as a heartbeat. 20–50 Hz while moving is plenty. Do not filter, threshold or debounce in firmware: dead zone, hysteresis, calibration offset, axis inversion and hold timing are per-person settings applied by the backend, so they can be tuned without reflashing.

### `…/status` — presence (QoS 1, retained)

Publish on connect, and register the offline message as the MQTT Last Will:

```json
{"status": "online", "fw": "1.0.0"}
```

A bare `online` / `offline` string is also accepted. A device is registered automatically the first time it publishes.

### Reserved for later

| Topic | Direction | Purpose |
|---|---|---|
| `…/config` | backend → device | Sample rate and similar device settings |
| `…/hid` | backend → device | Resolved actions for a device acting as a Bluetooth mouse/keyboard (the route to iOS, which allows no input injection) |

## 2. Backend → clients

Everything the backend emits uses one envelope, on MQTT (QoS 1, except pointer movement and trace samples at QoS 0) and mirrored on the WebSocket:

```json
{"v": 1, "type": "action", "profile": "5b0e…", "client": "android-3f9a1c2b", "ts": 1794496200123, "seq": 88, "data": {}}
```

`client` is the profile's primary client. Only that client carries out `action`, `speak`, `commit_text`, `assist_request` and typing into fields; the profile's other clients show the panel only.

WebSocket: `GET /v1/ws?device={id}` for a client device (it gets the messages of the profile it is assigned to, and an `assigned` message with `{"device_id", "profile_id"}` whenever that changes), `?profile={id}` to watch one profile, or neither for everything (dashboards; this includes `unassigned_input`). Authenticate with `Authorization: Bearer <token>`, or `?token=` for clients that cannot set headers. A client connection marks the device online.

### `…/events`

| `type` | `data` |
|---|---|
| `gesture` | `{"tokens": ["N","E"], "sequence": "N E", "raw_sequence": "N E", "context": "nav.gesture", "duration_ms": 310}` |

| `sample` | Raw axis sample during an active recording. Clients can export this live stream. |
| `profile_changed` | `{"language": "ro"}` when a profile's settings change. Clients and dashboards reload the state, so a new language applies at once. |
| `unassigned_input` | `{"device_id": "esp32-ab12"}`, at most every 2 s, when a joystick with no profile moves. Sent to dashboards only. |

### `…/output`

| `type` | `data` | Client should |
|---|---|---|
| `action` | `{"key": "key.up", "params": {"usage": "ArrowUp"}, "label": "Arrow up"}` | Execute the action (see catalogue). `label` is the catalogue name in English, for display; clients translate it |
| `mode_changed` | `{"mode": "text", "context": "text.twodir", "paused": false}` | Update its indicator |
| `text_update` | `{"buffer": "hel", "predictions": ["help","hello"], "suggest": {…}, "scheme": "grid", "scheme_state": {}}` | Show the message being composed and the writing help (below) |
| `correcting` | `{"text": "hw r u"}` | Show that a correction is being prepared |
| `suggestion` | A suggestion object (below), `status: "pending"` | Show it and how to answer |
| `suggestion_applied` | A suggestion object, `status: "auto_applied"` | Optionally show what was assumed |
| `suggestion_resolved` | `{"id": "…", "status": "accepted", "chosen": "key.up"}` | Hide the prompt |
| `speak` | `{"text": "Help me.", "language": "en", "provider": "piper", "audio_url": "…/v1/tts/ab12.wav", "audio_mime": "audio/wav", "utterance_id": "…"}` | Play `audio_url` if present, otherwise speak `text` with the platform voice |
| `alert` | `{"device_id": "…", "profile_name": "Ana", "message": "I need help, please come.", "time": "…"}` | Raise attention |
| `unmatched` | `{"sequence": "N W", "context": "nav.gesture"}` | Optional "not recognised" cue |
| `pointer_speed` | `{"slot": 2, "speed": 1200}` | Optional indicator |
| `scroll_mode` | `{"scrolling": true}` | Optional indicator |
| `assist_request` | `{"goal": ""}` | A screen client captures the screen and posts it to `POST /devices/{id}/assist` |
| `assist_thinking` | `{"app": "com.android.settings", "more": false}` | Show that the screen is being read |
| `assist` | An assist object (below), `status: "active"` | Show the proposals with their gestures |
| `assist_failed` | `{"error": "…"}` | Hide the progress indicator and show the error |
| `assist_resolved` | `{"id": "…", "status": "chosen", "chosen": 0}` | Hide the proposals (`chosen`, `dismissed` or `expired`) |
| `field` | `{"active": true, "app": "com.android.chrome", "hint": "Search"}` | A text field opened (or closed) with the ActSpeed keyboard |
| `commit_text` | `{"text": "weather in Bucharest", "app": "…"}` | Keyboard clients type the text into the open field |
| `timing_learned` | `{"gap_ms": 620, "previous_ms": 500, "samples": 16}` | The compound-gesture wait was adjusted to the person (optional to show) |
| `memory` | A memory object `{"id","device_id","profile_id","kind","content","created_at"}` | Optional |

Suggestion object:

```json
{"id": "…", "kind": "interpret", "context": "nav.gesture", "input": "N W",
 "candidates": [{"value": "key.up", "confidence": 0.6, "reason": "extra W"}],
 "model": "claude-haiku-5-5", "status": "pending"}
```

`kind` is `interpret` (an unmatched gesture; `candidates` are action keys or characters) or `correct` (a composed message; the single candidate is the corrected text). The person answers with the joystick: **right flick = accept, left flick = reject**. A `correct` suggestion can also be answered with a **down flick = edit**: the corrected text goes back into the message (status `edited`), in text mode, so the person can change it before speaking. In gesture navigation and corner text entry, another mapped gesture dismisses the suggestion.

### Screen assistant

A screen client is an app on the controlled device, such as the Android client in `actspeed-android`. It can read the screen and inject touches. The person asks for help with the look gesture `N S` (navigation), or the client asks on its own. The client then sends a screenshot and the visible elements. Claude (`AI_MODEL_ASSIST`) answers with a short summary and up to four proposals. The backend gives each proposal a gesture that is free in navigation: the diagonals when they are on, otherwise two-stroke turns such as `N E` and `S W`. Proposals that match a stored action use that action's stored gesture and are marked `existing: true`. Every other stored gesture keeps working while proposals are shown.

```json
{"id": "…", "device_id": "phone-1", "app": "com.android.settings", "summary": "Settings main page…",
 "proposals": [{"index": 0, "label": "Open Network & internet", "kind": "tap", "x": 540, "y": 742,
                "sequence": "N E", "existing": false, "confidence": 0.8}],
 "model": "claude-haiku-5-5", "status": "active", "width": 1080, "height": 2400}
```

**Automatic readings.** A client may read the screen without being asked and sends `auto: true`; the assist then carries `auto: true` too. The Android client reads a screen once it has settled (1.2 s without changes) and the joystick has been still for 1.5 s, at most every 5 s. It skips screens whose controls are the same as the last reading and screens that block screenshots, and it pauses for 20 minutes in an app where three automatic readings in a row were not used. Help is offered at a natural pause, not mid-action, and is easy to ignore: stored gestures keep working and a collapsed panel stays collapsed.

**Stored gestures and more options.** Claude also picks up to three of the person's stored navigation gestures that fit the screen, such as Back, Enter or scanning. They come after the proposals with `shortcut: true` and `existing: true`, and the panel shows them as small chips with the stored gesture. If Claude picks none, Back is offered. The gesture `CW CCW` (a clockwise circle, then a counterclockwise one) or `POST /devices/{id}/assist/more` asks for more options on the same screen. Claude is told what was already offered. New proposals are added after the shown ones, which keep their gestures, up to 8.

**Learning from choices.** Proposals the person chose in the same app over the last 90 days (up to 200 readings) rank higher, and the five most chosen are given to Claude as habits. A proposal can be removed with `POST /devices/{id}/assist/hide` (`assist_id`, `index`). The others keep their gestures, and the label is remembered as a memory note "Do not suggest: …" for that app, so it is not offered there again. Deleting the note on the Memory page undoes it.

Proposal `kind` is `tap`, `long_press`, `type` (`text`), `scroll` (`direction`) or `action` (`action_key`). Coordinates are in screen pixels. A proposal is performed with its gesture or with `POST /devices/{id}/assist/choose`. Tap, long press, type and scroll become `screen.*` actions. An `action` proposal runs like the stored action. Proposals expire after 3 minutes.

**Typing into fields.** A keyboard client, such as the ActSpeed keyboard on Android, reports a focused field with `POST /devices/{id}/field` (`active`, `app`, `hint`). An open field switches gesture navigation to text mode. The finished message is then sent as `commit_text` instead of `speak`, and nothing is said aloud. It is learned for predictions and remembered as "Typed: …". Closing the field switches back to navigation, unless a message is still half written. Alerts are always spoken.

**Session memory.** The backend stores short notes per profile: what was on screen, what was chosen, what was said, plus notes added by hand. The 20 most recent notes from the last 6 hours are added to screen readings, gesture interpretation and message correction, so the AI sees the context. Memory is listed and cleared with `/memories`.

## 3. Gestures

The backend reduces samples to tokens:

| Token | Meaning |
|---|---|
| `N` `E` `S` `W` (and `NE` `SE` `SW` `NW` where diagonals are on) | Stick pushed in that direction |
| `N+` … | Direction held |
| `CW` / `CCW` | Clockwise / counterclockwise analog circle, followed by a return to center |

A stroke begins outside the activation radius. A continuous path stays together until the stick returns inside the release radius for `neutral_ms` (40 ms by default). Simple scheme movements resolve at that point. Corner letters that can continue wait for another stroke for up to `corner_gap_ms` (300 ms by default). The engine waits up to `gap_ms` (500 ms by default) when a navigation or circle stroke can start a longer mapped sequence. Use one neutral return between strokes in a compound pattern, such as `CW CW`.

**Learned timing.** With `adaptive_timing` on (the default), the engine learns `gap_ms` from the person. It records the pauses between strokes of one compound gesture, and the near misses, where a pause ran over and split a stored compound gesture in two. After 12 pauses, and every 8 after that, it sets `gap_ms` to 1.4 × the 90th-percentile pause + 60 ms, between 300 and 1200 ms, when that differs by 50 ms or more. The new value is saved in the profile and published as `timing_learned`. Corner letters (`corner_gap_ms`) are not learned.

A **pattern** maps a sequence to an output in a mode:

| Mode | Applies in |
|---|---|
| `any` | Where | Gesture | Result |
|---|---|---|
| Everywhere | `CW` | Switch navigation ↔ text |
| Everywhere | `CW CW` | Pause / resume |
| Everywhere | `CCW CCW` | Attention call (local command and optional webhook) |
| Navigation · gesture | `N` / `E` / `S` / `W` | Arrow keys |
| Navigation · gesture | `CCW` / `CCW CW` | Enter / Escape |
| Navigation · gesture | `W+` / `N+` / `E+` / `S+` | Back / Home / App switcher / Play-pause |
| Navigation · gesture | `N N` / `S S` / `E E` / `W W` | Volume up / down / scan next / previous |
| Navigation · gesture | `N S` | Look: ask the screen client for a screen reading |
| Navigation · gesture | `CW CCW` | More options for the screen being read (or a reading, if none) |
| Navigation · pointer | Analog position | Proportional pointer movement |
| Navigation · pointer | `CCW` / `CCW CW` / `CCW CW CW` | Click / double click / right click |
| Navigation · pointer | `CCW CW CCW` / `CW CCW` / `CW CCW CW` | Drag lock / scroll mode / speed slot |
| Text · corners | `S` | Speak as typed |
| Text · corners | `N` / `N+` | First / second word |
| Text · corners | `E+` / `S+` | First / second phrase |
| Text · corners or two-direction | `CCW` | Request correction before speaking |
| Text · grid | `CCW` | Select the highlighted cell |

While paused, only resume and attention call can execute. Gesture navigation is the default for new profiles. Analog pointer mode can move the pointer during a circular command.

## 4. Text schemes

Chosen per profile with `settings.text_scheme`.

**`corners` — corner alphabet (default).** The alphabet is stored as patterns in `text.corners`. Trace the corners of a letter continuously or make separate flicks within the corner letter gap. Pause longer at center between letters. For example, `NW` = e, `NE` = t and one continuous `NW → NE` stroke = i. Cardinal strokes are controls. `E` adds a space, `W` deletes, `N` accepts the first word and `N+` (hold up) the second, `E+` and `S+` (hold right, hold down) put the first or second phrase in the message, `S` speaks as typed and `W+` clears. Use `CCW` to request correction before speaking.

**Writing help.** In text mode the state and every `text_update` carry `suggest`, three short lists. Each item has the gesture that picks it in the current scheme (`sequence`, empty if none):

```json
{"letters": [{"text": "h", "sequence": "NE NW"}],
 "words":   [{"text": "the", "sequence": "N"}, {"text": "this", "sequence": "N+"}],
 "phrases": [{"text": "thank you", "sequence": "E+", "source": "history"},
             {"text": "tell them I'm here", "sequence": "S+", "source": "ai"}]}
```

`letters` are the four most likely next letters, from the word list and the person's own messages; their gesture is the letter's own corner path. `words` are the word predictions. `phrases` come from the person's past messages (most used first, matching the start or the initials, such as "hay" for "how are you"), from Claude, and from saved phrases. With nothing typed, the most used messages are offered. When the person pauses for 700 ms with at least two letters typed and AI on, Claude (`AI_MODEL_INTERPRET`) proposes up to three whole messages that continue or expand the text, using recent messages and session memory (`source: "ai"`). A phrase that is still valid keeps its place in the list.

**`twodir` — two directions (Morse code).** Left enters a dot, right enters a dash, up commits the letter, and down cancels the letter or deletes one character. With no pending letter, hold up to accept the first prediction or down to clear the message. Use `CW CCW` for space and `CCW` to request correction before speaking.

**`grid` — scanning keyboard.** One cardinal stroke moves one cell. Select with `CCW` or enable resting selection with `settings.dwell_select_ms`. The first page holds predictions, letters and text controls. Its Speak cell reads the message as typed. The second page holds phrases.

## 5. Action catalogue

`GET /v1/actions` lists it. Keys are shaped like mouse and keyboard events so a client maps them directly to its platform's input API.

| Category | Keys |
|---|---|
| Pointer | `pointer.move` `{dx,dy}` (pixels, screen Y down), `pointer.click`, `pointer.double`, `pointer.right`, `pointer.drag_lock`, `scroll` `{dx,dy}` |
| Keys | `key.up` `key.down` `key.left` `key.right` `key.enter` `key.escape` `key.tab` `key.backspace` — `params.usage` is a [KeyboardEvent code](https://developer.mozilla.org/docs/Web/API/UI_Events/Keyboard_event_code_values) |
| Shortcuts | `key.combo.copy` `key.combo.paste` `key.combo.undo` `key.combo.switch_window` — `params.mods` uses `primary` for Ctrl (Cmd on macOS) |
| System | `system.home` `system.back` `system.app_switch` `system.volume_up` `system.volume_down` `system.mute` `system.media_play_pause` `system.media_next` `system.media_previous` |
| Switch scanning | `scan.next` `scan.prev` `scan.select` |
| Screen | `screen.tap` `{x,y}`, `screen.long_press` `{x,y}`, `screen.type` `{x,y,text}`, `screen.scroll` `{x,y,direction}` (screen pixels; sent by the screen assistant) |

Suggested platform mapping: Linux `uinput`, Windows `SendInput`, macOS `CGEvent`, Android `AccessibilityService`.

## 6. REST API

Base path `/v1`, JSON, `Authorization: Bearer $ADMIN_TOKEN`. `GET /healthz` is unauthenticated.

| Resource | Endpoints |
|---|---|
| Profiles | `GET/POST /profiles`, `GET/PUT/DELETE /profiles/{id}`, `PUT /profiles/{id}/primary` (`device_id`). A profile also carries its live `mode`, `paused` and `primary_client`; these are read-only in `PUT`. |
| Devices | `GET /devices?profile_id=`, `POST /devices` (`id`, `kind`: `client` or `joystick`, `name`), `GET/PATCH/DELETE /devices/{id}` (`name`, `kind`, `profile_id`; `null` unassigns), `POST /devices/{id}/sample` (`x`, `y`) |
| Live session | Under `/profiles/{id}` (dashboard) or `/devices/{id}` (a device acting for its profile; `409` while unassigned): `GET …/state`, `POST …/mode` (`mode`, `paused`), `POST …/speak` (`text`), `POST …/recordings`, and the screen assistant routes below. `POST /profiles/{id}/sample` drives a profile without a device (the dashboard's test joystick). |
| Recordings | `POST /profiles/{id}/recordings`, `GET /recordings`, `GET /recordings/{id}`, `POST /recordings/{id}/stop`, `POST /recordings/{id}/pattern` (`index` + pattern fields) |
| Patterns | `GET /patterns?mode=&profile_id=`, `POST /patterns`, `PUT/DELETE /patterns/{id}` |
| Actions | `GET /actions`, `PUT/DELETE /actions/{key}` |
| Phrases | `GET /phrases?profile_id=&language=` (shared phrases in that language, the profile's language by default, plus the profile's own), `POST /phrases` (`text`, `profile_id`, `language`), `DELETE /phrases/{id}` |
| Suggestions | `GET /suggestions?status=`, `POST /suggestions/{id}/accept\|reject\|edit\|promote` |
| History | `GET /utterances` |
| Screen assistant | `POST /devices/{id}/assist` (`image` base64 JPEG/PNG up to 6 MB, `mime`, `image_width`, `image_height`, `screen_width`, `screen_height`, `app`, `title`, `nodes`, `goal`, `auto`), `DELETE /devices/{id}/assist`, `POST /devices/{id}/assist/choose` (`assist_id`, `index`), `POST /devices/{id}/assist/request` (`goal`), `POST /devices/{id}/assist/more`, `POST /devices/{id}/assist/hide` (`assist_id`, `index`), `POST /devices/{id}/field` (`active`, `app`, `hint`), `GET /assists?device_id=&profile_id=`. A screen reading from a client that is not primary is refused (`409`); a field report from one is ignored. |
| Memory | `GET /memories?device_id=&profile_id=&limit=`, `POST /memories` (`profile_id`, or a `device_id` of the profile; `kind`, `content`), `DELETE /memories/{id}`, `DELETE /memories?device_id=` or `?profile_id=` |
| Audio | `GET /tts/{file}` |

Profile settings (all optional):

```json
{
  "signal": {"dead_zone": 400, "release": 250, "hold_ms": 600, "direction_ms": 35, "neutral_ms": 40,
             "center_x": 0, "center_y": 0, "invert_x": false, "invert_y": false},
  "gap_ms": 500,
  "corner_gap_ms": 300,
  "adaptive_timing": true,
  "nav_style": "gesture",
  "text_scheme": "corners",
  "pointer_speeds": [150, 500, 1200],
  "pointer_slot": 1,
  "dwell_select_ms": 0,
  "auto_apply_threshold": 0.9
}
```

### The dashboard flows this API supports

1. **Record a pattern.** `POST /profiles/{id}/recordings` → the person performs gestures → `POST /recordings/{id}/stop` → `POST /recordings/{id}/pattern` with which recorded gesture and what it should do.
2. **Review AI suggestions.** `GET /suggestions` shows every proposal with the gesture, the candidates and what happened. `promote` turns an interpreted gesture into a permanent pattern, after which it matches without the AI.
