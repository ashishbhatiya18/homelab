#!/usr/bin/env bash
# Lifecycle for one node's stacks. Shipped in every node bundle as
# nodes/<node>/node.sh next to that node's stacks and node.conf, so on a node:
#
#   ~/localstack/nodes/<node>/node.sh start   [stack...]   networks, then up -d in start order
#   ~/localstack/nodes/<node>/node.sh stop    [stack...]   stop, in reverse start order
#   ~/localstack/nodes/<node>/node.sh restart [stack...]   stop, then start
#   ~/localstack/nodes/<node>/node.sh down    <stack...>   remove a stack's containers
#   ~/localstack/nodes/<node>/node.sh status               every stack's containers
#   ~/localstack/nodes/<node>/node.sh order                stacks in start order
#   ~/localstack/nodes/<node>/node.sh list                 stacks, alphabetically
#   ~/localstack/nodes/<node>/node.sh compose <stack> <args…>  docker compose for one
#                                                          stack (logs, ps, exec, pull…)
#
# No stack names means every stack. Everything `home` does to a stack goes
# through these commands, so the node behaves the same whether driven from
# home or here.
# Within a stack, compose's depends_on still decides the order of services.
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── node.conf ────────────────────────────────────────────────────────────────
# ORDER=(…)                          stacks started first, in this order; the
#                                    rest follow alphabetically
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

cmd_start() {
  local s list
  list=$(selected "$@")   # exits on an unknown stack name
  ensure_networks
  for s in $list; do log "start $s"; compose "$s" up -d --remove-orphans; done
}

cmd_stop() {
  local s list
  list=$(selected "$@")
  for s in $(tac <<< "$list"); do log "stop $s"; compose "$s" stop; done
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
  down)    shift; cmd_down "$@" ;;
  status)  cmd_status ;;
  order)   ordered ;;
  list)    all_stacks ;;
  compose)
    shift; (( $# )) || die "compose needs a stack name"
    selected "$1" >/dev/null   # exits on an unknown stack name
    s="$1"; shift
    exec docker compose -f "$DIR/$s/compose.yaml" "$@" ;;
  networks) ensure_networks ;;
  *) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
