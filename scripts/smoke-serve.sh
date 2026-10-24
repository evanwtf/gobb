#!/bin/sh
# Smoke-test a running gobb that holds the example bundle.
#
# Usage: smoke-serve.sh <base-url>     for example: smoke-serve.sh http://127.0.0.1:8080
#
# Prints each URL with the status it got. Exits 1 at the first failure, 2 on a
# usage error. The server must run with GOBB_COOKIE_SECURE=false, because curl
# talks plain http. The sign-in uses the made-up user alice from
# docs/example-bundle/.
set -u

if [ "$#" -ne 1 ] || [ -z "$1" ]; then
	echo "usage: smoke-serve.sh <base-url>" >&2
	exit 2
fi
base=${1%/}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/smoke-serve.XXXXXX") || exit 2
trap 'rm -rf "$tmp"' EXIT
trap 'rm -rf "$tmp"; exit 1' HUP INT TERM

fail() {
	echo "FAIL $*" >&2
	exit 1
}

# location <want>
# Checks that the last response had exactly this Location header (whole line,
# so /t/1 does not match /t/10 or /t/1?page=2).
location() {
	tr -d '\r' < "$tmp/head" | grep -i -x -e "location: $1" > /dev/null ||
		fail "$path: want Location $1"
}

# get <path> <want-status> <want-csp: csp|-> [jar]
# Fetches without following redirects, then checks the status and, for csp,
# the Content-Security-Policy header. The body stays in $tmp/body.
get() {
	path=$1 want=$2 csp=$3
	set -- --max-time 20 -sS -o "$tmp/body" -D "$tmp/head" -w '%{http_code}'
	[ -f "$tmp/jar" ] && set -- "$@" -b "$tmp/jar" -c "$tmp/jar"
	got=$(curl "$@" "$base$path") || fail "$path: curl failed"
	echo "$got $path"
	[ "$got" = "$want" ] || fail "$path: want status $want, got $got"
	if [ "$csp" = csp ]; then
		grep -qi '^content-security-policy:' "$tmp/head" ||
			fail "$path: no Content-Security-Policy header"
	fi
}

get / 200 csp
get /f/2 200 csp
get /t/1 200 csp
get /login 200 csp
# The moved-topic marker sits in a forum that guests may not read. A guest gets
# the 404 of a missing topic, so the 301 is checked after the sign-in below.
get /t/3 404 -
# A gone user answers 410.
get /u/8 410 -
# A guest has no account.search, so search answers 403.
get '/search?q=example' 403 -
# Legacy routes: the old URLs of the example bundle.
get '/old/thread.cgi?view=full&id=1' 301 -
location /t/1
get /old/about.cgi 301 -
location /pages/about

# Sign in as alice with a cookie jar.
: > "$tmp/jar"
get /login 200 csp
csrf=$(sed -n 's/.*name="csrf" value="\([^"]*\)".*/\1/p' "$tmp/body" | head -n 1)
[ -n "$csrf" ] || fail "/login: no csrf field in the form"
got=$(curl --max-time 20 -sS -o "$tmp/body" -w '%{http_code}' -b "$tmp/jar" -c "$tmp/jar" \
	--data-urlencode "csrf=$csrf" --data-urlencode "name=alice" \
	--data-urlencode "password=example-password" --data-urlencode "next=/" \
	"$base/login") || fail "POST /login: curl failed"
echo "$got POST /login"
[ "$got" = 303 ] || fail "POST /login: want status 303, got $got"
get / 200 csp
grep -q 'Signed in as' "$tmp/body" || fail "/: no 'Signed in as' after sign-in"
# Search runs under the cap too, for the member.
get '/search?q=example' 200 csp
# The page echoes the query, so look for a hit: a link to a post.
grep -q -e 'href="/p/' "$tmp/body" || fail "/search?q=example: no hit (no link to a post)"
# A member who may read the marker's forum gets the 301 to its target.
get /t/3 301 -
location /t/1
echo "ok: every check passed"
