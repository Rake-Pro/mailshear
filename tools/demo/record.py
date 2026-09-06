#!/usr/bin/env python3
"""Record the mailshear demo TUI as an animated GIF, with no VHS and no ffmpeg.

The program runs inside a pty, its output is parsed by pyte (a terminal
emulator in Python), and every snapshot of the pyte screen is drawn into a
PIL frame with a monospace font. Frames are de-duplicated, capped, and
written out as a GIF with per-frame durations.

    python3 tools/demo/record.py review
    python3 tools/demo/record.py --all

Run it with the virtualenv that has pyte and Pillow. tools/demo/record-all.sh
does that and produces every recording under docs/demo.
"""

import argparse
import fcntl
import os
import pty
import select
import shutil
import signal
import struct
import subprocess
import sys
import termios
import time

import pyte
from PIL import Image, ImageDraw, ImageFont

# ---------------------------------------------------------------- appearance

FONT_PATH = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"
FONT_BOLD_PATH = "/usr/share/fonts/truetype/dejavu/DejaVuSansMono-Bold.ttf"
FONT_SIZE = 15
MARGIN = 8

# pyte reports "default" for anything the program did not colour, so the two
# defaults below stand in for the terminal's own colours. They match what the
# TUI assumes when it picks its dark palette.
DEFAULT_FG = (0xE2, 0xDF, 0xD6)
DEFAULT_BG = (0x1A, 0x1B, 0x1E)

# The 16 ANSI colours, in the names pyte uses.
NAMED = {
    "black": (0x2B, 0x2D, 0x31),
    "red": (0xE0, 0x5C, 0x5C),
    "green": (0x5C, 0xB8, 0x5C),
    "brown": (0xC8, 0x9B, 0x3C),
    "blue": (0x5C, 0x8A, 0xE0),
    "magenta": (0xB4, 0x6C, 0xD8),
    "cyan": (0x4C, 0xB6, 0xC4),
    "white": (0xE2, 0xDF, 0xD6),
    "brightblack": (0x6B, 0x6F, 0x78),
    "brightred": (0xFF, 0x8A, 0x80),
    "brightgreen": (0x8A, 0xE0, 0x8A),
    "brightbrown": (0xF0, 0xC6, 0x60),
    "brightblue": (0x8A, 0xB4, 0xFF),
    "brightmagenta": (0xD8, 0x9A, 0xFF),
    "bfightmagenta": (0xD8, 0x9A, 0xFF),  # pyte spells the bright bg this way
    "brightcyan": (0x80, 0xE0, 0xE8),
    "brightwhite": (0xFF, 0xFF, 0xFF),
}


def resolve(color, default):
    if color == "default" or not color:
        return default
    if color in NAMED:
        return NAMED[color]
    if len(color) == 6:
        try:
            return (int(color[0:2], 16), int(color[2:4], 16), int(color[4:6], 16))
        except ValueError:
            return default
    return default


# ------------------------------------------------------------- pty sanitizer

# CSI final bytes pyte can dispatch. Anything else raises a KeyError inside
# pyte's stream, so it is dropped before the bytes are fed in.
CSI_OK = set(b"@ABCDEFGHJKLMPXadefghlmr`")
# Queries: pyte would answer some of these itself, badly. They are dropped and
# answered by the recorder instead.
CSI_QUERY = set(b"cn")
# Sequences pyte has no handler for but that move real content around, so the
# recorder applies them to the pyte screen itself: scroll up, scroll down and
# cursor backward tab.
CSI_MANUAL = {ord("S"): "su", ord("T"): "sd", ord("Z"): "cbt"}
# ESC <x> sequences pyte can dispatch.
ESC_OK = set(b"78cDEHM")


