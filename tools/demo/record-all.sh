#!/usr/bin/env sh
# Record every demo GIF into docs/demo. Needs a Python with pyte and Pillow.
#
#   ./tools/demo/record-all.sh                 # every recording
#   ./tools/demo/record-all.sh review keep     # just those two
#
# Set DEMO_PYTHON to point at the interpreter to use; otherwise .venv/bin/python
# is preferred when it exists, then python3.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$root"

python=${DEMO_PYTHON:-}
if [ -z "$python" ]; then
	if [ -x .venv/bin/python ]; then
		python=.venv/bin/python
	else
		python=python3
	fi
fi

if ! "$python" -c 'import pyte, PIL' >/dev/null 2>&1; then
	echo "record-all.sh: $python has no pyte or Pillow." >&2
	echo "  python3 -m venv .venv && .venv/bin/pip install pyte pillow" >&2
	echo "  (or set DEMO_PYTHON to an interpreter that has them)" >&2
	exit 1
fi

if [ "$#" -eq 0 ]; then
	set -- --all
fi

go build -o bin/mailshear-demo ./tools/demo
exec "$python" tools/demo/record.py --binary bin/mailshear-demo --outdir docs/demo "$@"
