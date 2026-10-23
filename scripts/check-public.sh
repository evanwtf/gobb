#!/bin/sh
# Refuse private details in a public repo.
#
# Usage: check-public.sh [files...]
#   With no arguments, checks every file from `git ls-files`.
#   With arguments (pre-commit passes the staged names), checks only those.
#
# Private patterns: LAN addresses, home-directory paths, private DNS suffixes.
# Owner's denylist: one fixed string per line, from $DENYLIST_FILE if set,
# else .public-denylist if present. Blank lines and # comments are ignored.
#
# On a hit the script prints file:line and never the matched text.
# Exit status: 0 clean, 1 hit.
set -u

PATTERNS='192\.168\.[0-9]+\.[0-9]+|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]+\.[0-9]+|/Users/[A-Za-z0-9._-]+/|/home/[A-Za-z0-9._-]+/|\.(internal|lan|home\.arpa|localdomain)\b'

tmp=$(mktemp "${TMPDIR:-/tmp}/check-public.XXXXXX") || exit 2
trap 'rm -f "$tmp" "$tmp.list"' EXIT HUP INT TERM

deny=${DENYLIST_FILE:-.public-denylist}
if [ -f "$deny" ]; then
	grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$deny" >"$tmp"
fi

skip() {
	case $1 in
	scripts/check-public.sh | scripts/guards_test.go | .public-denylist) return 0 ;;
	esac
	return 1
}

# scan prints file:line for each hit in one file and succeeds if any.
scan() {
	hits=$(
		grep -I -n -H -E -e "$PATTERNS" -- "$1"
		if [ -s "$tmp" ]; then
			grep -I -n -H -i -F -f "$tmp" -- "$1"
		fi
	)
	if [ -n "$hits" ]; then
		printf '%s\n' "$hits" | cut -d: -f1,2 | sort -u -t: -k2,2n
		return 0
	fi
	return 1
}

status=0
check() {
	f=${1#./}
	skip "$f" && return
	[ -f "$f" ] || return
	if scan "$f"; then
		status=1
	fi
}

if [ "$#" -gt 0 ]; then
	for f in "$@"; do
		check "$f"
	done
else
	# Read the list into a file so `status` stays in this shell.
	git ls-files >"$tmp.list" || exit 2
	while IFS= read -r f; do
		check "$f"
	done <"$tmp.list"
	rm -f "$tmp.list"
fi

if [ "$status" -ne 0 ]; then
	echo "check-public: private detail found at the places above; reword it" >&2
fi
exit "$status"
