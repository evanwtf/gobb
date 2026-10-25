#!/bin/sh
# Print one shell block of the README, so CI runs the commands that the README
# shows and not a copy of them.
#
#   sh scripts/readme-block.sh NAME [README]
#
# The block is the first fence that opens with ```sh after the line
# `<!-- block: NAME -->`. Up to three spaces of list indentation are removed.
# The exit status is 1 when the README has no such block, or the block is empty.
set -eu
[ "$#" -ge 1 ] && [ "$#" -le 2 ] || { echo "usage: readme-block.sh NAME [README]" >&2; exit 2; }
name=$1
file=${2:-README.md}
out=$(awk -v marker="<!-- block: $name -->" '
  { line = $0; sub(/^ +/, "", line) }
  !want && line == marker { want = 1; next }
  want && !infence && line == "```sh" { infence = 1; next }
  infence && line == "```" { exit }
  infence { sub(/^   /, ""); print }
' "$file")
if [ -z "$out" ]; then
  echo "readme-block: no block named $name in $file" >&2
  exit 1
fi
printf '%s\n' "$out"
