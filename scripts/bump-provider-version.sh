#!/usr/bin/env bash
set -euo pipefail

[ $# -eq 2 ] || { echo "usage: $0 <provider build.gradle.kts> <engine version>" >&2; exit 2; }
FILE=$1
ENGINE=$2

[[ "$ENGINE" =~ ^[1-9][0-9]*$ ]] || { echo "engine version must be a positive integer, got '$ENGINE'" >&2; exit 1; }
[ -f "$FILE" ] || { echo "$FILE not found" >&2; exit 1; }

value_of() {
  local name=$1 count
  count=$(grep -cE "^    $name = [0-9]+$" "$FILE" || true)
  [ "$count" = 1 ] || { echo "$FILE: expected exactly one '$name = <n>' line, found $count; the file shape changed" >&2; exit 1; }
  grep -E "^    $name = [0-9]+$" "$FILE" | sed -E 's/.* = ([0-9]+)$/\1/'
}

major=$(value_of versionMajor)
minor=$(value_of versionMinor)
patch=$(value_of versionPatch)
value_of versionBuild > /dev/null

next=$((patch + 1))
name="$major.$minor.$next"

anchor_count=$(grep -cE '^    changelog =$' "$FILE" || true)
[ "$anchor_count" = 1 ] || { echo "$FILE: expected exactly one 'changelog =' line, found $anchor_count" >&2; exit 1; }

WORK=$(mktemp)
trap 'rm -f "$WORK"' EXIT

awk -v next_patch="$next" -v name="$name" -v engine="$ENGINE" '
  /^    versionPatch = [0-9]+$/ { print "    versionPatch = " next_patch; next }
  /^    versionBuild = [0-9]+$/ { print "    versionBuild = 0"; next }
  /^    changelog =$/ { print; pending = 1; next }
  pending == 1 && /^        """$/ {
    print
    print "        # v" name
    print ""
    print "        - Torrent engine updated to v" engine "."
    print ""
    pending = 0
    next
  }
  { print }
' "$FILE" > "$WORK"

grep -qxF "    versionPatch = $next" "$WORK" || { echo "$FILE: failed to bump versionPatch" >&2; exit 1; }
grep -qxF "        # v$name" "$WORK" || { echo "$FILE: failed to add the changelog entry" >&2; exit 1; }

cat "$WORK" > "$FILE"
echo "$name"