class Sanitizer:
    """Splits pty output into things the recorder knows how to apply.

    Returns (actions, replies, tail). An action is ("feed", bytes) for pyte or
    ("op", name, count) for a sequence pyte cannot dispatch but that still
    moves content. tail is an incomplete escape sequence, to be prepended to
    the next read.
    """

    def __init__(self):
        self.stripped = set()

    def feed(self, buf, cursor):
        actions = []
        out = bytearray()
        replies = bytearray()

        def flush():
            if out:
                actions.append(("feed", bytes(out)))
                del out[:]

        def done(tail):
            flush()
            return actions, bytes(replies), tail

        i, n = 0, len(buf)
        while i < n:
            b = buf[i]
            if b != 0x1B:
                out.append(b)
                i += 1
                continue
            if i + 1 >= n:
                return done(buf[i:])
            c = buf[i + 1]

            if c == 0x5B:  # CSI
                j = i + 2
                while j < n and 0x20 <= buf[j] <= 0x3F:
                    j += 1
                if j >= n:
                    return done(buf[i:])
                seq = buf[i : j + 1]
                final = buf[j]
                i = j + 1
                if final in CSI_MANUAL and b"?" not in bytes(seq):
                    flush()
                    try:
                        count = int(bytes(seq[2:-1]) or b"1")
                    except ValueError:
                        count = 1
                    actions.append(("op", CSI_MANUAL[final], max(count, 1)))
                    continue
                self._csi(seq, final, out, replies, cursor)
                continue

            if c == 0x5D:  # OSC, terminated by BEL or ST
                end, term = self._find_st(buf, i + 2)
                if end < 0:
                    return done(buf[i:])
                seq = buf[i:end]
                body = bytes(buf[i + 2 : end - term])
                if body.endswith(b"?") and (body.startswith(b"10;") or body.startswith(b"11;")):
                    self.stripped.add(body.decode("ascii", "replace"))
                    if body.startswith(b"11;"):
                        replies += b"\x1b]11;rgb:1a1a/1b1b/1e1e\x1b\\"
                    else:
                        replies += b"\x1b]10;rgb:e2e2/dfdf/d6d6\x1b\\"
                else:
                    out += seq
                i = end
                continue

            if c in (0x50, 0x58, 0x5E, 0x5F):  # DCS, SOS, PM, APC
                end, _ = self._find_st(buf, i + 2)
                if end < 0:
                    return done(buf[i:])
                self.stripped.add("ESC %s ... ST" % chr(c))
                i = end
                continue

            if c in (0x28, 0x29, 0x25, 0x23):  # charset selection, ESC # n
                if i + 2 >= n:
                    return done(buf[i:])
                out += buf[i : i + 3]
                i += 3
                continue

            if c in ESC_OK:
                out += buf[i : i + 2]
                i += 2
                continue

            self.stripped.add("ESC " + chr(c))
            i += 2
        return done(b"")

    def _csi(self, seq, final, out, replies, cursor):
        body = bytes(seq)
        if final in CSI_QUERY:
            self.stripped.add(body.decode("ascii", "replace"))
            if final == ord("c"):
                replies += b"\x1b[>0;10;1c" if b">" in body else b"\x1b[?62;22c"
            elif b"6" in body:
                replies += b"\x1b[%d;%dR" % (cursor[0] + 1, cursor[1] + 1)
            else:
                replies += b"\x1b[0n"
            return
        if final == ord("u"):  # kitty keyboard protocol
            self.stripped.add(body.decode("ascii", "replace"))
            if body == b"\x1b[?u":
                replies += b"\x1b[?0u"
            return
        if final in CSI_OK and b">" not in body and b"=" not in body and b"$" not in body:
            out += seq
            return
        self.stripped.add(body.decode("ascii", "replace"))

    @staticmethod
    def _find_st(buf, start):
        n = len(buf)
        j = start
        while j < n:
            if buf[j] == 0x07:
                return j + 1, 1
            if buf[j] == 0x1B:
                if j + 1 >= n:
                    return -1, 0
                if buf[j + 1] == 0x5C:
                    return j + 2, 2
            j += 1
        return -1, 0


# ----------------------------------------------------------------- recording

