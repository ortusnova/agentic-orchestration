"""An in-memory GitHub pull request API behind httpx.MockTransport."""

from __future__ import annotations

import json
import re
from typing import Any

import httpx

from workbench.delivery import GitHubClient

GITHUB_TOKEN = "github-token-for-tests-0123456789"
API = "https://github-api.test"
PULL_PATH = re.compile(r"^/repos/([^/]+)/([^/]+)/pulls/(\d+)$")
PULLS_PATH = re.compile(r"^/repos/([^/]+)/([^/]+)/pulls$")
COMMIT_PATH = re.compile(r"^/repos/([^/]+)/([^/]+)/commits/([^/]+)/(check-runs|status)$")
COMMENTS_PATH = re.compile(r"^/repos/([^/]+)/([^/]+)/issues/(\d+)/comments$")


class FakeGitHub:
    """Pull requests by ``owner/repo#number``; set one with :meth:`open`,
    :meth:`merge` or :meth:`close`. Every lookup is recorded."""

    def __init__(self, token: str = GITHUB_TOKEN):
        self.token = token
        self.pulls: dict[str, dict[str, Any]] = {}
        self.requests: list[str] = []
        self.unreachable = False
        self.status: int | None = None
        # The pull requests opened through the API, oldest first.
        self.opened: list[dict[str, Any]] = []
        # Check runs and commit statuses by commit, and each pull request's
        # conversation comments by ref.
        self.checks: dict[str, list[dict[str, Any]]] = {}
        self.statuses: dict[str, list[dict[str, Any]]] = {}
        self.comments: dict[str, list[dict[str, Any]]] = {}

    def branch_pull(self, ref: str, branch: str, sha: str) -> None:
        """An open pull request from ``branch``, its head at ``sha``, as Sortie opens one."""
        self._set(ref, head={"ref": branch, "sha": sha})

    def push(self, ref: str, sha: str) -> None:
        """A new commit on the pull request's branch."""
        self.pulls[ref]["head"] = {**self.pulls[ref]["head"], "sha": sha}

    def ci(self, sha: str, conclusion: str | None = "success") -> None:
        """One check run on ``sha``: completed with ``conclusion``, or still running when None."""
        run = {"name": "test", "status": "completed" if conclusion else "in_progress", "conclusion": conclusion}
        self.checks[sha] = [run]

    def say(self, ref: str, body: str, login: str = "operator") -> None:
        """A person comments on the pull request."""
        self.comments.setdefault(ref, []).append({"id": len(self.comments.get(ref, [])) + 1, "body": body,
                                                  "user": {"login": login, "type": "User"}})

    def _set(self, ref: str, **fields: Any) -> None:
        owner_repo, number = ref.split("#")
        self.pulls[ref] = {"number": int(number), "html_url": f"https://github.test/{owner_repo}/pull/{number}",
                           "state": "open", "merged": False, "merged_at": None, "merge_commit_sha": None, **fields}

    def open(self, ref: str) -> None:
        self._set(ref)

    def _head(self, ref: str) -> dict[str, Any]:
        head = (self.pulls.get(ref) or {}).get("head")
        return {"head": head} if head else {}

    def merge(self, ref: str, sha: str = "a" * 40) -> None:
        self._set(ref, state="closed", merged=True, merged_at="2026-10-30T12:00:00Z", merge_commit_sha=sha,
                  **self._head(ref))

    def close(self, ref: str) -> None:
        # GitHub reports a test merge commit for a closed pull request too: it is not a merge.
        self._set(ref, state="closed", merge_commit_sha="b" * 40, **self._head(ref))

    def lookups(self, ref: str) -> int:
        owner_repo, number = ref.split("#")
        return self.requests.count(f"/repos/{owner_repo}/pulls/{number}")

    def client(self, token: str | None = None) -> GitHubClient:
        return GitHubClient(API, self.token if token is None else token, transport=httpx.MockTransport(self.handle))

    def handle(self, request: httpx.Request) -> httpx.Response:
        if self.unreachable:
            raise httpx.ConnectError("connection refused", request=request)
        self.requests.append(request.url.path)
        if request.headers.get("authorization") != f"Bearer {self.token}":
            return httpx.Response(401, json={"message": "Bad credentials"})
        if self.status is not None:
            return httpx.Response(self.status, json={"message": "API rate limit exceeded"})
        listed = PULLS_PATH.match(request.url.path)
        if listed:
            return self._pulls(request, f"{listed[1]}/{listed[2]}")
        commit = COMMIT_PATH.match(request.url.path)
        if commit:
            sha = commit[3]
            if commit[4] == "check-runs":
                runs = self.checks.get(sha, [])
                return httpx.Response(200, json={"total_count": len(runs), "check_runs": runs})
            statuses = self.statuses.get(sha, [])
            return httpx.Response(200, json={"state": "success" if statuses else "pending", "statuses": statuses,
                                             "total_count": len(statuses)})
        thread = COMMENTS_PATH.match(request.url.path)
        if thread:
            ref = f"{thread[1]}/{thread[2]}#{thread[3]}"
            if ref not in self.pulls:
                return httpx.Response(404, json={"message": "Not Found"})
            if request.method == "POST":
                self.say(ref, json.loads(request.content)["body"], login="kit")
                return httpx.Response(201, json=self.comments[ref][-1])
            return httpx.Response(200, json=self.comments.get(ref, []))
        m = PULL_PATH.match(request.url.path)
        ref = f"{m[1]}/{m[2]}#{m[3]}" if m else None
        if ref not in self.pulls:
            return httpx.Response(404, json={"message": "Not Found"})
        return httpx.Response(200, json=self.pulls[ref])

    def _pulls(self, request: httpx.Request, owner_repo: str) -> httpx.Response:
        """List a repository's pull requests by head, or open one."""
        mine = [p for p in self.opened if p["repo"] == owner_repo]
        if request.method == "GET":
            head, state = request.url.params.get("head"), request.url.params.get("state", "open")
            heads = {p["ref"]: p["head"] for p in mine}
            for ref, pull in self.pulls.items():
                if ref.split("#")[0] == owner_repo and isinstance(pull.get("head"), dict):
                    heads.setdefault(ref, pull["head"]["ref"])
            # Newest first, as GitHub lists them.
            found = [self.pulls[ref] for ref in sorted(heads, key=lambda r: -self.pulls[r]["number"])
                     if (head is None or f"{owner_repo.split('/')[0]}:{heads[ref]}" == head)
                     and state in ("all", self.pulls[ref]["state"])]
            return httpx.Response(200, json=found)
        if request.method != "POST":
            return httpx.Response(405, json={"message": "Method Not Allowed"})
        body = json.loads(request.content)
        if not all(body.get(k) for k in ("title", "head", "base")):
            return httpx.Response(422, json={"message": "Validation Failed"})
        if any(p["head"] == body["head"] and self.pulls[p["ref"]]["state"] == "open" for p in mine):
            return httpx.Response(422, json={"message": "A pull request already exists"})
        ref = f"{owner_repo}#{100 + len(self.opened)}"
        self._set(ref)
        self.opened.append({"ref": ref, "repo": owner_repo, **body})
        return httpx.Response(201, json=self.pulls[ref])
