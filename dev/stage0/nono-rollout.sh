# shellcheck shell=bash
# How a change to nono's hosts reaches nono on p1 (spec store
# agent-platform/supervisor-autonomy, "A rollout narrows egress only after
# the offers carry the new hosts", design D8). A job's offer is made when
# it is dispatched, from the workflow Sortie ran then, and the job keeps it
# to its end, across a restart of Sortie. A host that leaves nono's profile
# for the offer's policy (as the harness hosts did, ONS-139) is reached
# directly only by a job whose offer has it: a job dispatched before Sortie
# ran the workflow that offers it reaches it through nono or not at all
# (ONS-122, dispatched 4 s before the restart while nono was already cut).
#
# So nono never runs the kit's profile (control-plane/nono/egress-profile.json)
# itself, but the profile in force, $STAGE0_HOME/nono/egress-profile.json,
# which run-nono.sh reads:
#
#   - A kit profile that removes no host is in force at once.
#   - One that removes hosts waits in egress-profile.next.json; until then
#     the profile in force is the kit's with every host nono allowed
#     before, so nono keeps the full list (nono_profile_stage).
#   - stage0.sh restarts Sortie on the rendered workflow first and records
#     its revision (the digest of the rendered workflow) and when the Sortie
#     that runs it started, from the state API's started_at, which must be
#     no earlier than the rendered workflow (nono_rollout_mark, in
#     $STAGE0_HOME/nono/narrowing).
#   - The kit's profile goes in force only once the state API shows a Sortie
#     started on that revision, polled since (it re-adopts the runs it
#     restored before its first poll), and no running job started before
#     it, so none holds an offer from an earlier revision (nono_narrow).
#     A re-adopted job keeps the started_at of its dispatch. stage0.sh
#     checks after its restart of Sortie, and deploy.sh at each tick of the
#     deploy timer, which restarts both nonos onto it.
#
# Needs STAGE0_HOME and STAGE0_SORTIE_PORT, python3, systemctl and curl;
# restart_changed (unit-restart.sh) for nono_restart and nono_narrow.

nono_profile() { echo "$STAGE0_HOME/nono/egress-profile.json"; }
nono_next() { echo "$STAGE0_HOME/nono/egress-profile.next.json"; }
nono_record() { echo "$STAGE0_HOME/nono/narrowing"; }
nono_units=(ortus-stage0-nono.service ortus-stage0-nono-live-proof.service)

# nono_py MODE ARG...: the rollout's JSON work, in python3.
#   removed OLD NEW  each host profile OLD allows and NEW does not
#   kept OLD NEW     NEW allowing every host OLD allows too; nothing when
#                    OLD already allows NEW's hosts with NEW's credentials
#   started MTIME    from the state API on stdin, when the Sortie that
#                    answers started, if at or after MTIME (epoch seconds)
#   ready SINCE      from the state API on stdin, "ready" when the Sortie
#                    that answers started at or after SINCE, has polled
#                    since, and every running job started at or after
#                    SINCE too; otherwise why not
# The program is an argument: stdin carries the state.
nono_py() {
  python3 -c "$(cat <<'EOF'
import json, re, sys
from datetime import datetime, timezone

def when(s):
    m = re.match(r"^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(\.\d+)?(Z|[+-]\d\d:\d\d)$", s if isinstance(s, str) else "")
    if not m:
        return None
    zone = "+00:00" if m.group(3) == "Z" else m.group(3)
    return datetime.fromisoformat(m.group(1) + (m.group(2) or ".0")[:7] + zone).astimezone(timezone.utc)

def hosts(p):
    return set(p["network"].get("allow_domain") or [])

mode, args = sys.argv[1], sys.argv[2:]
if mode == "removed":
    old, new = (json.load(open(f)) for f in args)
    print("\n".join(sorted(hosts(old) - hosts(new))))
elif mode == "kept":
    old, new = (json.load(open(f)) for f in args)
    new["network"]["allow_domain"] = sorted(hosts(new) | hosts(old))
    if new["network"] == dict(old["network"], allow_domain=sorted(hosts(old))):
        sys.exit(0)
    meta = new.setdefault("meta", {})
    meta["description"] = (meta.get("description", "") + " While a rollout waits (dev/stage0/nono-rollout.sh),"
                           " it also allows every host nono allowed before.").strip()
    print(json.dumps(new, indent=2))
elif mode == "started":
    state = json.load(sys.stdin)
    started = when(state.get("started_at"))
    if started is not None and started.timestamp() >= int(args[0]):
        print(state["started_at"])
elif mode == "ready":
    since, state = when(args[0]), json.load(sys.stdin)
    started = when(state.get("started_at"))
    if since is None or started is None or started < since:
        print("Sortie's state API does not show the workflow that offers the hosts (Sortie started %s, on it at %s)"
              % (state.get("started_at"), args[0]))
        sys.exit(0)
    # Sortie serves its state before it re-adopts the runs it restored, and
    # polls only after: a poll since its start shows every run it holds.
    polled = when((((state.get("health") or {}).get("poll") or {}).get("checked_at")))
    if polled is None or polled < started:
        print("Sortie has not polled since it started %s, so it may not have re-adopted every run yet"
              % state.get("started_at"))
        sys.exit(0)
    earlier = []
    for r in state.get("running") or []:
        r = r if isinstance(r, dict) else {}
        s = when(r.get("started_at"))
        if s is None or s < since:
            earlier.append("%s, started %s" % (r.get("issue_identifier", "a job"), r.get("started_at")))
    if earlier:
        print("%s hold%s an offer from before Sortie ran that workflow (%s)"
              % ("; ".join(earlier), "s" if len(earlier) == 1 else "", args[0]))
    else:
        print("ready")
EOF
)" "$@"
}