KEYS = {
    "enter": b"\r",
    "esc": b"\x1b",
    "tab": b"\t",
    "space": b" ",
    "backspace": b"\x7f",
    "up": b"\x1b[A",
    "down": b"\x1b[B",
    "right": b"\x1b[C",
    "left": b"\x1b[D",
    "home": b"\x1b[H",
    "end": b"\x1b[F",
    "pgdown": b"\x1b[6~",
    "pgup": b"\x1b[5~",
}


class Session:
    def __init__(self, argv, cols, rows, snap_ms=120):
        self.cols, self.rows = cols, rows
        self.snap_ms = snap_ms
        self.screen = pyte.Screen(cols, rows)
        self.stream = pyte.ByteStream(self.screen)
        self.san = Sanitizer()
        self.tail = b""
        self.frames = []  # (snapshot, timestamp)
        self.last_snap = 0.0
        # poster is the frame saved as the PNG. It defaults to the last frame,
        # which is wrong for a recording that ends by handing over to the next
        # screen, so those scripts call mark() on the frame worth keeping.
        self.poster = None

        self.master, slave = pty.openpty()
        fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        env = dict(os.environ)
        env.update(
            TERM="xterm-256color",
            COLORTERM="truecolor",
            LANG="C.UTF-8",
            LC_ALL="C.UTF-8",
            LINES=str(rows),
            COLUMNS=str(cols),
        )
        env.pop("NO_COLOR", None)
        env.pop("COLORFGBG", None)
        self.proc = subprocess.Popen(
            argv, stdin=slave, stdout=slave, stderr=slave, env=env, start_new_session=True
        )
        os.close(slave)
        self.start = time.monotonic()

    # -- terminal plumbing

    def pump(self, timeout):
        """Read whatever the program has written, up to timeout seconds."""
        r, _, _ = select.select([self.master], [], [], timeout)
        if not r:
            return
        try:
            data = os.read(self.master, 65536)
        except OSError:
            return
        if not data:
            return
        cursor = (self.screen.cursor.y, self.screen.cursor.x)
        actions, replies, self.tail = self.san.feed(self.tail + data, cursor)
        for action in actions:
            if action[0] == "feed":
                self.stream.feed(action[1])
            else:
                self.apply_op(action[1], action[2])
        if replies:
            os.write(self.master, bytes(replies))

    def apply_op(self, name, count):
        """Apply a sequence pyte cannot dispatch, on pyte's own buffer."""
        s = self.screen
        if name == "cbt":
            x = s.cursor.x
            for _ in range(count):
                stops = [t for t in s.tabstops if t < x]
                x = max(stops) if stops else 0
            s.cursor.x = x
            return
        top, bottom = s.margins or (0, s.lines - 1)
        for _ in range(count):
            if name == "su":
                for y in range(top, bottom):
                    s.buffer[y] = s.buffer[y + 1]
                s.buffer.pop(bottom, None)
            else:
                for y in range(bottom, top, -1):
                    s.buffer[y] = s.buffer[y - 1]
                s.buffer.pop(top, None)
        s.dirty.update(range(s.lines))

    def send(self, data):
        os.write(self.master, data)

    def snapshot(self):
        buf = self.screen.buffer
        out = []
        for y in range(self.rows):
            line = buf[y]
            row = []
            for x in range(self.cols):
                ch = line[x]
                row.append((ch.data, ch.fg, ch.bg, ch.bold, ch.reverse))
            out.append(tuple(row))
        return tuple(out)

    def capture(self, force=False):
        now = time.monotonic()
        if not force and (now - self.last_snap) * 1000 < self.snap_ms:
            return
        self.last_snap = now
        self.frames.append((self.snapshot(), now - self.start))

    def mark(self):
        """Keep this moment as the recording's still image."""
        self.pump(0)
        self.poster = self.snapshot()

    def wait(self, ms):
        """Run the terminal for ms milliseconds, snapshotting as it goes."""
        end = time.monotonic() + ms / 1000.0
        while True:
            now = time.monotonic()
            if now >= end:
                break
            self.pump(min(0.02, end - now))
            self.capture()
        self.pump(0)
        self.capture()

    def wait_for(self, needle, timeout_ms=12000, then_ms=0):
        """Run until the text shows up on screen, then wait a beat.

        Screens hand over on their own once the scripted backend finishes, and
        a recording under load takes longer than one running free, so the
        scripts wait on what is on screen rather than on the clock. Keys sent
        to a screen that has not arrived yet are simply dropped.
        """
        end = time.monotonic() + timeout_ms / 1000.0
        while time.monotonic() < end:
            self.pump(0.02)
            self.capture()
            if any(needle in line for line in self.screen.display):
                break
        else:
            raise RuntimeError("timed out waiting for %r" % needle)
        if then_ms:
            self.wait(then_ms)

    def type(self, text, per_char_ms=85):
        for ch in text:
            self.send(ch.encode())
            self.wait(per_char_ms)

    def key(self, name, after_ms=350):
        self.send(KEYS.get(name, name.encode()))
        self.wait(after_ms)

    def close(self):
        try:
            self.proc.send_signal(signal.SIGTERM)
            self.proc.wait(timeout=3)
        except Exception:
            try:
                self.proc.kill()
            except Exception:
                pass
        try:
            os.close(self.master)
        except OSError:
            pass


