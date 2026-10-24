#!/bin/sh
# Send a burst of wrong passwords to a running gobb.
#
# Usage: login-burst.sh <base-url> <tries> <at-once> <name>...
#   for example: login-burst.sh http://127.0.0.1:8180 40 20 bob carol
#
# Sends <tries> sign-ins with a wrong password, <at-once> at a time, and waits
# for each group to end. The names take turns. Each try comes from its own
# address in X-Forwarded-For (2001:db8:N::1, one /64 each), so the limit per
# address refuses none of them. The server must trust the peer as a proxy
# (GOBB_TRUSTED_PROXIES); .github/compose.ci.yaml does that. The limit per name
# allows 20 failures in an hour, so send at most 20 tries for each name.
#
# A wrong password answers 200 with the sign-in page. Prints one line for each
# group and a total. Exits 1 when any try gets another status (429, 503, a
# dropped connection), 2 on a usage error.
set -u

if [ "$#" -lt 4 ]; then
	echo "usage: login-burst.sh <base-url> <tries> <at-once> <name>..." >&2
	exit 2
fi
base=${1%/}
tries=$2
batch=$3
shift 3
case $tries$batch in
*[!0-9]*)
	echo "login-burst: <tries> and <at-once> must be whole numbers" >&2
	exit 2
	;;
esac
if [ "$tries" -lt 1 ] || [ "$batch" -lt 1 ]; then
	echo "login-burst: <tries> and <at-once> must be at least 1" >&2
	exit 2
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/login-burst.XXXXXX") || exit 2
trap 'rm -rf "$tmp"' EXIT
trap 'rm -rf "$tmp"; exit 1' HUP INT TERM

# One guest cookie and its csrf token serve every try.
got=$(curl --max-time 20 -sS -o "$tmp/form" -w '%{http_code}' -c "$tmp/jar" "$base/login") || {
	echo "FAIL GET /login: curl failed" >&2
	exit 1
}
[ "$got" = 200 ] || {
	echo "FAIL GET /login: want status 200, got $got" >&2
	exit 1
}
csrf=$(sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' "$tmp/form" | head -n 1)
[ -n "$csrf" ] || {
	echo "FAIL GET /login: no csrf field in the form" >&2
	exit 1
}

# tally reads one status per line and prints each status with its count.
tally() {
	sort | uniq -c | awk '{ printf "%s%s x%s", sep, $2, $1; sep = ", " }'
}

# try <n> <name>: one wrong password; writes the status to $tmp/status.<n>.
# curl writes 000 when the connection fails.
try() {
	code=$(curl --max-time 60 -sS -o /dev/null -w '%{http_code}' -b "$tmp/jar" \
		-H "X-Forwarded-For: 2001:db8:$1::1" \
		--data-urlencode "csrf=$csrf" --data-urlencode "name=$2" \
		--data-urlencode "password=wrong burst password $1" --data-urlencode "next=/" \
		"$base/login" 2>/dev/null)
	echo "${code:-000}" > "$tmp/status.$1"
}

n=0
while [ "$n" -lt "$tries" ]; do
	first=$((n + 1))
	i=0
	while [ "$i" -lt "$batch" ] && [ "$n" -lt "$tries" ]; do
		n=$((n + 1))
		# The names take turns: try n uses name ((n - 1) mod count) + 1.
		k=$(((n - 1) % $# + 1))
		eval "name=\${$k}"
		try "$n" "$name" &
		i=$((i + 1))
	done
	wait
	group=$(i=$first; while [ "$i" -le "$n" ]; do cat "$tmp/status.$i"; i=$((i + 1)); done)
	echo "tries $first-$n: $(printf '%s\n' "$group" | tally)"
done

all=$(cat "$tmp"/status.*)
bad=$(printf '%s\n' "$all" | grep -c -v -x 200)
echo "total: $tries tries: $(printf '%s\n' "$all" | tally)"
if [ "$bad" -ne 0 ]; then
	echo "FAIL $bad of $tries tries did not answer 200 (a wrong password)" >&2
	exit 1
fi
echo "ok: every try answered 200"
