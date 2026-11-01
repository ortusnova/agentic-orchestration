"""An in-memory GitHub pull request API behind httpx.MockTransport."""

from __future__ import annotations

import re
from typing import Any

import httpx

from workbench.delivery import GitHubClient

GITHUB_TOKEN = "github-token-for-tests-0123456789"
API = "https://github-api.test"
PULL_PATH = re.compile(r"^/repos/([^/]+)/([^/]+)/pulls/(\d+)$")


class FakeGitHub:
    """Pull requests by ``owner/repo#number``; set one with :meth:`open`,
    :meth:`merge` or :meth:`close`. Every lookup is recorded."""

    def __init__(self, token: str = GITHUB_TOKEN):
        self.token = token
        self.pulls: dict[str, dict[str, Any]] = {}
        self.requests: list[str] = []
        self.unreachable = False
        self.status: int | None = None

    def _set(self, ref: str, **fields: Any) -> None:
        owner_repo, number = ref.split("#")
        self.pulls[ref] = {"number": int(number), "html_url": f"https://github.test/{owner_repo}/pull/{number}",
                           "state": "open", "merged": False, "merged_at": None, "merge_commit_sha": None, **fields}

    def open(self, ref: str) -> None:
        self._set(ref)

    def merge(self, ref: str, sha: str = "a" * 40) -> None:
        self._set(ref, state="closed", merged=True, merged_at="2026-10-30T12:00:00Z", merge_commit_sha=sha)

    def close(self, ref: str) -> None:
        # GitHub reports a test merge commit for a closed pull request too: it is not a merge.
        self._set(ref, state="closed", merge_commit_sha="b" * 40)

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
        m = PULL_PATH.match(request.url.path)
        ref = f"{m[1]}/{m[2]}#{m[3]}" if m else None
        if ref not in self.pulls:
            return httpx.Response(404, json={"message": "Not Found"})
        return httpx.Response(200, json=self.pulls[ref])
