#!/usr/bin/env bash
# shellcheck disable=SC2015 # ok and fail always succeed
# Checks the carry of a worker host's Sortie's merge records into p1's
# (spec store agent-platform/one-sortie), with ssh run locally against a
# throwaway taichi home and systemctl stubbed: worker-host.sh carry gives
# p1's Sortie every plan_satisfaction row of taichi's, keeps the rows p1
# holds already, and a second carry changes nothing; taichi's stage0.sh
# (retire_sortie) stops its Sortie only once p1 holds every one of them.
set -euo pipefail

KIT=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failed=0
ok() { echo "ok: $1"; }
fail() { echo "FAIL: $1"; failed=1; }
command -v python3 > /dev/null || { echo "FAIL: python3 is not on PATH"; exit 1; }

p1=$tmp/p1 taichi=$tmp/taichi
mkdir -p "$tmp/bin" "$p1/sortie" "$taichi/.local/share/ortus-nova/stage0/sortie" "$tmp/units"
# ssh HOST CMD...: CMD runs here, in taichi's home, as ssh runs it there.
cat > "$tmp/bin/ssh" <<EOF
#!/usr/bin/env bash
while [[ \$1 == -o ]]; do shift 2; done
shift
cd "$taichi" && HOME="$taichi" exec bash -c "\$*"
EOF
printf '#!/bin/sh\necho "systemctl $*" >> "%s/calls"\n' "$tmp" > "$tmp/bin/systemctl"
chmod +x "$tmp/bin/"*
export PATH=$tmp/bin:$PATH
calls() { [[ -f $tmp/calls ]] && tr '\n' ';' < "$tmp/calls"; true; }

# A Sortie database, with the table Sortie keeps its merge records in
# (sortie-plan-control design D3) and one column a newer Sortie might add.
sortie_db() { # sortie_db FILE [EXTRA_COLUMN]
  python3 - "$@" <<'EOF'
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
extra = f", {sys.argv[2]} TEXT" if len(sys.argv) > 2 else ""
db.execute(f"CREATE TABLE plan_satisfaction (issue_id TEXT NOT NULL, kind TEXT NOT NULL, ref TEXT, pr_url TEXT, recorded_at TEXT{extra}, PRIMARY KEY (issue_id, kind))")
db.commit()
EOF
}
record() { # record FILE ISSUE KIND REF
  python3 - "$@" <<'EOF'
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
db.execute("INSERT INTO plan_satisfaction (issue_id, kind, ref, pr_url, recorded_at) VALUES (?, ?, ?, ?, '2026-11-02T12:00:00Z')",
           (sys.argv[2], sys.argv[3], sys.argv[4], f"https://github.com/ortusnova/homelab/pull/{sys.argv[2][-1]}"))
db.commit()
EOF
}
held() { # held FILE: each row, "issue kind ref"
  python3 - "$1" <<'EOF'
import sqlite3, sys
for r in sqlite3.connect(sys.argv[1]).execute("SELECT issue_id, kind, ref FROM plan_satisfaction ORDER BY issue_id, kind"):
    print(*r)
EOF
}

tdb=$taichi/.local/share/ortus-nova/stage0/sortie/.sortie.db pdb=$p1/sortie/.sortie.db
sortie_db "$tdb"
sortie_db "$pdb" newer_column
record "$tdb" homelab-1 merged aaa1
record "$tdb" homelab-2 merged bbb2
record "$tdb" homelab-3 no_change plan:homelab/a
record "$pdb" sortie-9 merged fff9
record "$pdb" homelab-2 merged bbb2

# On p1: STAGE0_HOME is p1's, and taichi's Sortie is at its default home.
printf 'STAGE0_SORTIE_PORT=7680\n' > "$p1/stage0.env"
carry() { STAGE0_HOME=$p1 STAGE0_UNITS=$tmp/p1-units "$KIT/worker-host.sh" carry brian@taichi > "$tmp/out" 2>&1; }

