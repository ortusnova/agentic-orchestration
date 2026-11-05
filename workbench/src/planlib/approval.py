"""Story digests: what an approval covers.

Approval itself is recorded by the plan workbench, never in a breakdown
file, because the planning agent writes those files. planlib only computes
the digest a story's approval must match, and reads the old format's
whole-breakdown approval so the workbench can import it once.
"""

from __future__ import annotations

import hashlib
import json
from typing import Any

# The hosts a post-deploy check runs on when its declaration names none.
DEFAULT_CHECK_HOSTS = ("p1",)


def canonical_json(value: Any) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode("utf-8")


def story_digest(deliverable: dict) -> str:
    """The digest of one story's content: every field of its deliverable."""
    return "sha256:" + hashlib.sha256(canonical_json(deliverable)).hexdigest()


def post_deploy_check(deliverable: dict) -> dict | None:
    """The deliverable's post-deploy check, keys in a fixed order and its
    default hosts filled in; None when it declares none."""
    check = deliverable.get("post_deploy_check")
    if not check:
        return None
    return {"check": check["check"], "args": list(check.get("args") or []),
            "hosts": list(check.get("hosts") or DEFAULT_CHECK_HOSTS)}


def resolved(doc: dict, deliverable: dict) -> dict:
    """The deliverable as an approval covers it: with the breakdown's default
    repository filled in when it names none, so changing that default
    returns the story to draft, and a post-deploy check's default hosts
    filled in, so naming them changes nothing. ``delivered_by`` records how
    the work was done, not what it is, so naming the pull request that
    delivered a story neither returns it to draft nor counts as an edit to a
    started one."""
    if "delivered_by" in deliverable:
        deliverable = {k: v for k, v in deliverable.items() if k != "delivered_by"}
    if deliverable.get("post_deploy_check"):
        deliverable = {**deliverable, "post_deploy_check": post_deploy_check(deliverable)}
    if doc.get("repository") and not deliverable.get("repository"):
        return {**deliverable, "repository": doc["repository"]}
    return deliverable


def deliverables_digest(doc: dict) -> str:
    """The old format's digest over all of a breakdown's deliverables."""
    return "sha256:" + hashlib.sha256(canonical_json(doc.get("deliverables", []))).hexdigest()


def legacy_approved(doc: dict) -> bool:
    """Whether a breakdown carries an approval made under the old format that
    still matches its deliverables. Used only to import it once."""
    return doc.get("status") == "approved" and doc.get("approved_digest") == deliverables_digest(doc)
