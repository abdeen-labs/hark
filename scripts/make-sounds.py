#!/usr/bin/env python3
"""Generate Hark's bundled notification tones."""

import math
import subprocess
import sys
import tempfile
import wave
from pathlib import Path

RATE = 44100
PEAK = 10 ** (-3 / 20)

# (frequency ratio, amplitude, decay multiplier, detune)
BAR = [
    (1.0, 1.00, 1.0, 0.0018),
    (2.76, 0.42, 1.8, 0.0026),
    (5.40, 0.15, 2.6, 0.0031),
    (8.93, 0.05, 3.4, 0.0),
]

# Bell spectrum.
BELL = [
    (0.5, 0.28, 0.7, 0.0009),
    (1.0, 1.00, 1.0, 0.0013),
    (1.19, 0.20, 1.5, 0.0),
    (2.0, 0.55, 1.6, 0.0021),
    (2.74, 0.22, 2.1, 0.0),
    (3.0, 0.28, 2.2, 0.0028),
    (4.2, 0.14, 3.0, 0.0),
    (5.4, 0.07, 3.8, 0.0),
]

# Glass spectrum.
GLASS = [
    (1.0, 1.00, 1.0, 0.0022),
    (3.01, 0.30, 2.0, 0.0034),
    (6.15, 0.10, 3.2, 0.0),
]


def noise(seed):
    state = seed & 0xFFFFFFFF
    while True:
        state = (state * 1664525 + 1013904223) & 0xFFFFFFFF
        yield state / 2147483648.0 - 1.0


def strike(buf, at, freq, tau, level=1.0, partials=BAR, tick=0.12, tick_hz=4200.0):
    attack = 0.003
    bend, bend_tau = 0.004, 0.025
    start = int(at * RATE)
    # Stop below -80 dB.
    end = min(len(buf), start + int(9.2 * tau * RATE))
    for i in range(start, end):
        t = (i - start) / RATE
        env = min(t / attack, 1.0)
        settle = t + bend * bend_tau * (1.0 - math.exp(-t / bend_tau))
        sample = 0.0
        for ratio, amp, decay, detune in partials:
            w = 2 * math.pi * freq * ratio
            sample += (
                amp
                * math.exp(-t * decay / tau)
                * math.sin(w * settle)
                * (math.cos(w * detune * 0.5 * t) if detune else 1.0)
            )
        buf[i] += level * env * sample

    if tick <= 0:
        return
    tick_len = int(0.006 * RATE)
    alpha = 1.0 - math.exp(-2 * math.pi * tick_hz / RATE)
    lp = 0.0
    source = noise(int(freq * 1000) ^ int(at * 100000) ^ 0x5EED)
    for i in range(start, min(len(buf), start + tick_len)):
        t = (i - start) / RATE
        lp += alpha * (next(source) - lp)
        buf[i] += level * tick * lp * math.exp(-t / 0.002)


def buffer(seconds):
    return [0.0] * int(seconds * RATE)


def relay():
    buf = buffer(1.4)
    strike(buf, 0.00, 880.00, 0.32, level=0.85, tick=0.10)
    strike(buf, 0.16, 1318.51, 0.42, tick=0.10)
    return buf


def semaphore():
    buf = buffer(1.5)
    strike(buf, 0.00, 1046.50, 0.26, level=0.85, tick=0.09)
    strike(buf, 0.13, 1318.51, 0.26, level=0.9, tick=0.09)
    strike(buf, 0.26, 1567.98, 0.45, tick=0.09)
    return buf


def beacon():
    buf = buffer(2.2)
    strike(buf, 0.00, 329.63, 0.95, partials=BELL, tick=0.06, tick_hz=1800.0)
    return buf


def lattice():
    buf = buffer(1.6)
    strike(buf, 0.00, 1046.50, 0.20, level=0.85, tick=0.08)
    strike(buf, 0.10, 1318.51, 0.20, level=0.85, tick=0.08)
    strike(buf, 0.20, 1567.98, 0.20, level=0.9, tick=0.08)
    strike(buf, 0.30, 1318.51, 0.45, tick=0.08)
    return buf