# taichi, the worker profile's retire_sortie, as its stage0.sh runs it.
retire() {
  rm -f "$tmp/calls" "$tmp/said"; rc=0
  (
    STAGE0_HOME=$taichi/.local/share/ortus-nova/stage0
    say() { echo "$*" >> "$tmp/said"; }
    # shellcheck source=worker-config.sh
    . "$KIT/worker-config.sh"
    retire_sortie "$tmp/units"
  ) || rc=$?
}
echo unit > "$tmp/units/ortus-stage0-sortie.service"

# Scenario: taichi's Sortie still holds records p1's does not.
retire
(( rc != 0 )) && [[ -e $tmp/units/ortus-stage0-sortie.service && -z $(calls) ]] \
  && grep -q 'lacks 3 merge record(s)' "$tmp/said" && grep -q 'worker-host.sh carry' "$tmp/said" \
  && ok "taichi's Sortie keeps running while p1's lacks its merge records, and says how to carry them" \
  || fail "retire before the carry: exit $rc, calls '$(calls)', $(cat "$tmp/said")"

carry && ok "worker-host.sh carry runs" || fail "carry: $(cat "$tmp/out")"
want="homelab-1 merged aaa1
homelab-2 merged bbb2
homelab-3 no_change plan:homelab/a
sortie-9 merged fff9"
[[ $(held "$pdb") == "$want" ]] && ok "p1's Sortie holds every record of taichi's, and keeps its own" || fail "p1 holds: $(held "$pdb")"
grep -q 'holds the 3 merge record(s)' "$tmp/out" && ok "and carry says how many" || fail "carry said: $(cat "$tmp/out")"
sum=$(sha256sum < "$pdb")
carry && [[ $(sha256sum < "$pdb") == "$sum" ]] && ok "a second carry changes nothing" || fail "a second carry: $(cat "$tmp/out")"

# Scenario: a homelab story after a story that merged under taichi. Its
# blocker homelab-1 satisfies it on p1 from p1's own record.
[[ $(held "$pdb" | grep -c '^homelab-1 merged ') == 1 ]] && ok "homelab-1's merge satisfies its dependents on p1" || fail "no record of homelab-1 on p1"

# A merge recorded on taichi after the carry: its Sortie keeps running.
record "$tdb" homelab-4 merged ddd4
retire
(( rc != 0 )) && grep -q 'lacks 1 merge record(s)' "$tmp/said" && [[ -e $tmp/units/ortus-stage0-sortie.service ]] \
  && ok "a merge after the carry keeps taichi's Sortie running" || fail "retire after a later merge: exit $rc, $(cat "$tmp/said")"
carry
retire
(( rc == 0 )) && [[ ! -e $tmp/units/ortus-stage0-sortie.service ]] \
  && [[ $(calls) == "systemctl --user disable -q --now ortus-stage0-sortie.service;systemctl --user daemon-reload;" ]] \
  && ok "once p1 holds every record, taichi's Sortie is stopped and its unit gone" || fail "retire after the carry: exit $rc, calls '$(calls)', $(cat "$tmp/said")"
[[ -s $tdb ]] && ok "taichi's database stays" || fail "taichi's database is gone"
retire
(( rc == 0 )) && [[ -z $(calls) ]] && ok "retiring it again changes nothing" || fail "retire again: exit $rc, calls '$(calls)'"

# A host whose Sortie never recorded a merge (no database) retires at once.
rm -f "$tdb"; echo unit > "$tmp/units/ortus-stage0-sortie.service"
retire
(( rc == 0 )) && [[ ! -e $tmp/units/ortus-stage0-sortie.service ]] && ok "a Sortie with no database retires without a carry" || fail "no database: exit $rc, $(cat "$tmp/said" 2> /dev/null)"

# p1 without plan control's table refuses the carry, changing nothing.
sortie_db "$tdb"; record "$tdb" homelab-5 merged eee5
rm -f "$pdb"; python3 -c 'import sqlite3, sys; sqlite3.connect(sys.argv[1]).execute("CREATE TABLE other (x)")' "$pdb"
carry && fail "a carry into a Sortie with no plan_satisfaction table succeeded" \
  || { grep -q 'no plan_satisfaction table' "$tmp/out" && ok "a carry into a Sortie without plan control is refused"; } || fail "carry said: $(cat "$tmp/out")"
exit $failed
