#!/usr/bin/env python3
"""Report the GitHub rules that would bound every minted token (spec store
job-scoped-github-tokens, design D7; make check-forge-rules).

For each repository some forge_ceiling of WORKFLOW.md grants write on, read
its rulesets through GitHub's API and say whether an active ruleset on its
default branch requires a pull request, blocks force pushes and blocks
deletion. Such a ruleset is a recommendation, not a requirement (operator,
2026-11-28: the organization stays on GitHub Free, which refuses rulesets on
a private repository): a repository without one is reported as
`recommended:`, and the check passes. It fails only when it cannot read a
repository, or when a ruleset lists the App as a bypass actor, which would
undo the ruleset for every minted token.

It only reads (GET). The credential is FORGE_RULES_TOKEN, a read-only token,
else the operator's `gh auth token`, used for GET requests only.
"""
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request

KIT = os.path.dirname(os.path.abspath(__file__))
API = os.environ.get("FORGE_RULES_API", "https://api.github.com").rstrip("/")
APP_ID = int(os.environ.get("FORGE_RULES_APP_ID", "5857256"))
NEEDED = {"pull_request": "require a pull request", "non_fast_forward": "block force pushes", "deletion": "block deletion"}


def ceiling_writes(workflow):
    """The repositories some forge_ceiling entry grants a write permission on."""
    lines = [l for l in open(workflow).read().split("---\n", 2)[1].splitlines() if l.strip() and not l.strip().startswith("#")]
    repos, inside, current = [], False, None
    for line in lines:
        if line == "    forge_ceiling:":
            inside = True
            continue
        if inside and not line.startswith("      "):
            inside = False
        if not inside:
            continue
        m = re.match(r"^        - repository: (\S+)$", line)
        if m:
            current = m.group(1)
            continue
        m = re.match(r"^          permissions: \{(.*)\}$", line)
        if m and current and re.search(r":\s*write\b", m.group(1)) and current not in repos:
            repos.append(current)
    return repos


def token():
    if os.environ.get("FORGE_RULES_TOKEN"):
        return os.environ["FORGE_RULES_TOKEN"]
    return subprocess.run(["gh", "auth", "token"], capture_output=True, text=True, check=True).stdout.strip()


def get(path, tok):
    req = urllib.request.Request(API + path, headers={"Authorization": "Bearer " + tok,
                                                       "Accept": "application/vnd.github+json"})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            return r.status, json.loads(r.read() or b"null")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read() or b"null")
        except ValueError:
            return e.code, None


def on_default_branch(ruleset, default):
    ref = (ruleset.get("conditions") or {}).get("ref_name") or {}
    include, exclude = ref.get("include", []), ref.get("exclude", [])
    names = {"~DEFAULT_BRANCH", "~ALL", "refs/heads/" + default}
    return any(i in names for i in include) and not any(e in names for e in exclude)


def check(repo, tok):
    """The problems of repo's default branch, which fail the check, and the
    rules it lacks, which are recommended."""
    status, info = get(f"/repos/{repo}", tok)
    if status != 200:
        return [f"cannot read the repository (HTTP {status})"], []
    default = info["default_branch"]
    status, rulesets = get(f"/repos/{repo}/rulesets?includes_parents=true", tok)
    if status == 403 and "Upgrade to GitHub Pro" in str((rulesets or {}).get("message", "")):
        return [], [f"no ruleset: GitHub refuses rulesets on this {'private ' if info.get('private') else ''}repository "
                    f"on its plan (HTTP 403: {rulesets['message']})"]
    if status != 200:
        return [f"cannot read its rulesets (HTTP {status})"], []
    have, bypassed = set(), False
    for summary in rulesets:
        status, rs = get(f"/repos/{repo}/rulesets/{summary['id']}", tok)
        if status != 200 or rs.get("target", "branch") != "branch" or rs.get("enforcement") != "active":
            continue
        if not on_default_branch(rs, default):
            continue
        if any(a.get("actor_type") == "Integration" and a.get("actor_id") == APP_ID for a in rs.get("bypass_actors") or []):
            bypassed = True
            continue
        have |= {r["type"] for r in rs.get("rules", [])}
    missing = [f"{default}: no active ruleset that {what}" for rule, what in NEEDED.items() if rule not in have]
    problems = [f"{default}: a ruleset lists the App (ID {APP_ID}) as a bypass actor"] if bypassed else []
    return problems, missing


def main():
    workflow = sys.argv[1] if len(sys.argv) > 1 else os.path.join(KIT, "WORKFLOW.md")
    repos = ceiling_writes(workflow)
    if not repos:
        print("check-forge-rules: no ceiling grants write; nothing to check", file=sys.stderr)
        return 1
    tok = token()
    failed = False
    for repo in repos:
        problems, missing = check(repo, tok)
        for p in problems:
            failed = True
            print(f"FAIL: {repo}: {p}")
        for m in missing:
            print(f"recommended: {repo}: {m}")
        if not problems and not missing:
            print(f"ok: {repo}: its default branch requires a pull request and blocks force pushes and deletion, with no bypass for the App")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