def meridian():
    buf = buffer(1.8)
    strike(buf, 0.00, 293.66, 0.70, partials=BELL, tick=0.05, tick_hz=1800.0)
    strike(buf, 0.18, 1174.66, 0.45, level=0.65, tick=0.08)
    return buf


def pulse():
    buf = buffer(1.1)
    strike(buf, 0.00, 1760.00, 0.08, tick=0.20)
    strike(buf, 0.09, 1760.00, 0.26, tick=0.20)
    return buf


def aperture():
    buf = buffer(1.1)
    strike(buf, 0.00, 1975.53, 0.10, level=0.8, partials=GLASS, tick=0.14, tick_hz=6000.0)
    strike(buf, 0.12, 1567.98, 0.32, partials=GLASS, tick=0.14, tick_hz=6000.0)
    return buf


def filament():
    buf = buffer(1.3)
    strike(buf, 0.000, 2093.00, 0.05, level=0.5, partials=GLASS, tick=0.06, tick_hz=7000.0)
    strike(buf, 0.055, 2093.00, 0.07, level=0.7, partials=GLASS, tick=0.06, tick_hz=7000.0)
    strike(buf, 0.110, 2093.00, 0.50, partials=GLASS, tick=0.06, tick_hz=7000.0)
    return buf


def gantry():
    buf = buffer(2.2)
    strike(buf, 0.00, 220.00, 1.00, partials=BELL, tick=0.05, tick_hz=1500.0)
    strike(buf, 0.22, 293.66, 0.80, level=0.8, partials=BELL, tick=0.05, tick_hz=1500.0)
    return buf


def sonar():
    buf = buffer(2.0)
    strike(buf, 0.00, 1244.51, 0.50, tick=0.08)
    strike(buf, 0.42, 1244.51, 0.45, level=0.28, tick=0.0)
    strike(buf, 0.84, 1244.51, 0.40, level=0.10, tick=0.0)
    return buf


TONES = [
    ("relay", relay),
    ("semaphore", semaphore),
    ("beacon", beacon),
    ("lattice", lattice),
    ("meridian", meridian),
    ("pulse", pulse),
    ("aperture", aperture),
    ("filament", filament),
    ("gantry", gantry),
    ("sonar", sonar),
]


def finish(buf):
    peak = max(abs(s) for s in buf)
    drive = 1.2
    knee = math.tanh(drive)
    alpha = 1.0 - math.exp(-2 * math.pi * 11000.0 / RATE)
    lp = 0.0
    out = []
    for s in buf:
        v = math.tanh(drive * s / peak) / knee
        lp += alpha * (v - lp)
        out.append(lp)
    peak = max(abs(s) for s in out)
    return [s * PEAK / peak for s in out]


def write_wav(path, buf):
    # Fade the tail to avoid a click.
    fade = int(0.030 * RATE)
    frames = bytearray()
    for i, s in enumerate(buf):
        remaining = len(buf) - i
        if remaining < fade:
            s *= remaining / fade
        frames += int(round(max(-1.0, min(1.0, s)) * 32767)).to_bytes(2, "little", signed=True)
    with wave.open(str(path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(RATE)
        w.writeframes(bytes(frames))


def main():
    out = Path(__file__).resolve().parent.parent / "ios" / "Hark" / "Sounds"
    out.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory() as tmp:
        for stem, build in TONES:
            buf = finish(build())
            wav = Path(tmp) / f"{stem}.wav"
            write_wav(wav, buf)
            caf = out / f"{stem}.caf"
            subprocess.run(
                ["afconvert", "-f", "caff", "-d", "LEI16@44100", "-c", "1", str(wav), str(caf)],
                check=True,
            )
            print(f"{caf.name}  {len(buf) / RATE:.2f}s")
    return 0


if __name__ == "__main__":
    sys.exit(main())
