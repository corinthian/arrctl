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
siblings=(../traktctl ../plexctl)

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
    if [ ! -d "$theirs" ]; then
      echo "skip: $sibling has no internal/$pkg"
      skipped=$((skipped + 1))
      continue
    fi
    compared=$((compared + 1))
    if diff -ru "$root/internal/$pkg" "$theirs"; then
      echo "ok: internal/$pkg matches $sibling"
    else
      echo "DRIFT: internal/$pkg differs from $sibling/internal/$pkg"
      drift=1
    fi
  done
done

echo "compared $compared, skipped $skipped"
if [ "$drift" -ne 0 ]; then
  echo "check-shared.sh: shared packages have drifted" >&2
  exit 1
fi
exit 0
