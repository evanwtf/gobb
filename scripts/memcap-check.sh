#!/bin/sh
# Check a running container against its memory cap, from its cgroup (v2).
#
# Usage: memcap-check.sh <container> <cap-bytes>
#   for example: memcap-check.sh "$(docker compose ps -q gobb)" 268435456
#
# Prints the restart count and the OOM flag of Docker, the docker stats line,
# the cgroup path, memory.peak and the oom_kill count of memory.events. Exits 1
# when Docker restarted the container or saw an OOM kill, when the cgroup is not
# readable, when oom_kill is not 0, or when memory.peak reached <cap-bytes>.
# Exits 2 on a usage error.
#
# A restart makes a new cgroup, so the restart count is the check that sees a
# kill before the last start. The cgroup path depends on the cgroup driver of
# Docker: systemd or cgroupfs. Both are tried, below CGROUP_ROOT (default
# /sys/fs/cgroup; the tests set it).
set -u
root=${CGROUP_ROOT:-/sys/fs/cgroup}

if [ "$#" -ne 2 ] || [ -z "$1" ]; then
	echo "usage: memcap-check.sh <container> <cap-bytes>" >&2
	exit 2
fi
cid=$1
cap=$2
case $cap in
"" | *[!0-9]*)
	echo "memcap-check: <cap-bytes> must be a whole number" >&2
	exit 2
	;;
esac

state=$(docker inspect -f '{{.RestartCount}} {{.State.OOMKilled}}' "$cid") || exit 1
echo "restarts and OOM kill: $state"
docker stats --no-stream --format 'docker stats now: {{.MemUsage}} ({{.MemPerc}})' "$cid"
[ "$state" = "0 false" ] || {
	echo "the container restarted or the kernel killed it"
	exit 1
}

id=$(docker inspect -f '{{.Id}}' "$cid") || exit 1
dir=
for d in "$root/system.slice/docker-$id.scope" "$root/docker/$id"; do
	if [ -r "$d/memory.peak" ] && [ -r "$d/memory.events" ]; then
		dir=$d
		break
	fi
done
[ -n "$dir" ] || {
	echo "cgroup memory.peak is not readable on this host (tried the systemd and cgroupfs paths)"
	exit 1
}
peak=$(cat "$dir/memory.peak")
oom=$(awk '$1 == "oom_kill" { print $2 }' "$dir/memory.events")
echo "cgroup path used: $dir"
echo "memory.peak: $peak bytes ($((peak / 1048576)) MiB)"
echo "memory.events oom_kill: $oom"
[ "$oom" = 0 ] || {
	echo "the kernel killed a process in the container"
	exit 1
}
[ "$peak" -lt "$cap" ] || {
	echo "memory.peak reached the cap of $cap bytes"
	exit 1
}
