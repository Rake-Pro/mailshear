#!/usr/bin/env sh
# Build or install mailshear on Linux and macOS.
#   ./build.sh                 builds bin/mailshear
#   ./build.sh --install       go install so `mailshear` works from any shell;
#                              appends $(go env GOPATH)/bin to your shell rc if missing
#   ./build.sh --install --no-add-to-path   skip the shell rc edit
#   ./build.sh --test          go vet and go test
# mailshear is one interactive program: run it in a terminal and it walks you
# through the account setup on first start.
set -eu
cd "$(dirname "$0")"
export CGO_ENABLED=0
LDFLAGS="-s -w"

install=0 addpath=1 test=0
for a in "$@"; do
  case "$a" in
    --install) install=1 ;;
    --no-add-to-path) addpath=0 ;;
    --add-to-path) addpath=1 ;;
    --test) test=1 ;;
    -h|--help) sed -n '2,9p' "$0"; exit 0 ;;
    *) echo "unknown option: $a" >&2; exit 2 ;;
  esac
done

if [ "$test" = 1 ]; then
  go vet ./...
  go test ./...
  exit 0
fi

if [ "$install" = 1 ]; then
  # go run ./tools/install: go install, shell rc PATH entry, config bootstrap.
  if [ "$addpath" = 1 ]; then go run ./tools/install; else go run ./tools/install -no-add-to-path; fi
  exit $?
fi

mkdir -p bin
go build -trimpath -ldflags="$LDFLAGS" -o bin/mailshear ./cmd/mailshear
echo "built: bin/mailshear  (run: ./bin/mailshear)"
