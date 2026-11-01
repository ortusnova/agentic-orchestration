"""Write the small plan fake_sortie.py serves into a new spec store.

Usage: seed_store.py <store-dir>

accounts/a3, then billing/b1 (depends on a3) and billing/b2 (review by
acceptance criteria). Each story covers a requirement of its change's spec,
so an approver can approve it. The store must not exist yet.
"""

import sys
from pathlib import Path

import yaml
from workbench.scaffold import _git, scaffold_store


def story(change, id_, title, depends_on=(), **extra):
    return {"id": id_, "title": title, "kind": "story", "depends_on": list(depends_on), "covers": [f"{change}#{title}"],
            "acceptance_criteria": [f"{title} works"], **extra}


def spec(stories):
    out = "## ADDED Requirements\n"
    for s in stories:
        out += (f"\n### Requirement: {s['title']}\nThe system SHALL do this: {s['title'].lower()}.\n\n"
                f"#### Scenario: It works\n- **WHEN** a person asks for it\n- **THEN** {s['title'].lower()} works\n")
    return out


def main() -> int:
    root = Path(sys.argv[1])
    scaffold_store(root, "check")
    os_ = root / "openspec"
    (os_ / "plan.yaml").write_text(yaml.safe_dump({"schema_version": 1, "changes": [
        {"name": "accounts", "depends_on": []}, {"name": "billing", "depends_on": ["accounts"]}]}, sort_keys=False))
    for change, stories in {
        "accounts": [story("accounts", "a3", "Create the accounts table")],
        "billing": [story("billing", "b1", "Charge an account", ["accounts/a3"]),
                    story("billing", "b2", "Price list", review="criteria")],
    }.items():
        d = os_ / "changes" / change
        (d / "specs" / change).mkdir(parents=True)
        (d / "proposal.md").write_text(f"## Why\n\nThe check plan needs a {change} change that an approver can approve.\n\n"
                                       f"## What Changes\n\n- {change}\n")
        (d / "specs" / change / "spec.md").write_text(spec(stories))
        (d / "breakdown.yaml").write_text(yaml.safe_dump({"schema_version": 1, "deliverables": stories}, sort_keys=False))
    _git(root, "add", "-A")
    _git(root, "-c", "user.name=check", "-c", "user.email=check@localhost", "commit", "-qm", "Seed the check plan")
    return 0


if __name__ == "__main__":
    sys.exit(main())
