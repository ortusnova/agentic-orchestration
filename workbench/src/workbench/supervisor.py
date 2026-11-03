"""Sortie's supervisor, as the workbench shows it (agent-platform/run-supervision,
"The supervisor acts only through a scoped API, and each action is recorded").

The supervisor acts on Sortie's runs through a token that allows only a few
actions. Sortie records each one, refused or not, with the evidence it was
based on and its result, and serves the newest in its state as
``supervisor_actions`` (sortie#40):

    {"supervisor_actions": [{"id", "action", "route", "issue_identifier",
                             "evidence": "...", "result": "done" | "refused" | "rejected" | "failed",
                             "status", "detail", "pull_request": "owner/repo#N",
                             "merge_commit", "at"}],
     "supervisor": {"status": "active" | "paused" | "off", "paused_at", "paused_by"}}

``supervisor``, the supervisor session's status, comes with its pause and
resume (openspec sortie-supervises-runs, supervisor-session); a Sortie that
serves the actions without it reads as off, with nothing to pause. Every
Sortie's actions show, each beside the story it touched; the status and the
pause are those of the Sortie that dispatches the project. Nothing here
writes.
"""

from __future__ import annotations

import re
from typing import Any

from .questions import parked_at_of

# The actions the home lists, newest first, and those a story's details list.
HOME_ACTIONS = 20
STORY_ACTIONS = 10

_PULL_URL = re.compile(r"^https://github\.com/([^/]+/[^/]+)/pull/(\d+)")
_PULL_REF = re.compile(r"^([\w.-]+/[\w.-]+)#(\d+)$")
_SHA = re.compile(r"^[0-9a-f]{7,40}$")


def _words(code: str) -> str:
    return code.replace("_", " ").replace("-", " ").strip()


def _text(v: Any) -> str:
    if isinstance(v, bool):
        return "yes" if v else "no"
    if isinstance(v, (list, tuple)):
        return ", ".join(_text(x) for x in v)
    if isinstance(v, dict):
        return ", ".join(f"{_words(str(k))}: {_text(x)}" for k, x in v.items())
    return "" if v is None else str(v)


def pull_of(raw: Any) -> dict[str, Any] | None:
    """The pull request an action names: its ``owner/repo#number`` and address."""
    if isinstance(raw, str) and raw.strip():
        raw = raw.strip()
        m = _PULL_URL.match(raw)
        if m:
            return {"ref": f"{m.group(1)}#{m.group(2)}", "url": raw}
        m = _PULL_REF.match(raw)
        if m:
            return {"ref": raw, "url": f"https://github.com/{m.group(1)}/pull/{m.group(2)}"}
        return {"ref": raw, "url": None}
    if not isinstance(raw, dict):
        return None
    url = raw.get("url") or raw.get("html_url")
    url = url if isinstance(url, str) and url else None
    repo, number = raw.get("repository") or raw.get("repo"), raw.get("number")
    m = _PULL_URL.match(url or "")
    if not repo and m:
        repo = m.group(1)
    if number is None and m:
        number = m.group(2)
    if repo and number is not None:
        ref = f"{repo}#{number}"
    elif number is not None:
        ref = f"#{number}"
    elif url:
        ref = url
    else:
        return None
    if url is None and repo and number is not None and str(repo).count("/") == 1:
        url = f"https://github.com/{repo}/pull/{number}"
    return {"ref": ref, "url": url}


def evidence_of(raw: Any, pull: dict[str, Any] | None) -> list[dict[str, Any]]:
    """The evidence an action was based on, as named values. A commit of a
    GitHub pull request links to the commit."""
    if raw is None or raw == "" or raw == {} or raw == []:
        return []
    if isinstance(raw, str):
        return [{"name": None, "value": raw, "url": None}]
    if isinstance(raw, list):
        return [e for x in raw for e in evidence_of(x, pull)]
    if not isinstance(raw, dict):
        return [{"name": None, "value": _text(raw), "url": None}]
    out = []
    repo_url = None
    if pull and pull.get("url"):
        m = _PULL_URL.match(pull["url"])
        repo_url = f"https://github.com/{m.group(1)}" if m else None
    for k, v in raw.items():
        value = _text(v)
        url = value if value.startswith(("https://", "http://")) else None
        if url is None and repo_url and "commit" in str(k) and _SHA.match(value):
            url = f"{repo_url}/commit/{value}"
        out.append({"name": _words(str(k)), "value": value, "url": url})
    return out


