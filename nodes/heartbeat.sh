#!/bin/sh
# Node heartbeat: one round, run every 5 minutes by the `heartbeat` service of
# each node's localstack stack (curlimages/curl, so busybox sh). The bundle
# copies this file into every node's localstack/ (scripts/bundle.Dockerfile),
# so editing it redeploys that stack, and the next round runs the new version
# without a restart. Each round:
#   - pings Healthchecks.io (HC_PING_URL); when pings stop, Healthchecks.io
#     alerts that the node (or the whole home) is down;
#   - checks how full the host's root filesystem (mounted at /host) is and
#     pushes to ntfy when it changes level: ok → warning (DISK_WARN%) →
#     critical (DISK_CRIT%), and back. Without NTFY_TOKEN this is log-only.
# Env: HC_PING_URL, NTFY_TOKEN (secrets/<node>-heartbeat.env); NODE, NTFY_URL
# (topic URL), DISK_WARN (85), DISK_CRIT (95).
set -u

warn=${DISK_WARN:-85}
crit=${DISK_CRIT:-95}
# The level last reported. Kept in the container, so a disk that is already
# full is reported once after every container (re)creation.
state=/tmp/disk-level

curl -fsS -m 10 --retry 3 -o /dev/null "$HC_PING_URL" || echo "healthchecks ping failed"

# df -P: 1024-blocks Used Available Capacity Mounted-on; split into $1 $2.
# shellcheck disable=SC2046
set -- $(df -P /host | awk 'NR == 2 { sub("%", "", $5); print $5, int($4 / 1048576) }')
pct=${1:-0} free_gb=${2:-0}
if [ "$pct" -ge "$crit" ]; then
	level=critical
elif [ "$pct" -ge "$warn" ]; then
	level=warning
else
	level=ok
fi
last=$(cat "$state" 2>/dev/null || echo ok)
[ "$level" = "$last" ] && exit 0

echo "disk ${pct}% (${free_gb} GB free): $last -> $level"
if [ -z "${NTFY_TOKEN:-}" ]; then
	echo "$level" >"$state"
	exit 0
fi
case $level in
critical) set -- "$NODE: disk ${pct}% full" 5 rotating_light "Only ${free_gb} GB free on /. Services and backups fail when it fills up." ;;
warning) set -- "$NODE: disk ${pct}% full" 4 warning "${free_gb} GB free on / (warning at ${warn}%, critical at ${crit}%)." ;;
ok) set -- "$NODE: disk back to ${pct}%" 3 white_check_mark "${free_gb} GB free on /." ;;
esac
# Only a delivered alert counts; otherwise the next round retries.
if curl -fsS -m 10 --retry 3 -o /dev/null -H "Authorization: Bearer $NTFY_TOKEN" \
	-H "Title: $1" -H "Priority: $2" -H "Tags: $3" -d "$4" "$NTFY_URL"; then
	echo "$level" >"$state"
else
	echo "ntfy push failed"
fi
