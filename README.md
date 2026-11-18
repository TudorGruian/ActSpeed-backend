# ActSpeed backend

ActSpeed is an accessibility device: a joystick on an ESP32, used with the mouth or a thumb. This backend receives the joystick stream over MQTT and turns it into:

- **Navigation actions** (pointer, keys, system buttons, switch scanning) that client apps execute on a computer, phone or tablet.
- **Text and speech**: gestures spell letters, pick words and phrases, and the finished message is spoken.

ActSpeed recognizes motion and matches stored patterns locally. Haiku can suggest a match for an unmatched directional gesture. In the corner alphabet, a down flick speaks as typed and a counterclockwise circle asks Sonnet for a correction before speaking. Suggestions are kept for review, corrections need confirmation, and gesture suggestions above the profile threshold can auto-apply.

The contract for firmware and clients is in [docs/protocol.md](docs/protocol.md).

## Run it

Needs Go 1.27+ and Docker.

```bash
docker compose -f deploy/docker-compose.yml up -d
```

```bash
go run ./cmd/actspeed
```

The backend migrates the database, seeds the default actions, patterns and phrases, and listens on `:8080`.

Build the separate React dashboard with Node.js 20.19+ or 22.12+.

```bash
make dashboard
```

Open **http://localhost:8080** to see a person's live session, composed message, recognized gestures, recordings, profile settings and AI suggestions. Each profile (a person) owns its joysticks and its Android clients; one client is primary and acts on the screen, the others mirror the panel. New devices wait on the Profiles page until they are added to someone. For development, run `npm --prefix dashboard run dev` and open the address Vite prints. Vite proxies REST and WebSocket traffic to the backend.

The emulators live under [emulators](emulators). The [PS4 controller emulator](emulators/ps4/README.md) sends the left stick axes. The scripted emulator can send predictable paths without a controller.

```bash
go run ./emulators/trace -seq "CW | NW | NE | CCW |"
```

With its joystick (`sim-1` by default) added to a profile in navigation mode, this switches to text, enters `et` with a pause between flicks, and speaks the result. Use `-spell "help me"` with a device configured for two-direction Morse.

## Configuration

Environment variables (see [.env.example](.env.example)):

