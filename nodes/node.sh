#!/usr/bin/env bash
# Lifecycle for one node's stacks. Shipped in every node bundle next to that
# node's stacks and node.conf (deployed to ~/localstack/stacks), so on a node:
#
#   ~/localstack/stacks/node.sh start   [stack...]   networks, then up -d in start order
#   ~/localstack/stacks/node.sh stop    [stack...]   stop, in reverse start order
#   ~/localstack/stacks/node.sh restart [stack...]   stop, then start
#   ~/localstack/stacks/node.sh recreate [stack...]  networks, then up -d --force-recreate
#   ~/localstack/stacks/node.sh down    <stack...>   remove a stack's containers
#   ~/localstack/stacks/node.sh status               every stack's containers
#   ~/localstack/stacks/node.sh order                stacks in start order
#   ~/localstack/stacks/node.sh phases [stack...]    start phases, one per line
#   ~/localstack/stacks/node.sh list                 stacks, alphabetically
#   ~/localstack/stacks/node.sh compose <stack> <args…>  docker compose for one
#                                                          stack (logs, ps, exec, pull…)
#
# No stack names means every stack. Stacks listed in ORDER (node.conf) start
# one after another; all other stacks then start in parallel (stop runs the
# other way round). Everything `home` does to a stack goes through these
# commands, so the node behaves the same whether driven from home or here.
# Within a stack, compose's depends_on still decides the order of services.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── node.conf ────────────────────────────────────────────────────────────────
# ORDER=(…)                          stacks started first, one after another
#                                    in this order; the rest then in parallel
# network <name> <driver> [args…]    a network stacks join as external; args
#                                    go to `docker network create`
ORDER=()
NETWORKS=()
declare -A NET_DRIVER=() NET_ARGS=()
network() {
  local name="$1" driver="$2"; shift 2
  NETWORKS+=("$name"); NET_DRIVER[$name]="$driver"; NET_ARGS[$name]="$*"
}
# shellcheck source=/dev/null
[[ -f "$DIR/node.conf" ]] && source "$DIR/node.conf"

log() { echo "[node] $*"; }
die() { echo "node.sh: $*" >&2; exit 1; }

all_stacks() {
  local d
  for d in "$DIR"/*/; do [[ -f "$d/compose.yaml" ]] && basename "$d"; done
}

# Every stack in start order: ORDER first, then the rest alphabetically.
ordered() {
  local s all
  all=$(all_stacks)
  for s in "${ORDER[@]}"; do grep -qx "$s" <<< "$all" && echo "$s"; done
  for s in $all; do printf '%s\n' "${ORDER[@]}" | grep -qx "$s" || echo "$s"; done
}

# The given stacks (or all), validated, in start order.
selected() {
  local s order
  order=$(ordered)
  (( $# )) || { echo "$order"; return; }
  for s in "$@"; do grep -qx "$s" <<< "$order" || die "no stack '$s' in $DIR"; done
  for s in $order; do printf '%s\n' "$@" | grep -qx "$s" && echo "$s"; done
  return 0
}

ensure_networks() {
  local n
  for n in "${NETWORKS[@]}"; do
    docker network inspect "$n" >/dev/null 2>&1 && continue
    log "creating network $n"
    # shellcheck disable=SC2086 # NET_ARGS holds separate docker flags
    docker network create --driver "${NET_DRIVER[$n]}" ${NET_ARGS[$n]} "$n" >/dev/null
  done
}

compose() { local s="$1"; shift; docker compose -f "$DIR/$s/compose.yaml" "$@"; }

in_order() { printf '%s\n' "${ORDER[@]}" | grep -qx "$1"; }

# Start phases for the given stacks: each ORDER stack alone, in order, then
# every other stack together on one line.
phases() {
  local s rest=()
  for s in "$@"; do
    if in_order "$s"; then echo "$s"; else rest+=("$s"); fi
  done
  (( ${#rest[@]} )) && echo "${rest[*]}"
  return 0
}

# Runs `compose <stack> <args>` for every stack of one phase at once, each
# stack's output prefixed with its name. Fails if any stack failed.
parallel() {
  local verb="$1" s p rc=0; shift
  local -a stacks=() pids=()
  read -r -a stacks <<< "$PHASE"
  if (( ${#stacks[@]} == 1 )); then
    log "$verb ${stacks[0]}"; compose "${stacks[0]}" "$@"; return
  fi
  log "$verb in parallel: ${stacks[*]}"
  for s in "${stacks[@]}"; do
    ( compose "$s" "$@" 2>&1 | sed -u "s/^/[$s] /" ) &
    pids+=("$!")
  done
  for p in "${pids[@]}"; do wait "$p" || rc=1; done
  return "$rc"
}

# start/recreate: ORDER stacks one by one, then the rest in parallel.
# $1 is the verb, $2 the compose flags after `up -d`, the rest stack names.
run_up() {
  local verb="$1" flags="$2" list PHASE; shift 2
  local -a stacks
  list=$(selected "$@")   # exits on an unknown stack name
  mapfile -t stacks <<< "$list"
  ensure_networks
  while IFS= read -r PHASE; do
    # shellcheck disable=SC2086 # flags are separate words
    parallel "$verb" up -d $flags
  done < <(phases "${stacks[@]}")
}

cmd_start()    { run_up start "--remove-orphans" "$@"; }
cmd_recreate() { run_up recreate "--force-recreate --remove-orphans" "$@"; }

# stop: the parallel phase first, then ORDER stacks in reverse.
cmd_stop() {
  local list PHASE
  local -a stacks
  list=$(selected "$@")
  mapfile -t stacks <<< "$list"
  while IFS= read -r PHASE; do
    parallel stop stop
  done < <(phases "${stacks[@]}" | tac)
}

cmd_down() {
  local s
  (( $# )) || die "down needs stack names"
  # By project name, so it also works for a stack already removed from the repo.
  for s in "$@"; do log "down $s"; (cd / && docker compose -p "$s" down); done
}

cmd_status() {
  local s
  for s in $(ordered); do
    echo "$s"
    compose "$s" ps -a --format '  {{.Name}}\t{{.State}}\t{{.Health}}\t{{.Status}}'
  done
}

case "${1:-}" in
  start)   shift; cmd_start "$@" ;;
  stop)    shift; cmd_stop "$@" ;;
  restart) shift; cmd_stop "$@"; cmd_start "$@" ;;
  recreate) shift; cmd_recreate "$@" ;;
  down)    shift; cmd_down "$@" ;;
  status)  cmd_status ;;
  order)   ordered ;;
  phases)  shift; list=$(selected "$@"); mapfile -t stacks <<< "$list"; phases "${stacks[@]}" ;;
  list)    all_stacks ;;
  compose)
    shift; (( $# )) || die "compose needs a stack name"
    selected "$1" >/dev/null   # exits on an unknown stack name
    s="$1"; shift
    exec docker compose -f "$DIR/$s/compose.yaml" "$@" ;;
  networks) ensure_networks ;;
  *) sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
