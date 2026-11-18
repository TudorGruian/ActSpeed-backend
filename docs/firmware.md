# ActSpeed firmware guide (ESP32 joystick)

This is everything the ESP32 firmware has to do. The device is a **dumb sensor**: it reads the joystick and publishes its position over MQTT. All gesture recognition, calibration, dead zones, timing and text entry happen on the backend, so the firmware stays small and never needs reflashing to tune for a person.

## 1. What the device does

1. Join Wi‑Fi.
2. Connect to the MQTT broker, with a Last Will.
3. Publish `online` on its status topic.
4. Read the joystick's X and Y axes and publish them on the raw topic when they change, plus a heartbeat once a second.
5. Reconnect automatically if Wi‑Fi or MQTT drops.

There are no buttons. All commands are stick movements, including circles that switch mode and select. If the hardware has a button, you do not need to send it.

## 2. Connection

| Setting | Development | Production |
|---|---|---|
| Broker | `mqtt://<backend-machine-ip>:1883` | `mqtts://<host>:8883` (TLS) |
| MQTT version | 3.1.1 or 5, either works | same |
| Client ID | the device ID (below) | same |
| Username / password | none (anonymous) | username = device ID, password issued per device |
| Keep‑alive | 15–30 s | same |
| Clean session | true | true |

**Device ID**: 1–64 characters, only `A-Z a-z 0-9 _ -`. Recommended: `esp32-` followed by the last 3 bytes of the Wi‑Fi MAC in hex, e.g. `esp32-a1b2c3`. It must be stable across reboots. The device appears on the dashboard automatically the first time it publishes; no registration is needed.

Make the broker address, Wi‑Fi credentials and MQTT credentials configurable, for example through a captive‑portal setup page or NVS. Do not hard‑code them.

> Dev note: the development broker currently listens on `127.0.0.1` only. To reach it from the ESP32, change `deploy/docker-compose.yml` to publish `"1883:1883"` on the backend machine. Do this only on a trusted network, because it is anonymous.

## 3. Topics

All topics start with `actspeed/v1/devices/{deviceId}/`.

| Topic | Direction | QoS | Retained | Required |
|---|---|---|---|---|
| `…/{id}/raw` | device → backend | 0 | no | **yes** |
| `…/{id}/status` | device → backend | 1 | **yes** | **yes** |
| `…/{id}/config` | backend → device | 1 | yes | reserved, subscribe later |
| `…/{id}/hid` | backend → device | 1 | no | reserved, later (BLE HID bridge) |

The device must **not** publish anywhere else. In production the broker ACL enforces this.

## 4. `raw`: joystick samples

Payload: compact JSON, one object per message.

```json
{"seq":1042,"t":733120,"x":815,"y":-40}
```

| Field | Type | Meaning |
|---|---|---|
| `seq` | uint32 | Counter that increases by 1 per message and wraps at 2³². |
| `t` | int | Milliseconds since boot (`millis()`), for debugging only. |
| `x` | int | −1000 … 1000. **Right is positive.** |
| `y` | int | −1000 … 1000. **Up is positive.** (Many sticks read down as positive. Invert it in firmware so up is +.) |

Resting centre should be close to `0,0`. Small offsets are fine because the backend calibrates per person.

### Scaling the ADC

The ESP32 ADC is 12‑bit (0–4095). At boot, sample the stick at rest for about 200 ms to get `centerRaw`. Then:

```c
int scale(int raw, int centerRaw, int minRaw, int maxRaw) {
  int v = raw >= centerRaw
      ? (long)(raw - centerRaw) * 1000 / (maxRaw - centerRaw)
      : (long)(raw - centerRaw) * 1000 / (centerRaw - minRaw);
  return v < -1000 ? -1000 : v > 1000 ? 1000 : v;
}
```

`minRaw`/`maxRaw` can be fixed constants measured once for the stick model. Use `ADC_11db` attenuation so the full 0–3.3 V range is readable, and average 4–8 readings per sample to cut noise. Use ADC1 pins (GPIO 32–39), because ADC2 does not work while Wi‑Fi is on.

### When to send

- Read the stick at **100 Hz** internally.
- Publish when `|x − lastX| ≥ 20` or `|y − lastY| ≥ 20`, at most **50 messages/s**.
- Also publish **once per second** even if nothing changed (heartbeat).
- Always publish when the stick comes back near centre, so the backend sees the return.

### What NOT to do in firmware

No dead zone, no thresholds, no debouncing, no gesture logic, no smoothing beyond ADC averaging. The backend does all of that per person and can change it live. Filtering on the device makes gestures (corner letters, circles) less reliable.

## 5. `status`: presence

On every successful connect, publish **retained, QoS 1**:

```json
{"status":"online","fw":"1.0.0"}
```

Register the **Last Will** in the CONNECT packet (same topic, retained, QoS 1):

```json
{"status":"offline","fw":"1.0.0"}
```

Before a planned shutdown or sleep, publish `offline` yourself. `fw` is your firmware version string, and it is shown on the dashboard.

## 6. Reliability

- Wi‑Fi or MQTT lost: retry with backoff of 1 s, 2 s, 5 s, then every 10 s. Never block the stick‑reading loop.
- Don't buffer samples while offline. Old positions are useless, so drop them.
- Keep `seq` increasing across reconnects; reset it only on reboot.
- Use a watchdog. A mouth‑operated device must recover by itself.

## 7. Reserved for later

You don't need these yet, but keep room in the design:

- **`config`** (backend → device, retained): device settings such as sample rate. Its format will be defined later.
- **`hid`**: the ESP32 will also act as a **Bluetooth mouse/keyboard** for iOS and other devices that allow no software input. The backend will send resolved actions such as `{"key":"pointer.move","params":{"dx":4,"dy":-2}}` here. Choose a module/stack that can run Wi‑Fi and BLE HID together, such as ESP32 or ESP32‑S3 with NimBLE.

## 8. Testing without the backend team

1. Start the stack, following the README: `docker compose -f deploy/docker-compose.yml up -d`, then `go run ./cmd/actspeed`.
2. Watch everything on the broker:
   ```bash
   mosquitto_sub -h <broker-ip> -v -t 'actspeed/v1/#'
   ```
3. Open the dashboard at `http://<backend-ip>:8080`. Your device ID appears with status *online*. The **Live** page shows the stick position and recognized gestures.
4. Check:
   - pushing right gives positive `x`; pushing up gives positive `y`
   - at rest, values stay within about ±50
   - full deflection reaches about ±1000
   - unplugging power shows *offline* within about 1.5× the keep‑alive
5. The reference implementation that does exactly this, with a PS4 controller in place of the stick, is [emulators/ps4/actspeed_sim.py](../emulators/ps4/actspeed_sim.py).

## 9. Checklist

- [ ] Stable device ID, used as client ID (and username in production)
- [ ] Last Will `offline` retained; `online` retained on connect
- [ ] `raw` JSON with `seq`, `t`, `x`, `y`; right = +x, up = +y; range ±1000
- [ ] Send on change ≥ 20, max 50/s, heartbeat 1/s
- [ ] No dead zone or filtering beyond ADC averaging
- [ ] Auto‑reconnect with backoff, watchdog
- [ ] Wi‑Fi / broker / credentials configurable, TLS for production
