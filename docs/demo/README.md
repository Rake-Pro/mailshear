# Demo recordings

The GIFs here are recorded from the real TUI driven against `tools/demo`, a scripted fake backend with a synthetic mailbox. Nothing in them touches a mail server.

- Prerequisites: Go 1.27, a Python 3 with [pyte](https://pypi.org/project/pyte/) and [Pillow](https://pypi.org/project/pillow/) (`python3 -m venv .venv && .venv/bin/pip install pyte pillow`), and DejaVu Sans Mono (`fonts-dejavu-core`; change `FONT_PATH` in `record.py` for another font). No VHS, no ffmpeg, no asciinema.
- Everything: `make demos`, or `./tools/demo/record-all.sh`. Set `DEMO_PYTHON` to pick the interpreter; otherwise `.venv/bin/python` is used when it exists, then `python3`.
- One at a time: `./tools/demo/record-all.sh review keep`. The names are the keys of `SCRIPTS` in `record.py`, one function each.
- Watch it by hand: `go run ./tools/demo -screen flow` (starts at the scan), `-screen setup` (the form) or `-screen accounts` (the switcher). `-speed 2` runs the scripted delays twice as fast.
- Output is `<name>.gif` plus `<name>.png`, the still the README falls back to. Check both after a UI change: a script that walks past a field the form no longer has records the wrong screen without failing.
