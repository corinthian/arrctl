#!/usr/bin/env bash
# Build the universal macOS arrctl binary (arm64 + amd64) plus per-arch
# artifacts. Ad-hoc codesign; notarization is a later concern.
#
# This script builds. It does not deploy: copying dist/arrctl onto PATH is a
# deliberate, separate `cp`, the same as traktctl and plexctl.
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${1:-$(grep -m1 'Version =' internal/commands/app.go | cut -d'"' -f2)}"
mkdir -p dist

GOVULNCHECK="$(command -v govulncheck || true)"
if [ -z "$GOVULNCHECK" ] && [ -x "$(go env GOPATH)/bin/govulncheck" ]; then
  GOVULNCHECK="$(go env GOPATH)/bin/govulncheck"
fi
if [ -z "$GOVULNCHECK" ]; then
  echo "[build] govulncheck not found; installing..."
  go install golang.org/x/vuln/cmd/govulncheck@latest
  GOVULNCHECK="$(go env GOPATH)/bin/govulncheck"
fi
echo "[build] govulncheck ./..."
"$GOVULNCHECK" ./...

echo "[build] go test ./..."
go test ./...

echo "[build] arrctl ${VERSION}"
GOOS=darwin GOARCH=arm64 go build -trimpath -o dist/arrctl-arm64 ./cmd/arrctl
GOOS=darwin GOARCH=amd64 go build -trimpath -o dist/arrctl-amd64 ./cmd/arrctl

lipo -create -output dist/arrctl dist/arrctl-arm64 dist/arrctl-amd64
codesign --sign - --options runtime --force dist/arrctl

echo "[build] universal binary:"
lipo -info dist/arrctl

# Toolchain hygiene: every shipped artefact must be built by a compiler at least
# as new as go.mod's `go` directive, so a stale local toolchain cannot silently
# ship an old (potentially vulnerable) stdlib.
required_go="$(grep -m1 '^go ' go.mod | awk '{print $2}')"
for bin in dist/arrctl-arm64 dist/arrctl-amd64 dist/arrctl; do
  built_go="$(go version "$bin" | awk '{print $2}' | sed 's/^go//')"
  newest="$(printf '%s\n%s\n' "$required_go" "$built_go" | sort -V | tail -1)"
  if [ "$newest" != "$built_go" ]; then
    echo "[build] FATAL: $bin built with go${built_go}, older than go.mod's required go${required_go}" >&2
    exit 1
  fi
  echo "[build] $bin: go${built_go} (>= go${required_go} required) OK"
done
