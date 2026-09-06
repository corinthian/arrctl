#!/usr/bin/env bash
# Six packages under internal/ are shared verbatim with traktctl and plexctl.
# They are copied, not vendored, because a module boundary is the thing this
# refactor is deliberately not paying for yet. A copy drifts silently, so this
# script says so out loud.
#
# A sibling that does not have a package is skipped, not failed: the copies
# land repository by repository, and two of the six are never going everywhere
# (rawjson is arrctl and traktctl only; configpath is arrctl and traktctl
# only). A skip therefore means "not compared", never "compared and matched".
set -euo pipefail

cd "$(dirname "$0")/.."
root="$(pwd)"

packages=(cause xduration xhttp rawjson atomicfile configpath)
# Every repo in the suite except this one. Derived, not listed, so this file
# is byte-identical in all three checkouts.
self="$(basename "$root")"
siblings=()
for r in arrctl traktctl plexctl; do
  [ "$r" != "$self" ] && siblings+=("../$r")
done

drift=0
compared=0
skipped=0

for sibling in "${siblings[@]}"; do
  if [ ! -d "$root/$sibling" ]; then
    echo "skip: $sibling is not checked out"
    skipped=$((skipped + ${#packages[@]}))
    continue
  fi
  for pkg in "${packages[@]}"; do
    theirs="$root/$sibling/internal/$pkg"
    if [ ! -d "$root/internal/$pkg" ]; then
      echo "skip: this repo has no internal/$pkg"
      skipped=$((skipped + 1))
      continue
    fi
    if [ ! -d "$theirs" ]; then
      echo "skip: $sibling has no internal/$pkg"
      skipped=$((skipped + 1))
      continue
    fi
    compared=$((compared + 1))
    # xhttp imports internal/cause by module path, and each repo has its own
    # module path, so that one line legitimately differs. Normalise it before
    # diffing; nothing else is normalised.
    mine_n="$(mktemp -d)"; theirs_n="$(mktemp -d)"
    cp -R "$root/internal/$pkg/." "$mine_n/"; cp -R "$theirs/." "$theirs_n/"
    for f in "$mine_n"/*.go "$theirs_n"/*.go; do
      [ -f "$f" ] && sed -i '' 's#github.com/corinthian/[a-z]*/internal/#MODULE/internal/#g' "$f"
    done
    if diff -ru "$mine_n" "$theirs_n"; then
      echo "ok: internal/$pkg matches $sibling"
    else
      echo "DRIFT: internal/$pkg differs from $sibling/internal/$pkg"
      drift=1
    fi
    rm -rf "$mine_n" "$theirs_n"
  done
done

echo "compared $compared, skipped $skipped"
if [ "$drift" -ne 0 ]; then
  echo "check-shared.sh: shared packages have drifted" >&2
  exit 1
fi
exit 0
