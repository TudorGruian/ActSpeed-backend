# PS4 joystick emulator

The left stick on a DualShock 4 stands in for the mouth joystick. It sends X and Y samples to the backend using the [MQTT protocol](../../docs/protocol.md).

Run from this directory after starting the backend and MQTT broker.

```bash
python3 -m venv .venv
.venv/bin/pip install -r requirements.txt
.venv/bin/python actspeed_sim.py
```

Pair a DualShock 4 by holding Share + PS until the light bar flashes, then select it in Bluetooth settings. The dashboard will show it as `ps4-sim`.

| Flag | Purpose |
|---|---|
| `--host`, `--port` | MQTT broker, defaults to `localhost:1883` |
| `--device` | MQTT device ID, defaults to `ps4-sim` |
| `--list` | Inspect axis indexes |
| `--axis-x`, `--axis-y` | Remap the stick axes if SDL reports different indexes |
| `-v` | Print sent samples |

The simulator uses pygame-ce to handle macOS Bluetooth controller re-enumeration.
