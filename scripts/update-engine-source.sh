#!/usr/bin/env bash
set -euo pipefail

[ $# -eq 3 ] || { echo "usage: $0 <EngineSource.kt> <version> <sha256.txt>" >&2; exit 2; }
FILE=$1
VERSION=$2
SUMS=$3
ABIS=(arm64-v8a armeabi-v7a x86_64 x86)

[[ "$VERSION" =~ ^[1-9][0-9]*$ ]] || { echo "version must be a positive integer, got '$VERSION'" >&2; exit 1; }
[ -f "$FILE" ] || { echo "$FILE not found" >&2; exit 1; }
WORK=$(mktemp)
trap 'rm -f "$WORK" "$WORK.tmp"' EXIT
cp "$FILE" "$WORK"

hash_of() {
  local h
  h=$(awk -v f="$1" '$2 == f { print $1 }' "$SUMS")
  [[ "$h" =~ ^[0-9a-f]{64}$ ]] || { echo "no sha256 for $1 in $SUMS" >&2; exit 1; }
  echo "$h"
}

replace_line() {
  local pattern=$1 line=$2 count
  count=$(grep -cE "$pattern" "$WORK" || true)
  [ "$count" = 1 ] || { echo "$FILE: expected exactly one line matching '$pattern', found $count; the file shape changed" >&2; exit 1; }
  awk -v p="$pattern" -v l="$line" '$0 ~ p { print l; next } { print }' "$WORK" > "$WORK.tmp"
  mv "$WORK.tmp" "$WORK"
  grep -qxF "$line" "$WORK" || { echo "$FILE: failed to write '$line'" >&2; exit 1; }
}

HEX='[0-9a-f]{64}'
replace_line '^    const val VERSION = "[0-9]+"$' "    const val VERSION = \"$VERSION\""
dex=$(hash_of classes.dex)
replace_line "^    const val DEX_SHA256 = \"$HEX\"\$" "    const val DEX_SHA256 = \"$dex\""
for abi in "${ABIS[@]}"; do
  lib=$(hash_of "jni/$abi/libgojni.so")
  replace_line "^        \"$abi\" to \"$HEX\",\$" "        \"$abi\" to \"$lib\","
done
grep -qF 'releases/download/v$VERSION/torrserver.aar' "$WORK" || { echo "$FILE: AAR_URL does not use v\$VERSION" >&2; exit 1; }
cat "$WORK" > "$FILE"
