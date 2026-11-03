"""An in-memory Sortie plan API behind httpx.MockTransport."""

from __future__ import annotations

import json
from typing import Any

import httpx

from tests.workbench.conftest import SORTIE_TOKEN
from workbench.sortie import SortieClient


# What the supervisor's scoped token allows (agent-platform/run-supervision):
# Sortie refuses every other action for it, and records each one either way.
SUPERVISOR_ACTIONS = {"answer_question", "release_park", "grant_sessions", "record_merge", "start_fix_run",
                      "mark_draft", "merge_pull", "file_issue", "request_proof"}


def built_unapproved(n: dict[str, Any]) -> bool:
    """A ui story that covers a requirement: Sortie syncs and releases it
    without an approval, and refuses one that names an approver."""
    return n.get("kind") == "story" and n.get("review") == "ui" and bool(n.get("covers"))


def synced(n: dict[str, Any]) -> bool:
    return n["approval"] == "approved" or built_unapproved(n)


def releases(n: dict[str, Any]) -> bool:
    if n.get("review") == "ui":
        return built_unapproved(n)
    return bool(n.get("release")) and n["approval"] == "approved"


class FakeSortie:
    """Serves tracker workspace projects; ``nodes`` and ``edges`` are the
    configured project's (``T``), the one Sortie dispatches."""

    DEFAULT = "T"

    def __init__(self):
        # Tracker projects of the workspace: key -> {"name", "problems"}.
        self.projects: dict[str, dict[str, Any]] = {self.DEFAULT: {"name": "Tracker", "problems": []}}
        self.ensure_error: tuple[int, str] | None = None
        self.nodes: list[dict[str, Any]] = []
        self.edges: list[dict[str, str]] = []
        self.capabilities = ["create_issue", "update_issue", "set_parent", "link_dependency"]
        self.down = False
        self.requests: list[tuple[str, str, Any]] = []
        self.previews: dict[str, dict[str, Any]] = {}
        self.applied: list[dict[str, Any]] = []
        # Plan control: the state names, and the plan keys pulled back or cancelled.
        self.plan_states: dict[str, str] | None = None
        self.pulled_back: list[str] = []
        self.cancelled: list[str] = []
        self.claimed: set[str] = set()
        # Workers, by tracker identifier: running entries (as /api/v1/state
        # lists them), retry entries, and the brokers that reported.
        self.running: dict[str, dict[str, Any]] = {}
        self.retrying: dict[str, dict[str, Any]] = {}
        self.brokers: dict[str, dict[str, str]] = {}
        self.state_down = False
        # Sortie's agent concurrency cap, and the runs that wait on a budget
        # or a person, as /api/v1/state reports them.
        self.max_agents: int | None = 3
        self.budget_exhausted: list[dict[str, Any]] = []
        self.parked: list[dict[str, Any]] = []
        # The full stop statement of each park, by identifier, as the
        # per-issue response carries it; and the answers people sent.
        self.park_details: dict[str, dict[str, Any]] = {}
        self.answers: list[dict[str, Any]] = []
        self.answer_releases = True
        # Sync: no connection at all; the next apply is refused as stale;
        # refresh fails; the refreshes Sortie was asked for.
        self.unreachable = False
        self.stale_once = False
        self.refresh_down = False
        self.refreshes = 0
        self.released: set[str] = set()
        # What the workbench shows of the workers (agent-platform/run-supervision):
        # members merged into /api/v1/state as Sortie serves them (host, workers,
        # pulls, waits, held, dispatch_paused), the sessions people granted, and
        # the run history of issues that no longer run: identifier ->
        # {"dispatch_id", "events"}.
        self.extra_state: dict[str, Any] = {}
        self.grants: list[dict[str, Any]] = []
        self.grants_supported = True
        self.history: dict[str, dict[str, Any]] = {}
        # The supervisor, as the state lists it, or None for a Sortie that
        # has none; and the pauses and resumes people asked for.
        self.supervisor: dict[str, Any] | None = {"status": "active", "paused_at": None, "paused_by": None, "actions": []}
        self.supervisor_controls: list[dict[str, str]] = []

    def supervise(self, action: str, identifier: str | None = None, *, evidence: Any = None,
                  pull: dict[str, Any] | None = None, at: str = "2026-11-03T10:00:00Z", detail: str | None = None) -> dict[str, Any]:
        """The supervisor asks for ``action`` with its scoped token. Sortie
        does it only if the token allows it and the supervisor is not paused,
        and records the action, its evidence and its result either way."""
        assert self.supervisor is not None, "this Sortie has no supervisor"
        paused = self.supervisor["status"] != "active"
        allowed = action in SUPERVISOR_ACTIONS and not paused
        if detail is None and not allowed:
            detail = "the supervisor is paused" if paused else f"{action.replace('_', ' ')} is not the supervisor's to do"
        record = {"id": f"a-{len(self.supervisor['actions']) + 1}", "at": at, "action": action,
                  "issue_identifier": identifier, "pull_request": pull, "evidence": evidence,
                  "result": "done" if allowed else "refused", "detail": detail}
        self.supervisor["actions"].append(record)
        return record

    def run(self, identifier: str, session_id: str, stage: str = "implement", broker: str | None = None, token: str = "viewer-secret", **extra):
        """Start a run of the issue; with ``broker``, its broker has reported."""
        self.retrying.pop(identifier, None)
        self.running[identifier] = {"issue_identifier": identifier, "session_id": session_id, "rule_name": stage,
                                    "turn_count": 1, "last_message": "working", "held": False, "holder": "",
                                    "broker": {"http_url": broker} if broker else None, **extra}
        if broker:
            self.brokers[identifier] = {"http_url": broker, "viewer_token": token}
        else:
            self.brokers.pop(identifier, None)

    def end(self, identifier: str, retry_attempt: int | None = None) -> None:
        self.running.pop(identifier, None)
        self.brokers.pop(identifier, None)
        if retry_attempt is not None:
            self.retrying[identifier] = {"issue_identifier": identifier, "attempt": retry_attempt}

    def park(self, identifier: str, statement: str, parked_at: str = "2026-11-01T18:35:26Z", reason: str = "agent_blocked",
             listed: str | None = None) -> None:
        """Park the issue with the agent's stop statement. ``listed`` is the
        statement as the state lists it, when it is shorter."""
        self.end(identifier)
        self.unpark(identifier)
        self.parked.append({"issue_identifier": identifier, "reason": reason, "parked_at": parked_at,
                            "statement": statement if listed is None else listed})
        self.park_details[identifier] = {"statement": statement, "parked_at": parked_at, "reason": reason}

    def unpark(self, identifier: str) -> None:
        """Release the park, as a person's gesture in the tracker does."""
        self.parked = [p for p in self.parked if p["issue_identifier"] != identifier]
        self.park_details.pop(identifier, None)

    def issue(self, plan_key, kind="story", state="Todo", parent=None, runtime=None, terminal=False, **extra):
        node = {
            "id": f"id-{len(self.nodes) + 1}", "identifier": f"T-{len(self.nodes) + 1}", "plan_key": plan_key,
            "kind": kind, "title": f"tracker {plan_key}", "state": state, "terminal": terminal,
            "url": f"https://tracker/{len(self.nodes) + 1}", "parent_id": parent, "runtime": runtime, **extra,
        }
        self.nodes.append(node)
        return node

    def client(self) -> SortieClient:
        return SortieClient("http://sortie.test", SORTIE_TOKEN, transport=httpx.MockTransport(self.handle))

    def handle(self, request: httpx.Request) -> httpx.Response:
        body = json.loads(request.content) if request.content else None
        if self.unreachable:
            raise httpx.ConnectError("connection refused", request=request)
        self.requests.append((request.method, request.url.path, body))
        if request.headers.get("authorization") != f"Bearer {SORTIE_TOKEN}":
            return httpx.Response(401, json={"error": {"code": "unauthorized", "message": "token"}})
        if self.down:
            return httpx.Response(502, json={"error": {"code": "tracker_transport_error", "message": "tracker down"}})
        path = request.url.path
        project = request.url.params.get("project") or self.DEFAULT
        if path.startswith("/api/v1/plan/projects/"):
            return self.handle_project(request.method, path.rsplit("/", 1)[1], body)
        if path.startswith("/api/v1/plan/") and project not in self.projects:
            return httpx.Response(404, json={"error": {"code": "project_not_found", "message": f"plan: project not found: {project}"}})
        if path == "/api/v1/plan/graph":
            own = project == self.DEFAULT
            return httpx.Response(200, json={
                "project": project, "dispatched": own, "generated_at": "now", "capabilities": self.capabilities,
                "nodes": self.nodes if own else [], "edges": self.edges if own else [],
                "plan_states": self.plan_states,
            })
        if path in ("/api/v1/plan/pull-back", "/api/v1/plan/cancel"):
            key = body["plan_key"]
            node = next((n for n in self.nodes if n["plan_key"] == key), None)
            if node is None:
                return httpx.Response(404, json={"error": {"code": "issue_not_found", "message": key}})
            if path.endswith("pull-back"):
                if key in self.claimed:
                    return httpx.Response(409, json={"error": {"code": "already_claimed", "message": key}})
                self.pulled_back.append(key)
                node["state"] = (self.plan_states or {}).get("backlog", "Backlog")
            else:
                self.cancelled.append(key)
                node["state"], node["terminal"] = (self.plan_states or {}).get("cancel", "Canceled"), True
            return httpx.Response(200, json={"plan_key": key})
        if path == "/api/v1/plan/capabilities":
            return httpx.Response(200, json={"capabilities": self.capabilities, "plan_key_storage": "custom_field"})
        if path == "/api/v1/plan/sync":
            graph = body["graph"]
            if any(n.get("review") == "ui" and ("approved_by" in n or "approved_at" in n) for n in graph["nodes"]):
                return httpx.Response(400, json={"error": {"code": "invalid_graph",
                                                           "message": "a ui story carries no approved_by or approved_at"}})
            if not body.get("apply"):
                pid = f"p{len(self.previews) + 1}"
                approved = [n for n in graph["nodes"] if synced(n)]
                missing = [] if "link_dependency" in self.capabilities else ["link_dependency"]
                preview = {"counts": {"creates": len(approved), "updates": 0, "parents": 0,
                                      "relations_added": len(graph["blocks"]), "relations_removed": 0, "orphans": 0},
                           "missing_capabilities": missing, "skipped_drafts": [], "source_digest": graph["source_digest"]}
                self.previews[pid] = preview
                return httpx.Response(200, json={"preview_id": pid, "preview": preview, "capabilities": self.capabilities})
            if body["preview_id"] not in self.previews or self.stale_once:
                self.stale_once = False
                return httpx.Response(409, json={"error": {"code": "stale_preview", "message": "unknown preview"}})
            self.applied.append({**body, "project": project})
            released = [{"plan_key": n["plan_key"], "issue_id": "", "identifier": ""} for n in graph["nodes"]
                        if releases(n) and n["plan_key"] not in self.released]
            self.released.update(r["plan_key"] for r in released)
            return httpx.Response(200, json={"result": {"created": [], "released": released, "orphans": [], "failed": None}})
        if path == "/api/v1/refresh":
            self.refreshes += 1
            if self.refresh_down:
                return httpx.Response(503, json={"error": {"code": "unavailable", "message": "refresh"}})
            return httpx.Response(202, json={"queued": True})
        if path == "/api/v1/state":
            if self.state_down:
                return httpx.Response(503, json={"error": {"code": "unavailable", "message": "state"}})
            agent = {"max_concurrent_agents": self.max_agents} if self.max_agents is not None else {}
            state = {"running": list(self.running.values()), "retrying": list(self.retrying.values()),
                     "budget_exhausted": self.budget_exhausted, "parked": self.parked, "config": {"agent": agent},
                     **self.extra_state}
            if self.supervisor is not None:
                state["supervisor"] = self.supervisor
            return httpx.Response(200, json=state)
        if path in ("/api/v1/supervisor/pause", "/api/v1/supervisor/resume") and request.method == "POST":
            if self.supervisor is None:
                return httpx.Response(404, json={"error": {"code": "not_found", "message": path}})
            author = (body or {}).get("author")
            if not author:
                return httpx.Response(400, json={"error": {"code": "invalid_request", "message": "author"}})
            op = path.rsplit("/", 1)[1]
            self.supervisor_controls.append({"op": op, "author": author})
            if op == "pause":
                self.supervisor.update(status="paused", paused_at="2026-11-03T11:00:00Z", paused_by=author)
            else:
                self.supervisor.update(status="active", paused_at=None, paused_by=None)
            return httpx.Response(200, json={k: v for k, v in self.supervisor.items() if k != "actions"})
        if path.startswith("/api/v1/") and path.endswith("/answer") and request.method == "POST":
            ident = path.removeprefix("/api/v1/").removesuffix("/answer")
            if ident not in self.park_details:
                return httpx.Response(409, json={"error": {"code": "not_parked", "message": f"{ident} is not parked"}})
            if not (body or {}).get("text") or not (body or {}).get("author"):
                return httpx.Response(400, json={"error": {"code": "invalid_answer", "message": "text and author"}})
            self.answers.append({"identifier": ident, "text": body["text"], "author": body["author"]})
            if self.answer_releases:
                self.unpark(ident)
            return httpx.Response(200, json={"issue_identifier": ident, "released": True})
        if path.startswith("/api/v1/") and path.endswith("/sessions") and request.method == "POST":
            ident = path.removeprefix("/api/v1/").removesuffix("/sessions")
            if not self.grants_supported:
                return httpx.Response(404, json={"error": {"code": "not_found", "message": path}})
            add = body.get("add") if isinstance(body, dict) else None
            if not isinstance(add, int) or isinstance(add, bool) or not 1 <= add <= 64:
                return httpx.Response(400, json={"error": {"code": "invalid_request", "message": "add from 1 to 64"}})
            held = any(b["issue_identifier"] == ident for b in self.budget_exhausted)
            if not held and ident not in self.running and ident not in self.retrying and ident not in self.park_details:
                return httpx.Response(404, json={"error": {"code": "issue_not_found", "message": f"issue {ident} is not known"}})
            self.grants.append({"identifier": ident, "add": add, "by": body.get("by") or "operator",
                                "reason": body.get("reason") or ""})
            # Sortie raises the issue's budget and dispatches it again.
            entry = next((b for b in self.budget_exhausted if b["issue_identifier"] == ident), {})
            self.budget_exhausted = [b for b in self.budget_exhausted if b["issue_identifier"] != ident]
            granted = sum(g["add"] for g in self.grants if g["identifier"] == ident)
            budget = entry.get("budget_sessions")
            return httpx.Response(200, json={"issue_identifier": ident, "issue_id": f"id-{ident}",
                                             "used_sessions": entry.get("used_sessions", 0),
                                             "budget_sessions": budget + add if isinstance(budget, int) else None,
                                             "granted_sessions": granted})
        if path.startswith("/api/v1/runs/") and path.endswith("/events"):
            dispatch = path.removeprefix("/api/v1/runs/").removesuffix("/events")
            run = next((h for h in self.history.values() if h["dispatch_id"] == dispatch), None)
            if run is None:
                return httpx.Response(404, json={"error": {"code": "run_events_not_retained", "message": dispatch}})
            return httpx.Response(200, json={"dispatch_id": dispatch, "first_seq": 1, "events": run["events"]})
        if path.startswith("/api/v1/") and path.endswith("/broker"):
            ident = path.removeprefix("/api/v1/").removesuffix("/broker")
            if ident not in self.brokers:
                return httpx.Response(404, json={"error": {"code": "broker_not_found", "message": ident}})
            return httpx.Response(200, json=self.brokers[ident])
        if path.startswith("/api/v1/") and path.count("/") == 3:
            ident = path.removeprefix("/api/v1/")
            if ident not in self.running and ident not in self.retrying and ident not in self.park_details and ident not in self.history:
                return httpx.Response(404, json={"error": {"code": "issue_not_found", "message": ident}})
            past = self.history.get(ident)
            return httpx.Response(200, json={"issue_identifier": ident, "running": self.running.get(ident),
                                             "retry": self.retrying.get(ident), "parked": self.park_details.get(ident),
                                             "history": {"runs": [{"dispatch_id": past["dispatch_id"]}]} if past else None,
                                             "recent_events": (past["events"][-50:] if past else [])})
        return httpx.Response(404, json={"error": {"code": "not_found", "message": path}})

    def handle_project(self, method: str, key: str, body: Any) -> httpx.Response:
        def view(created: bool) -> dict[str, Any]:
            p = self.projects.get(key)
            return {"key": key, "name": p["name"] if p else None, "url": f"https://tracker/{key}" if p else None,
                    "exists": p is not None, "created": created, "ready": bool(p) and not p["problems"],
                    "dispatched": key == self.DEFAULT, "problems": list(p["problems"]) if p else []}

        if method == "GET":
            return httpx.Response(200, json=view(False))
        if self.ensure_error:
            status, code = self.ensure_error
            return httpx.Response(status, json={"error": {"code": code, "message": "ensure failed"}})
        created = key not in self.projects
        if created:
            self.projects[key] = {"name": (body or {}).get("name") or key, "problems": []}
        return httpx.Response(200, json=view(created))
