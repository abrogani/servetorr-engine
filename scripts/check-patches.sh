#!/usr/bin/env bash
set -uo pipefail

[ $# -eq 2 ] || { echo "usage: $0 <upstream-commit> <report.md>" >&2; exit 2; }
COMMIT=$1
REPORT=$2
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
UPSTREAM_REPO=$(sed -n 's/^UPSTREAM_REPO=//p' "$HERE/build.sh")
SRC=$(mktemp -d)
trap 'rm -rf "$SRC"' EXIT

git clone --quiet "$UPSTREAM_REPO" "$SRC" || exit 1
git -C "$SRC" checkout --quiet "$COMMIT" || exit 1

failed=0
: > "$REPORT"
for patch in "$HERE"/patches/upstream-*.patch; do
  name=$(basename "$patch")
  if git -C "$SRC" apply --check "$patch" 2> /dev/null; then
    git -C "$SRC" apply "$patch"
    echo "- \`$name\`: applied" >> "$REPORT"
    continue
  fi
  failed=1
  out=$(git -C "$SRC" apply --reject --verbose "$patch" 2>&1)
  {
    echo "- \`$name\`: **failed**"
    echo
    echo '  ```'
    echo "$out" | sed 's/^/  /'
    find "$SRC" -name '*.rej' -newer "$patch" -print0 | while IFS= read -r -d '' rej; do
      echo "  --- ${rej#$SRC/}"
      sed 's/^/  /' "$rej"
      rm -f "$rej"
    done
    echo '  ```'
  } >> "$REPORT"
done
echo "- \`gobind-java-lazy-load.patch\`: applies to generated Java, checked by the build" >> "$REPORT"
exit $failed
