#!/bin/sh
# Refuse a commit message that carries an agent session link, ID or trailer.
# Usage: refuse-session-ids.sh <commit-msg-file>
# Exit status: 0 clean, 1 hit.
set -u

if [ "$#" -ne 1 ]; then
	echo "usage: refuse-session-ids.sh <commit-msg-file>" >&2
	exit 2
fi

# Clean the message the way git does before it stores it: drop the scissors line
# and everything after it (the verbose diff), then drop lines that start with #.
msg=$(mktemp "${TMPDIR:-/tmp}/refuse-session-ids.XXXXXX") || exit 2
trap 'rm -f "$msg"' EXIT HUP INT TERM
awk '/^# -+ >8 -+$/ { skip = 1 } skip || /^#/ { next } { print }' "$1" >"$msg"

# Match without regard to case: "Co-authored-by:" is how GitHub writes it.
if grep -q -i -E -e 'claude\.ai/code/session' \
	-e 'Claude-Session:' \
	-e 'Co-Authored-By:.*Claude' \
	-e 'session_[A-Za-z0-9]{20,}' -- "$msg"; then
	echo "refuse-session-ids: the commit message holds an agent session detail; remove it" >&2
	exit 1
fi
exit 0
