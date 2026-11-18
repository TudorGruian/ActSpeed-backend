#!/usr/bin/env python3
"""ActSpeed device simulator: a game controller (e.g. a PS4 DualShock over
Bluetooth) standing in for the ESP32 joystick until the hardware exists.

It speaks exactly the device side of docs/protocol.md:

  actspeed/v1/devices/{id}/raw     {"seq","t","x","y"}  QoS 0, on change + 1 s heartbeat
  actspeed/v1/devices/{id}/status  {"status":"online","fw":...} retained, offline as Last Will

Mapping:
  left stick  -> x / y  (-1000..1000, x right, y up)
No filtering or dead zone here on purpose: the backend applies those per person.
"""

from __future__ import annotations

import argparse
import json
import os
import signal
import sys
import time

os.environ.setdefault("PYGAME_HIDE_SUPPORT_PROMPT", "1")
# Keep reading the controller while the terminal, not this script, has focus.
os.environ.setdefault("SDL_JOYSTICK_ALLOW_BACKGROUND_EVENTS", "1")
import paho.mqtt.client as mqtt  # noqa: E402
import pygame  # noqa: E402  (pygame-ce: plain pygame 2.6 drops controllers on macOS)

TOPIC_ROOT = "actspeed/v1/devices"
FW = "ps4-sim/0.1"
CHANGE_STEP = 20  # units; matches the protocol's suggested threshold
HEARTBEAT_S = 1.0


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--host", default="localhost", help="MQTT broker host (default: localhost)")
    p.add_argument("--port", type=int, default=1883, help="MQTT broker port (default: 1883)")
    p.add_argument("--username", default=os.getenv("MQTT_USERNAME"))
    p.add_argument("--password", default=os.getenv("MQTT_PASSWORD"))
    p.add_argument("--device", default="ps4-sim", help="device id (default: ps4-sim)")
    p.add_argument("--joystick", type=int, default=0, help="controller index if several are connected")
    p.add_argument("--axis-x", type=int, default=0, help="axis index for X (PS4 left stick: 0)")
    p.add_argument("--axis-y", type=int, default=1, help="axis index for Y (PS4 left stick: 1)")
    p.add_argument("--rate", type=float, default=50, help="max samples per second while moving (default: 50)")
    p.add_argument("--list", action="store_true", help="list controllers, then show live axis indexes")
    p.add_argument("-v", "--verbose", action="store_true", help="print every sample sent")
    return p.parse_args()


def open_joystick(index: int) -> pygame.joystick.JoystickType:
    pygame.init()
    pygame.joystick.init()
    count = pygame.joystick.get_count()
    if count == 0:
        sys.exit("No controller found. Pair it in Bluetooth settings (PS4: hold Share + PS until the light flashes).")
    if index >= count:
        sys.exit(f"Controller {index} not found; {count} connected (use --list).")
    return pygame.joystick.Joystick(index)


def list_mode(js: pygame.joystick.JoystickType) -> None:
    for i in range(pygame.joystick.get_count()):
        print(f"[{i}] {pygame.joystick.Joystick(i).get_name()}")
    print(f"\nUsing [{js.get_instance_id()}] {js.get_name()}: {js.get_numaxes()} axes")
    print("Move the left stick to see its axis indexes. Ctrl+C to quit.\n")
    clock = pygame.time.Clock()
    while True:
        for ev in pygame.event.get():
            if ev.type == pygame.JOYAXISMOTION and abs(ev.value) > 0.5:
                print(f"axis {ev.axis}: {ev.value:+.2f}")
        clock.tick(30)


def scale(v: float) -> int:
    return max(-1000, min(1000, round(v * 1000)))