| Variable | Default | Purpose |
|---|---|---|
| `DASHBOARD_DIR` | `./dashboard/dist` | Directory containing the built React dashboard |
| `HTTP_ADDR` | `:8080` | API listen address |
| `ADMIN_TOKEN` | *(empty: API open)* | Bearer token for the API. **Set this outside local development** |
| `PUBLIC_URL` | | Base URL put in front of audio links, e.g. `http://192.168.1.10:8080` |
| `DATABASE_URL` | local compose Postgres | PostgreSQL connection string |
| `MQTT_URL`, `MQTT_USERNAME`, `MQTT_PASSWORD` | `mqtt://localhost:1883` | Broker |
| `ANTHROPIC_API_KEY` | | Enables Claude. Without it the backend works on stored patterns only |
| `AI_ENABLED` | on when a key is present | Force AI on or off |
| `AI_MODEL_INTERPRET` | `claude-haiku-5-5` | Suggests a match for an unmatched directional gesture |
| `AI_MODEL_CORRECT` | `claude-sonnet-5-5` | Corrects a composed message at Speak |
| `AI_TIMEOUT` | `8s` | After this the gesture is dropped / the message is spoken as typed |
| `AI_MODEL_ASSIST` | `claude-haiku-5-5` | Reads screenshots from a screen client and proposes actions |
| `AI_ASSIST_TIMEOUT` | `25s` | Time allowed for one screen reading |
| `TTS_PROVIDER` | `client` | `client` (clients speak the text), `piper`, or `elevenlabs` |
| `PIPER_URL`, `PIPER_VOICE` | `http://localhost:5000` | Piper HTTP server, and its voice for English (the server's default if empty) |
| `PIPER_VOICE_RO` | `ro_RO-mihai-medium` | Piper voice for profiles in Romanian (the Docker image downloads it) |
| `ELEVENLABS_API_KEY`, `ELEVENLABS_VOICE_ID` | | ElevenLabs; its multilingual model speaks Romanian with the same voice |
| `TTS_CACHE_DIR` | `./data/tts` | Synthesized audio cache |
| `ALERT_WEBHOOK_URL` | | Receives a JSON POST when the attention call fires (ntfy, SMS gateway, …) |
| `ALERT_MESSAGE` | `I need help, please come.` | Spoken on an attention call |
| `ALERT_MESSAGE_RO` | `Am nevoie de ajutor, vă rog să veniți.` | The same, for profiles in Romanian |

AI can also be switched off per person (`ai_enabled` on the profile): composed messages are personal speech, and with AI on they are sent to Anthropic for correction.

Offline speech with Piper:

```bash
docker compose -f deploy/docker-compose.yml --profile piper up -d --build
```

```bash
TTS_PROVIDER=piper go run ./cmd/actspeed
```

## Gesture recognition and calibration

The [EdgeWrite joystick study](https://citeseerx.ist.psu.edu/document?doi=980abe51c396422e00babd955ccf6d180ee3ae7f&repid=rep1&type=pdf) uses ordered corner visits and a return to center to separate letters. ActSpeed uses that segmentation principle with its own alphabet. A continuous `NW → NE` is `i`. Two quick flicks within the corner letter gap also produce `i`. Pause longer at center between the flicks to type `et`. The `N` edge between the two corners does not change the letter. The [XInput guidance](https://learn.microsoft.com/en-us/windows/win32/xinput/getting-started-with-xinput#dead-zone) describes why resting input varies across sticks and needs a dead zone.

Circles use signed angular movement around the calibrated center. A circle must traverse at least 300° and seven of eight sectors, with at least 80% of angular travel in the net direction. Large jumps and substantial reversals disqualify the circle. This preserves clockwise versus counterclockwise and accepts a range of starting angles and speeds. The [$1 Recognizer](https://depts.washington.edu/acelab/proj/dollar/index.html) is an option for a larger vocabulary of shapes. These two circular commands need only the analog sweep.

| Setting | Starting value | What to check on hardware |
|---|---:|---|
| Activation / release radius | 400 / 250 | Resting drift stays below release. Comfortable deflections cross activation. |
| Direction dwell | 35 ms | Short transients on the way to a corner are ignored. Deliberate flicks still register. |
| Neutral settle | 40 ms | Brief spring rebound does not split a stroke. Separate flicks finish reliably. |
| Compound gap | 500 ms | Enough time to start the second circle or a double navigation flick. Corner letters use their own gap. |
| Corner letter gap | 300 ms | Waits for another stroke only if the current stroke could start a longer letter. |
| Direction hold | 600 ms | The user can distinguish held commands from flicks comfortably. |

Use **Live → Record gesture trace** to capture raw samples and download the CSV, then adjust the profile in **Profiles**. Try separate NW/NE flicks, continuous NW → NE, and circles starting at different angles. Record intended gesture, recognized output, misses and false activations over repeated attempts at a comfortable pace. The values above are starting points for calibration.

| Package | Role |
|---|---|
| `internal/signal` | Axis calibration, direction quantization and proportional pointer input |
| `internal/gesture` | Analog stroke recognition, neutral segmentation and circles |
| `internal/scheme` | Text input schemes: two-direction, corner paths, scanning grid |
| `internal/predict` | Local word prediction that learns from the person's own messages |
| `internal/resolver` | In-memory pattern library and matching precedence |
| `internal/session` | Per-profile engine: modes, pause, pointer, text buffer, suggestions; routes outputs to the primary client |
| `internal/ai` | Claude: interpret a gesture, correct a message (structured output, vocabulary-filtered) |
| `internal/tts`, `internal/alert` | Speech providers, attention-call webhook |
| `internal/store` | PostgreSQL (migrations embedded, run at start) |
| `internal/api`, `internal/mqtt` | REST + WebSocket, broker client |
| `dashboard` | React dashboard |
| `emulators/ps4`, `emulators/trace` | DualShock and scripted input emulators |

## Tests

```bash
go test ./...
```

The engine is tested end to end with fakes for storage, AI and output; the Claude client is tested with an in-process HTTP transport that checks the request it sends.

## Before real use

- The development broker allows anonymous access on localhost. For real devices use per-device credentials and the ACL in [deploy/mosquitto/acl.example](deploy/mosquitto/acl.example), with TLS.
- Set `ADMIN_TOKEN`. There is one admin token today, no user accounts.
- The corner alphabet and timing still need validation with mouth-joystick users. Word prediction starts with an English base list and learns from use.
