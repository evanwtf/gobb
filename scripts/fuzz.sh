#!/bin/sh
# Run each fuzz target for a fixed time, and fail when a target does not exist.
#
# Usage: sh scripts/fuzz.sh FUZZTIME "PACKAGE NAME" ["PACKAGE NAME" ...]
#   sh scripts/fuzz.sh 60s "./markup/ FuzzParse" "./internal/password/ FuzzVerifyLegacy"
#
# go test -fuzz '^Name$' exits 0 with only a warning when no fuzz test has that
# name, so a renamed target would turn the CI job into a green no-op. Before each
# run, the script lists the target and requires the list to be exactly its name.
# One run reports every target: a failure does not skip the rest. The exit status
# is 1 when any target is missing or fails.
set -u

if [ "$#" -lt 2 ]; then
	echo 'usage: fuzz.sh FUZZTIME "PACKAGE NAME" ...' >&2
	exit 2
fi
fuzztime=$1
shift

status=0
for target in "$@"; do
	pkg=${target% *}
	name=${target#* }
	echo "== $name in $pkg"
	# Only stdout holds the list. Stderr goes to the log as it is: a cold module
	# cache makes go print "go: downloading ..." lines there.
	if ! listed=$(go test -list "^${name}\$" "$pkg"); then
		echo "$listed"
		echo "FAIL: go test -list failed for $name in $pkg"
		status=1
		continue
	fi
	# The list holds one name per line, then a line that starts with ok.
	names=$(printf '%s\n' "$listed" | grep -v -E '^(ok|FAIL|\?)[[:space:]]' || true)
	if [ "$names" != "$name" ]; then
		echo "FAIL: $pkg has no fuzz target named exactly $name (go test -list gave: ${names:-nothing})"
		status=1
		continue
	fi
	# A test or a benchmark can have the listed name, and go test then warns that
	# no fuzz test ran. Keep the output, show it, and fail on that warning.
	rc=0
	out=$(go test -run '^$' -fuzz "^${name}\$" -fuzztime "$fuzztime" -parallel 4 "$pkg" 2>&1) || rc=$?
	printf '%s\n' "$out"
	if [ "$rc" -ne 0 ]; then
		status=1
	elif printf '%s\n' "$out" | grep -q 'no fuzz tests to fuzz'; then
		echo "FAIL: $name in $pkg is not a fuzz target: go test found no fuzz test to run"
		status=1
	fi
done
exit "$status"
