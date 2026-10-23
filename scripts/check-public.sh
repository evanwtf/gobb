#!/bin/sh
# Refuse private details in a public repo.
#
# Usage: check-public.sh [--commit-msg] [files...]
#   With no arguments, checks every file from `git ls-files`.
#   With arguments (pre-commit passes the staged names), checks only those.
#   With --commit-msg, each file is a commit message. The script first drops the
#   scissors line and everything after it (the verbose diff), which never reaches
#   history. It keeps lines that start with #: git keeps them for -m and -F, so
#   the guard scans them too. Line numbers in the output stay those of the file.
#
# Private patterns: LAN addresses, home-directory paths, private DNS suffixes.
# Owner's denylist: one fixed string per line, from $DENYLIST_FILE if set,
# else .public-denylist if present. Blank lines and # comments are ignored.
#
# On a hit the script prints file:line and never the matched text.
# A DENYLIST_FILE that is set but unreadable is an error, not "no denylist".
# Exit status: 0 clean, 1 hit, 2 error.
set -u

PATTERNS='192\.168\.[0-9]+\.[0-9]+|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]+\.[0-9]+|/Users/[A-Za-z0-9._-]+/|/home/[A-Za-z0-9._-]+/|\.(internal|lan|home\.arpa|localdomain)\b'

msgmode=0
if [ "${1:-}" = "--commit-msg" ]; then
	msgmode=1
	shift
fi

tmp=$(mktemp "${TMPDIR:-/tmp}/check-public.XXXXXX") || exit 2
trap 'rm -f "$tmp" "$tmp.list" "$tmp.msg"' EXIT HUP INT TERM

if [ "${DENYLIST_FILE+set}" = set ]; then
	# Fail closed: a wrong path in CI must not turn the guard off.
	if [ ! -r "${DENYLIST_FILE}" ] || [ ! -f "${DENYLIST_FILE}" ]; then
		echo "check-public: DENYLIST_FILE is set but is not a readable file" >&2
		exit 2
	fi
	deny=$DENYLIST_FILE
else
	deny=.public-denylist
fi
if [ -f "$deny" ]; then
	grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$deny" >"$tmp"
fi

skip() {
	case $1 in
	scripts/check-public.sh | scripts/guards_test.go | .public-denylist) return 0 ;;
	esac
	return 1
}

# strip_msg writes a commit message to $tmp.msg without the scissors line and
# what follows it. It blanks those lines instead of deleting them, so line
# numbers stay the same.
strip_msg() {
	awk '/^# -+ >8 -+$/ { skip = 1 } skip { print ""; next } { print }' "$1" >"$tmp.msg"
}

# scan prints name:line for each hit in file $1 and succeeds if any.
# $2 is the name to print; it differs from $1 for a cleaned commit message.
scan() {
	hits=$(
		grep -I -n -E -e "$PATTERNS" -- "$1"
		if [ -s "$tmp" ]; then
			grep -I -n -i -F -f "$tmp" -- "$1"
		fi
	)
	if [ -n "$hits" ]; then
		printf '%s\n' "$hits" | cut -d: -f1 | sort -u -n | sed "s|^|$2:|"
		return 0
	fi
	return 1
}

status=0
check() {
	f=${1#./}
	skip "$f" && return
	[ -f "$f" ] || return
	if [ "$msgmode" -eq 1 ]; then
		strip_msg "$f"
		if scan "$tmp.msg" "$f"; then
			status=1
		fi
	elif scan "$f" "$f"; then
		status=1
	fi
}

if [ "$#" -gt 0 ]; then
	for f in "$@"; do
		check "$f"
	done
else
	# Read the list into a file so `status` stays in this shell.
	git -c core.quotePath=false ls-files >"$tmp.list" || exit 2
	while IFS= read -r f; do
		check "$f"
	done <"$tmp.list"
	rm -f "$tmp.list"
fi

if [ "$status" -ne 0 ]; then
	echo "check-public: private detail found at the places above; reword it" >&2
fi
exit "$status"
