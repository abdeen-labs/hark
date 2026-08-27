#!/usr/bin/env python3
"""Synthesize Hark's bundled notification tones into ios/Hark/Sounds/.

Each tone is a struck-bar chime — a few sine partials with a fast attack and
an exponential decay — rendered as 44.1 kHz 16-bit mono, normalized to
-3 dBFS, and converted to CAF with afconvert. Output is deterministic: the
same script produces the same bytes.

    python3 scripts/make-sounds.py
"""

import math
import subprocess
import sys
import tempfile
import wave
from pathlib import Path

RATE = 44100
PEAK = 10 ** (-3 / 20)

# A struck bar's spectrum: (frequency ratio, level, decay speed relative to
# the fundamental's). The inharmonic ratios are what make it read as metal
# rather than an organ.
BAR = [
    (1.0, 1.0, 1.0),
    (2.76, 0.40, 1.8),
    (5.40, 0.14, 2.6),
]

# A bell's spectrum: a hum tone below the strike tone and near-harmonic
# partials above it.
BELL = [
    (0.5, 0.30, 0.7),
    (1.0, 1.0, 1.0),
    (2.0, 0.55, 1.6),
    (3.0, 0.30, 2.2),
    (4.2, 0.16, 3.0),
    (5.4, 0.08, 3.8),
]


def strike(buf, at, freq, tau, level=1.0, partials=BAR):
    """Add one strike at `at` seconds: a 4 ms attack, then each partial
    decaying with time constant tau over its decay speed."""
    attack = 0.004
    start = int(at * RATE)
    # exp(-9.2) < 1e-4: past that the strike is inaudible.
    end = min(len(buf), start + int(9.2 * tau * RATE))
    for i in range(start, end):
        t = (i - start) / RATE
        env = min(t / attack, 1.0)
        sample = 0.0
        for ratio, amp, decay in partials:
            sample += amp * math.exp(-t * decay / tau) * math.sin(2 * math.pi * freq * ratio * t)
        buf[i] += level * env * sample


def buffer(seconds):
    return [0.0] * int(seconds * RATE)


def relay():
    """Two quick strikes a perfect fifth apart."""
    buf = buffer(1.4)
    strike(buf, 0.00, 880.00, 0.32)
    strike(buf, 0.16, 1318.51, 0.42)
    return buf


def semaphore():
    """Three ascending strikes."""
    buf = buffer(1.5)
    strike(buf, 0.00, 1046.50, 0.28)
    strike(buf, 0.14, 1318.51, 0.28)
    strike(buf, 0.28, 1567.98, 0.45)
    return buf


def beacon():
    """One deep bell, long decay."""
    buf = buffer(2.0)
    strike(buf, 0.00, 329.63, 0.85, partials=BELL)
    return buf


def lattice():
    """A small up-down arpeggio."""
    buf = buffer(1.6)
    strike(buf, 0.00, 1046.50, 0.22, level=0.9)
    strike(buf, 0.11, 1318.51, 0.22, level=0.9)
    strike(buf, 0.22, 1567.98, 0.22, level=0.9)
    strike(buf, 0.33, 1318.51, 0.40)
    return buf


def meridian():
    """A low-high dyad, two octaves apart."""
    buf = buffer(1.7)
    strike(buf, 0.00, 293.66, 0.65, partials=BELL)
    strike(buf, 0.18, 1174.66, 0.45, level=0.7)
    return buf


def pulse():
    """A short, higher double tick."""
    buf = buffer(1.2)
    strike(buf, 0.00, 1760.00, 0.09)
    strike(buf, 0.09, 1760.00, 0.28)
    return buf


TONES = [
    ("relay", relay),
    ("semaphore", semaphore),
    ("beacon", beacon),
    ("lattice", lattice),
    ("meridian", meridian),
    ("pulse", pulse),
]


def write_wav(path, buf):
    peak = max(abs(s) for s in buf)
    scale = PEAK / peak
    # A 30 ms fade at the tail so the file never ends on a click.
    fade = int(0.030 * RATE)
    frames = bytearray()
    for i, s in enumerate(buf):
        v = s * scale
        remaining = len(buf) - i
        if remaining < fade:
            v *= remaining / fade
        frames += int(round(max(-1.0, min(1.0, v)) * 32767)).to_bytes(2, "little", signed=True)
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
            buf = build()
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