# ------------------------------------------------------------------ frames

class Renderer:
    def __init__(self, cols, rows):
        self.font = ImageFont.truetype(FONT_PATH, FONT_SIZE)
        self.bold = ImageFont.truetype(FONT_BOLD_PATH, FONT_SIZE)
        ascent, descent = self.font.getmetrics()
        self.cw = int(round(self.font.getlength("M")))
        self.ch = ascent + descent + 1
        self.baseline = ascent + 1
        self.cols, self.rows = cols, rows
        self.size = (cols * self.cw + 2 * MARGIN, rows * self.ch + 2 * MARGIN)

    def render(self, snap):
        img = Image.new("RGB", self.size, DEFAULT_BG)
        d = ImageDraw.Draw(img)
        for y, row in enumerate(snap):
            top = MARGIN + y * self.ch
            # Paint background runs first so a styled cell keeps its full box.
            x = 0
            while x < self.cols:
                _, fg, bg, _, rev = row[x]
                col = resolve(fg, DEFAULT_FG) if rev else resolve(bg, DEFAULT_BG)
                run = x + 1
                while run < self.cols:
                    _, f2, b2, _, r2 = row[run]
                    c2 = resolve(f2, DEFAULT_FG) if r2 else resolve(b2, DEFAULT_BG)
                    if c2 != col:
                        break
                    run += 1
                if col != DEFAULT_BG:
                    d.rectangle(
                        [
                            MARGIN + x * self.cw,
                            top,
                            MARGIN + run * self.cw - 1,
                            top + self.ch - 1,
                        ],
                        fill=col,
                    )
                x = run
            # Then the glyphs, one run of identical style at a time.
            x = 0
            while x < self.cols:
                data, fg, bg, bold, rev = row[x]
                style = (fg, bg, bold, rev)
                run, text = x, ""
                while run < self.cols:
                    d2 = row[run]
                    if (d2[1], d2[2], d2[3], d2[4]) != style:
                        break
                    text += d2[0]
                    run += 1
                if text.strip():
                    col = resolve(bg, DEFAULT_BG) if rev else resolve(fg, DEFAULT_FG)
                    d.text(
                        (MARGIN + x * self.cw, top + self.baseline),
                        text,
                        font=self.bold if bold else self.font,
                        fill=col,
                        anchor="ls",
                    )
                x = run
        return img


def dedupe(frames, tail_ms):
    """Collapse repeated snapshots into (snapshot, duration_ms) pairs."""
    out = []
    for i, (snap, ts) in enumerate(frames):
        end = frames[i + 1][1] if i + 1 < len(frames) else ts + tail_ms / 1000.0
        dur = max(int(round((end - ts) * 1000)), 20)
        if out and out[-1][0] == snap:
            out[-1][1] += dur
        else:
            out.append([snap, dur])
    return out


