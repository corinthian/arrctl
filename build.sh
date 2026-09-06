#!/usr/bin/env bash
# Build the universal macOS arrctl binary (arm64 + amd64) plus per-arch
# artifacts. Ad-hoc codesign; notarization is a later concern.
#
# This script builds. It does not deploy: copying dist/arrctl onto PATH is a
# deliberate, separate `cp`, the same as traktctl and plexctl.
#
# Version resolution, highest first:
#   1. The positional argument.
#   2. $ARRCTL_BUILD_VERSION.
#   3. The exact tag on HEAD, if there is one (leading "v" stripped).
#   4. internal/commands/app.go's own `var Version` default.
# Every source empty fails loudly rather than stamping an empty version.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist

VERSION="${1:-}"
if [ -z "${VERSION}" ]; then
  VERSION="${ARRCTL_BUILD_VERSION:-}"
fi
if [ -z "${VERSION}" ]; then
  # The one legitimate `|| true` here: a non-git export (or an untagged
  # commit) must not kill the build under `set -e`.
  VERSION="$(git describe --tags --exact-match 2>/dev/null || true)"
  VERSION="${VERSION#v}"
fi
if [ -z "${VERSION}" ]; then
  VERSION="$(sed -n 's/^var Version = "\(.*\)"$/\1/p' internal/commands/app.go)"
fi
if [ -z "${VERSION}" ]; then
  echo "build.sh: could not determine a version — no positional argument, no \$ARRCTL_BUILD_VERSION, no exact tag, and no 'var Version = \"...\"' in internal/commands/app.go" >&2
  exit 1
fi

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

echo "[build] go vet ./..."
go vet ./...

echo "[build] gofmt -l internal cmd"
GOFMT_OUT="$(gofmt -l internal cmd)"
if [ -n "$GOFMT_OUT" ]; then
  echo "[build] FATAL: gofmt found unformatted files:" >&2
  echo "$GOFMT_OUT" >&2
  exit 1
fi

echo "[build] go test ./..."
go test ./...

echo "[build] go test -race ./..."
go test -race ./...

echo "[build] go mod tidy -diff"
if ! go mod tidy -diff; then
  echo "[build] FATAL: go mod tidy -diff reports a change; run go mod tidy and commit it" >&2
  exit 1
fi

echo "[build] arrctl ${VERSION}"
LDFLAGS="-X github.com/corinthian/arrctl/internal/commands.Version=${VERSION}"
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="${LDFLAGS}" -o dist/arrctl-arm64 ./cmd/arrctl
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="${LDFLAGS}" -o dist/arrctl-amd64 ./cmd/arrctl

lipo -create -output dist/arrctl dist/arrctl-arm64 dist/arrctl-amd64
codesign --sign - --options runtime --force dist/arrctl

echo "[build] universal binary:"
lipo -info dist/arrctl

# Prove the stamp landed. Exact string equality against the whole --version
# line, not containment: a prefix match would let "arrctl version 1.2.0-dev"
# pass for a build that asked for "1.2.0".
REPORTED="$(dist/arrctl --version)"
EXPECTED="arrctl version ${VERSION}"
if [ "$REPORTED" != "$EXPECTED" ]; then
  echo "build.sh: binary reports '${REPORTED}', expected '${EXPECTED}'" >&2
  exit 1
fi
echo "[build] $REPORTED"

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