def main() -> None:
    args = parse_args()
    js = open_joystick(args.joystick)
    if args.list:
        try:
            list_mode(js)
        except KeyboardInterrupt:
            return

    raw_topic = f"{TOPIC_ROOT}/{args.device}/raw"
    status_topic = f"{TOPIC_ROOT}/{args.device}/status"

    client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2, client_id=f"actspeed-sim-{args.device}",
                         protocol=mqtt.MQTTv5)
    if args.username:
        client.username_pw_set(args.username, args.password)
    client.will_set(status_topic, json.dumps({"status": "offline", "fw": FW}), qos=1, retain=True)

    def on_connect(c, _userdata, _flags, reason, _props):
        if reason.is_failure:
            print(f"MQTT connect failed: {reason}", file=sys.stderr)
            return
        c.publish(status_topic, json.dumps({"status": "online", "fw": FW}), qos=1, retain=True)
        print(f"Connected to {args.host}:{args.port} as device '{args.device}'")

    def on_disconnect(_c, _u, _f, reason, _p):
        if reason.value != 0:
            print(f"MQTT disconnected ({reason}), reconnecting…", file=sys.stderr)

    client.on_connect = on_connect
    client.on_disconnect = on_disconnect
    client.reconnect_delay_set(1, 10)
    try:
        client.connect(args.host, args.port, keepalive=30)
    except OSError as e:
        sys.exit(f"Cannot reach MQTT broker at {args.host}:{args.port}: {e}\n"
                 "Start it with: docker compose -f ../../deploy/docker-compose.yml up -d")
    client.loop_start()

    stop = False

    def handle_stop(*_):
        nonlocal stop
        stop = True

    signal.signal(signal.SIGINT, handle_stop)
    signal.signal(signal.SIGTERM, handle_stop)

    print(f"Controller: {js.get_name()}  (stick axes {args.axis_x}/{args.axis_y})")
    print("Left stick controls the joystick. Ctrl+C to stop.")

    start = time.monotonic()
    seq = 0
    last = None  # (x, y) last sent
    last_sent = 0.0
    min_interval = 1.0 / max(args.rate, 1)
    clock = pygame.time.Clock()

    while not stop:
        for ev in pygame.event.get():
            # macOS re-enumerates controllers (removed/added) at start-up and
            # on Bluetooth hiccups, so reattach instead of quitting.
            if ev.type == pygame.JOYDEVICEREMOVED and js is not None and ev.instance_id == js.get_instance_id():
                print("Controller lost, waiting for it to come back…", file=sys.stderr)
                js = None
            elif ev.type == pygame.JOYDEVICEADDED and js is None:
                if pygame.joystick.get_count() > args.joystick:
                    js = pygame.joystick.Joystick(args.joystick)
                    print(f"Controller back: {js.get_name()}")
        if js is None:
            # Centre the stick while no controller is there.
            if last is not None and last != (0, 0) and client.is_connected():
                seq += 1
                now = time.monotonic()
                client.publish(raw_topic, json.dumps({"seq": seq, "t": int((now - start) * 1000), "x": 0, "y": 0}))
                last, last_sent = (0, 0), now
            clock.tick(20)
            continue

        x = scale(js.get_axis(args.axis_x))
        y = -scale(js.get_axis(args.axis_y))  # pygame Y grows downwards; protocol Y is up
        now = time.monotonic()

        changed = last is None or abs(x - last[0]) >= CHANGE_STEP or abs(y - last[1]) >= CHANGE_STEP
        # Rate-limit movement samples.
        due = (changed and (last is None or now - last_sent >= min_interval)) \
            or now - last_sent >= HEARTBEAT_S
        if due and client.is_connected():
            seq += 1
            sample = {"seq": seq, "t": int((now - start) * 1000), "x": x, "y": y}
            client.publish(raw_topic, json.dumps(sample, separators=(",", ":")), qos=0)
            last, last_sent = (x, y), now
            if args.verbose:
                print(sample)
        clock.tick(200)

    print("Stopping…")
    client.publish(status_topic, json.dumps({"status": "offline", "fw": FW}), qos=1, retain=True).wait_for_publish(2)
    client.loop_stop()
    client.disconnect()
    pygame.quit()


if __name__ == "__main__":
    main()