def cap_frames(frames, limit):
    """Drop evenly spaced frames until the count fits, keeping the ends."""
    while len(frames) > limit:
        drop = len(frames) - limit
        stride = max(len(frames) // (drop + 1), 2)
        kept = []
        for i, f in enumerate(frames):
            if i not in (0, len(frames) - 1) and i % stride == 0 and len(frames) - len(kept) > 0:
                if kept:
                    kept[-1][1] += f[1]
                    continue
            kept.append(f)
        if len(kept) == len(frames):
            break
        frames = kept
    return frames


def palette_for(images):
    colors = set()
    for im in images:
        colors.update(c for _, c in im.getcolors(maxcolors=1 << 20))
    colors = sorted(colors)[:256]
    pal = []
    for c in colors:
        pal.extend(c)
    pal.extend([0, 0, 0] * (256 - len(colors)))
    p = Image.new("P", (1, 1))
    p.putpalette(pal)
    return p


def write_gif(images, durations, path):
    pal = palette_for(images)
    quantized = [im.quantize(palette=pal, dither=Image.Dither.NONE) for im in images]
    quantized[0].save(
        path,
        save_all=True,
        append_images=quantized[1:],
        duration=durations,
        loop=0,
        optimize=True,
        disposal=1,
    )


# ------------------------------------------------------------------ scripts

def s_setup(t):
    t.wait(900)
    t.key("tab", 450)     # the form opens on the account name; the address is next
    t.type("casey@gmail.com")
    t.wait(1100)
    t.key("tab", 500)     # the provider picker, on auto-detect
    t.key("right", 800)   # cycled by hand: Gmail / Google Workspace
    t.key("left", 900)    # and back to auto-detect, which found the same thing
    t.key("tab", 500)     # the auth method: Gmail offers OAuth as well
    t.key("right", 900)   # OAuth sign-in, which asks for a client id instead
    t.key("left", 800)    # and back to the app password
    t.key("tab", 500)     # the app password field
    t.type("wxyz-abcd-efgh-ijkl", 70)
    t.wait(1200)
    t.mark()              # the filled-in form is the still worth keeping
    t.key("enter", 900)


def s_scan(t):
    t.wait(1650)          # connect, then the bar starts filling
    t.mark()              # a half-filled bar is the still worth keeping
    t.wait_for("mailshear review")
    t.wait(1800)          # the review screen it hands over to


def s_review(t):
    t.wait_for("mailshear review", then_ms=700)   # the scan runs first, at -speed 3
    for _ in range(8):
        t.key("down", 190)
    t.wait(700)
    t.key("space", 700)   # mark the brand row for unsubscribe
    t.key("d", 900)       # and for delete
    t.key("enter", 1300)  # expand the five senders behind it
    t.key("enter", 900)   # and collapse them again
    t.send(b"/")
    t.wait(500)
    t.type("linked", 130)
    t.wait(1200)
    t.key("esc", 900)
    t.key("down", 400)
    t.key("enter", 1600)  # detail pane for one sender
    t.send(b"?")
    t.wait(1900)          # the key overlay
    t.send(b"?")
    t.wait(700)
    t.key("esc", 1200)


def s_accounts(t):
    t.wait(1200)          # the switcher: both mailboxes, credentials and counts
    t.key("down", 1000)   # the work account, signed in with OAuth
    t.key("up", 900)
    t.mark()              # the list is the still worth keeping
    t.key("enter", 600)   # take the personal mailbox on to the scan
    t.wait_for("mailshear review", then_ms=1200)


def s_confirm_apply(t):
    t.wait_for("mailshear review", then_ms=700)
    t.key("down", 160)
    t.key("down", 260)
    t.key("space", 400)
    t.key("d", 800)       # PayPal: unsubscribe, delete matched, receipts kept back
    for _ in range(6):
        t.key("down", 150)
    t.key("space", 500)
    t.key("d", 900)       # the brand row: unsubscribe and delete
    t.key("w", 300)
    t.wait_for("about to apply", then_ms=2200)
    t.key("enter", 300)   # apply: results stream in, then the delete bar
    t.wait_for("manual follow-up", timeout_ms=25000, then_ms=1800)
    t.send(b"u")
    t.wait(1600)          # the undo prompt
    t.send(b"n")
    t.wait(1200)


def s_keep(t):
    t.wait_for("mailshear review", then_ms=700)   # the scan runs first, at -speed 3
    t.key("down", 220)
    t.key("down", 900)    # the PayPal row in the keep section
    t.key("d", 1500)      # kept counts appear in the status line
    t.send(b"K")
    t.wait(1800)          # the include-kept confirmation
    t.send(b"y")
    t.wait(1800)          # the [d!] marker
    t.send(b"K")
    t.wait(1600)          # and cleared again


# name -> (which screen the demo starts on, the script, columns, rows, the
# -speed the demo runs its scripted delays at). Recordings that only pass
# through the scan on the way to the review run it fast; the ones that are
# about the scan or the apply run it at the pace a real one moves.
SCRIPTS = {
    "accounts": ("accounts", s_accounts, 100, 30, 3.0),
    "setup": ("setup", s_setup, 100, 30, 1.0),
    "scan": ("flow", s_scan, 100, 30, 1.0),
    "review": ("flow", s_review, 100, 30, 3.0),
    "confirm-apply": ("flow", s_confirm_apply, 100, 30, 1.0),
    "keep": ("flow", s_keep, 100, 30, 3.0),
}


def record(name, outdir, binary, speed, cap, tail_ms=2500):
    screen, script, cols, rows, default_speed = SCRIPTS[name]
    if speed is None:
        speed = default_speed
    argv = [binary, "-screen", screen, "-speed", str(speed)]
    t = Session(argv, cols, rows)
    try:
        t.capture(force=True)
        script(t)
        t.capture(force=True)
    finally:
        t.close()

    frames = cap_frames(dedupe(t.frames, tail_ms), cap)
    r = Renderer(cols, rows)
    images = [r.render(f[0]) for f in frames]
    durations = [f[1] for f in frames]
    poster = r.render(t.poster) if t.poster is not None else images[-1]

    os.makedirs(outdir, exist_ok=True)
    gif = os.path.join(outdir, name + ".gif")
    png = os.path.join(outdir, name + ".png")
    write_gif(images, durations, gif)
    poster.save(png)

    size = os.path.getsize(gif)
    print(
        "%-14s %3d frames  %dx%d  %6.1f KB  %s"
        % (name, len(frames), r.size[0], r.size[1], size / 1024.0, gif)
    )
    if t.san.stripped:
        print("               stripped: " + ", ".join(sorted(repr(s) for s in t.san.stripped)))
    return size


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("names", nargs="*", help="recordings to make: " + ", ".join(SCRIPTS))
    ap.add_argument("--all", action="store_true", help="make every recording")
    ap.add_argument("--outdir", default="docs/demo")
    ap.add_argument("--binary", default="", help="the built demo binary (default: go run)")
    ap.add_argument(
        "--speed", type=float, default=None,
        help="override the per-recording speed multiplier for the demo's delays",
    )
    ap.add_argument("--cap", type=int, default=150, help="maximum frames per GIF")
    args = ap.parse_args()

    names = list(SCRIPTS) if args.all or not args.names else args.names
    for n in names:
        if n not in SCRIPTS:
            sys.exit("unknown recording %r" % n)

    binary = args.binary
    if not binary:
        binary = shutil.which("mailshear-demo") or ""
    if not binary:
        sys.exit("pass --binary: build it with `go build -o /tmp/mailshear-demo ./tools/demo`")

    for n in names:
        record(n, args.outdir, binary, args.speed, args.cap)


if __name__ == "__main__":
    main()