def _merge_commit(evidence: list[dict[str, Any]], sha: Any, pull: dict[str, Any] | None) -> list[dict[str, Any]]:
    """The merge commit Sortie names beside the evidence, as one more named value."""
    if not isinstance(sha, str) or not sha or any(e["value"] == sha for e in evidence):
        return evidence
    return evidence + evidence_of({"merge_commit": sha}, pull)


def action_of(raw: dict[str, Any], key_of: dict[str, str], nodes: dict[str, dict[str, Any]],
              sortie: str | None = None) -> dict[str, Any]:
    """One recorded action, with the story it touched."""
    ident = raw.get("issue_identifier") or raw.get("identifier")
    ident = ident if isinstance(ident, str) and ident else None
    key = key_of.get(ident) if ident else None
    node = nodes.get(key) if key else None
    pull = pull_of(raw.get("pull_request") or raw.get("pull"))
    result = raw.get("result")
    if isinstance(result, dict):
        detail = result.get("detail") or result.get("message") or raw.get("detail")
        result = result.get("status") or result.get("outcome")
    else:
        detail = raw.get("detail") or raw.get("reason") or raw.get("message")
    kind = raw.get("action") or raw.get("kind") or "unknown"
    return {
        "id": str(raw.get("id") or ""),
        "at": parked_at_of({"at": raw.get("at") or raw.get("time") or raw.get("recorded_at")}),
        "action": str(kind),
        "identifier": ident,
        "key": key,
        "label": key.split("/", 1)[1] if key and "/" in key else key,
        "title": (node or {}).get("title"),
        "url": ((node or {}).get("tracker") or {}).get("url"),
        "pull": pull,
        "evidence": _merge_commit(evidence_of(raw.get("evidence"), pull), raw.get("merge_commit"), pull),
        "result": str(result or "unknown"),
        "detail": _text(detail) or None,
        "sortie": sortie,
    }


def supervisor_view(state: dict[str, Any] | None, nodes: list[dict[str, Any]],
                    peers: list[tuple[str, dict[str, Any]]] = (), own: str | None = None,
                    ) -> tuple[dict[str, Any] | None, dict[str, list[dict[str, Any]]]]:
    """The supervisor's status and its recent actions on this project's
    issues, newest first, and the actions on each, by plan key; None
    and none when no Sortie reports a supervisor. ``state`` is the state of
    the Sortie that dispatches the project, named ``own``; ``peers`` are the
    other Sorties' names and states. An action on another project's issue is
    left out; one that names no issue stays."""
    sup = (state or {}).get("supervisor")
    sup = sup if isinstance(sup, dict) else None
    sources = [(own, state or {})] + [(name, s or {}) for name, s in peers]
    if sup is None and not any(isinstance(s.get("supervisor_actions"), list) for _, s in sources):
        return None, {}
    node_of = {n["plan_key"]: n for n in nodes if n.get("plan_key")}
    key_of = {n["tracker"]["identifier"]: n["plan_key"] for n in nodes
              if n.get("plan_key") and (n.get("tracker") or {}).get("identifier")}
    raws: list[tuple[str | None, dict[str, Any]]] = [(own, a) for a in (sup or {}).get("actions") or []]
    for name, s in sources:
        raws += [(name, a) for a in s.get("supervisor_actions") or []]
    seen: set[tuple[str | None, str]] = set()
    recorded = []
    for name, a in raws:
        if not isinstance(a, dict):
            continue
        if a.get("id") is not None:
            if (name, str(a["id"])) in seen:
                continue
            seen.add((name, str(a["id"])))
        recorded.append(action_of(a, key_of, node_of, name))
    recorded = [a for a in recorded if a["identifier"] is None or a["key"] is not None]
    # Newest first; of two at the same time, the one Sortie recorded later.
    order = sorted(range(len(recorded)), key=lambda i: (recorded[i]["at"] or 0.0, i), reverse=True)
    actions = [recorded[i] for i in order]
    if sup is None:
        status, reason = "off", ("Sortie records the supervisor's actions but serves no status for it: "
                                 "it runs no supervisor session to pause.")
    else:
        status, reason = sup.get("status"), sup.get("reason") or None
        if not isinstance(status, str) or not status:
            status = "paused" if sup.get("paused") else "active"
    by_key: dict[str, list[dict[str, Any]]] = {}
    for a in actions:
        if a["key"] and len(by_key.setdefault(a["key"], [])) < STORY_ACTIONS:
            by_key[a["key"]].append(a)
    view = {
        "status": status,
        "paused_at": parked_at_of({"at": (sup or {}).get("paused_at")}),
        "paused_by": (sup or {}).get("paused_by") or None,
        "reason": reason,
        "actions": actions[:HOME_ACTIONS],
        "total": len(actions),
    }
    return view, by_key