# nono_profile_stage KIT_PROFILE REPO [FROM]: put in force what nono may
# run now. FROM is the kit's commit that ran before this one: when no
# profile is in force yet and nono runs, it runs FROM's profile, which
# becomes the one in force.
nono_profile_stage() {
  local kit=$1 repo=$2 from=${3:-} force next removed
  force=$(nono_profile) next=$(nono_next)
  mkdir -p "${force%/*}"
  if [[ ! -s $force ]]; then
    if [[ -n $from ]] && systemctl --user is-active -q ortus-stage0-nono.service \
      && git -C "$repo" show "$from:control-plane/nono/egress-profile.json" > "$force.tmp" 2> /dev/null && [[ -s $force.tmp ]]; then
      echo "nono runs the profile of ${from:0:12}: it stays in force until the rollout lets it go" >&2
      mv "$force.tmp" "$force"
    else
      rm -f "$force.tmp"
      cp "$kit" "$force"
    fi
  fi
  removed=$(nono_py removed "$force" "$kit") || return 1
  if [[ -z $removed ]]; then
    cmp -s "$kit" "$force" || cp "$kit" "$force"
    rm -f "$next" "$(nono_record)"
    return 0
  fi
  cmp -s "$kit" "$next" || cp "$kit" "$next"
  nono_py kept "$force" "$kit" > "$force.tmp" || { rm -f "$force.tmp"; return 1; }
  if [[ -s $force.tmp ]]; then mv "$force.tmp" "$force"; else rm -f "$force.tmp"; fi
  echo "nono keeps $(paste -sd' ' <<< "$removed"), which the kit's profile no longer allows, until Sortie runs the workflow that offers the hosts nono leaves and no running job holds an earlier offer" >&2
}

# nono_state: Sortie's state API.
nono_state() { curl -sf --max-time 10 "http://127.0.0.1:$STAGE0_SORTIE_PORT/api/v1/state"; }

# nono_revision WORKFLOW: the revision of the rendered workflow.
nono_revision() { sha256sum < "$1" | cut -c1-16; }

# nono_record_field KEY: KEY's value in the record of the waiting rollout.
nono_record_field() { sed -n "s/^$1=//p" "$(nono_record)" 2> /dev/null | head -n 1; }

# nono_rollout_mark WORKFLOW: after Sortie's restart on WORKFLOW, record
# its revision and when the Sortie that runs it started: the state API's
# started_at, once it is no earlier than WORKFLOW, so the Sortie that
# answers read it at its start. A record of the same revision with a start
# is kept: the first Sortie on that revision started then.
nono_rollout_mark() {
  [[ -s $(nono_next) ]] || return 0
  local rev since=''
  rev=$(nono_revision "$1")
  if [[ $(nono_record_field revision) == "$rev" && -n $(nono_record_field since) ]]; then return 0; fi
  since=$(nono_state | nono_py started "$(stat -c %Y "$1")") || since=''
  printf 'revision=%s\nsince=%s\n' "$rev" "$since" > "$(nono_record)"
}

# nono_narrow WORKFLOW [restart]: put the waiting kit profile in force once
# the rollout lets it (above); with restart, also restart both nonos onto
# it, from the inputs their last start recorded (nono_restart). While
# nothing waits, it does nothing and asks Sortie nothing.
nono_narrow() {
  local next since verdict unit inputs
  next=$(nono_next)
  [[ -s $next ]] || return 0
  if [[ $(nono_record_field revision) != "$(nono_revision "$1")" ]]; then
    echo "nono keeps the full list: Sortie has not restarted on the rendered workflow yet" >&2
    return 0
  fi
  [[ -n $(nono_record_field since) ]] || nono_rollout_mark "$1"
  since=$(nono_record_field since)
  if [[ -z $since ]]; then
    echo "nono keeps the full list: Sortie's state API does not show a Sortie started on the rendered workflow" >&2
    return 0
  fi
  if ! verdict=$(nono_state | nono_py ready "$since"); then
    echo "nono keeps the full list: Sortie's state API did not answer" >&2
    return 0
  fi
  if [[ $verdict != ready ]]; then
    echo "nono keeps the full list: $verdict" >&2
    return 0
  fi
  cp "$next" "$(nono_profile)"
  rm -f "$next" "$(nono_record)"
  echo "nono serves the kit's profile now: Sortie runs the workflow that offers the hosts nono left (since $since), and no running job holds an earlier offer" >&2
  [[ ${2:-} == restart ]] || return 0
  for unit in "${nono_units[@]}"; do
    mapfile -t inputs 2> /dev/null < "$STAGE0_HOME/nono/$unit.inputs" || inputs=()
    restart_changed "$unit" "${inputs[@]}" || return 1
  done
}

# nono_restart UNIT PATH...: restart_changed UNIT PATH..., recording the
# PATHs for nono_narrow's restart.
nono_restart() {
  mkdir -p "$STAGE0_HOME/nono"
  printf '%s\n' "${@:2}" > "$STAGE0_HOME/nono/$1.inputs"
  restart_changed "$@"
}
