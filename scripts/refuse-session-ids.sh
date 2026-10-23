#!/bin/sh
# Refuse a commit message that carries an agent session link, ID or trailer.
# Usage: refuse-session-ids.sh <commit-msg-file>
# Exit status: 0 clean, 1 hit.
set -u

if [ "$#" -ne 1 ]; then
	echo "usage: refuse-session-ids.sh <commit-msg-file>" >&2
	exit 2
fi

if grep -q -E -e 'claude\.ai/code/session' \
	-e 'Claude-Session:' \
	-e 'Co-Authored-By:.*Claude' \
	-e 'session_[A-Za-z0-9]{20,}' -- "$1"; then
	echo "refuse-session-ids: the commit message holds an agent session detail; remove it" >&2
	exit 1
fi
exit 0
