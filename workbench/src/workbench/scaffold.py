"""Create a project's empty spec store: the planning schemas, an empty plan, and git.
Keep an existing store's schemas on the ones the workbench ships
(agent-platform/store-upkeep)."""

from __future__ import annotations

import hashlib
import os
import re
import shutil
import subprocess
import tempfile
from dataclasses import dataclass, field
from importlib import resources
from pathlib import Path

PLAN_YAML = "# The order between changes. Format: openspec/schemas/planning/plan.schema.json\nschema_version: 1\nchanges: []\n"
CONFIG_YAML = "schema: spec-driven-planned\n"
UPGRADE_MESSAGE = "Update the planning schemas"
UPGRADE_BRANCH = "workbench/planning-schemas-"
PLANNING = Path("openspec") / "schemas" / "planning"
_AUTHOR = ("-c", "user.name=plan workbench", "-c", "user.email=workbench@localhost")
# A push or a lookup on the upstream never waits for a password, nor for ever.
_REMOTE_ENV = {"GIT_TERMINAL_PROMPT": "0"}
_REMOTE_TIMEOUT = 120

GITHUB_REMOTE = re.compile(
    r"^(?:https://(?:[^@/]+@)?github\.com/|ssh://git@github\.com/|git@github\.com:)"
    r"(?P<owner>[^/]+)/(?P<repo>[^/]+?)(?:\.git)?/?$"
)


class ScaffoldError(Exception):
    pass


def scaffold_store(root: Path, name: str) -> None:
    """Create the store at ``root``, which must not exist yet."""
    root = Path(root)
    if root.exists():
        raise ScaffoldError(f"{root} already exists")
    openspec = root / "openspec"
    template = resources.files("workbench").joinpath("store_template", "openspec", "schemas")
    try:
        (openspec / "changes").mkdir(parents=True)
        (openspec / "specs").mkdir()
        with resources.as_file(template) as src:
            shutil.copytree(src, openspec / "schemas")
        (openspec / "config.yaml").write_text(CONFIG_YAML)
        (openspec / "plan.yaml").write_text(PLAN_YAML)
        for d in ("changes", "specs"):
            (openspec / d / ".gitkeep").touch()
        _git(root, "init", "-q")
        _git(root, "add", "-A")
        _git(root, *_AUTHOR, "commit", "-qm", f"Create the spec store for {name}")
    except BaseException:
        shutil.rmtree(root, ignore_errors=True)
        raise


@dataclass(frozen=True)
class Upstream:
    """What the store's checked-out branch tracks."""

    branch: str  # the store's branch
    remote: str  # the remote's name
    base: str  # the branch on the remote
    url: str  # the remote's URL, as configured

    def github_repo(self) -> tuple[str, str] | None:
        """``(owner, repo)`` when the remote is on GitHub."""
        m = GITHUB_REMOTE.match(self.url)
        return (m["owner"], m["repo"]) if m else None


@dataclass
class SchemaUpgrade:
    """The workbench's schema files that differ in the store, and what was done about them."""

    # Each store path, relative to the root, with the content the workbench ships.
    changed: dict[str, bytes] = field(default_factory=dict)
    upstream: Upstream | None = None
    # The files were written into the store, and committed if it is a git checkout.
    written: bool = False
    # The branch that carries them to the upstream; None while nothing needs one.
    branch: str | None = None
    pushed: bool = False

    @property
    def paths(self) -> list[str]:
        return sorted(self.changed)

    def replaced(self) -> dict[str, str]:
        """The shipped planning schemas the store's files lack yet, by kind:
        until the upgrade merges, the plan is validated against these."""
        if self.written:
            return {}
        out = {}
        for path, data in self.changed.items():
            p = Path(path)
            if p.parent == PLANNING and p.name.endswith(".schema.json"):
                out[p.name.removesuffix(".schema.json")] = data.decode()
        return out


def shipped_changes(root: Path) -> dict[str, bytes]:
    """The workbench's own schema files that differ in the store at ``root``.
    Only files the store already has count: a store that never took the
    workbench's schemas keeps its own."""
    root = Path(root)
    dest = root / "openspec" / "schemas"
    if not dest.is_dir():
        return {}
    template = resources.files("workbench").joinpath("store_template", "openspec", "schemas")
    changed: dict[str, bytes] = {}
    with resources.as_file(template) as src:
        for f in sorted(Path(src).rglob("*")):
            target = dest / f.relative_to(src)
            if f.is_file() and target.is_file() and target.read_bytes() != f.read_bytes():
                changed[target.relative_to(root).as_posix()] = f.read_bytes()
    return changed


def upgrade_schemas(root: Path) -> SchemaUpgrade:
    """Bring the store's schema files up to the ones this workbench ships.

    A store whose branch tracks an upstream is left as it is: the timer that
    fast-forwards it must find nothing of its own on it, and
    :func:`push_upgrade` carries the files there instead. Any other store
    gets them written in, and committed on its branch."""
    root = Path(root)
    changed = shipped_changes(root)
    if not changed:
        return SchemaUpgrade()
    is_git = (root / ".git").exists()
    upstream = upstream_of(root) if is_git else None
    up = SchemaUpgrade(changed, upstream)
    if upstream is not None:
        return up
    for path, data in changed.items():
        (root / path).write_bytes(data)
    up.written = True
    if is_git:
        _git(root, "add", "--", *up.paths)
        _git(root, *_AUTHOR, "commit", "-qm", UPGRADE_MESSAGE, "--", *up.paths)
    return up


def upstream_of(root: Path) -> Upstream | None:
    """What the store's checked-out branch tracks on a remote, if it does."""
    branch = _git_maybe(root, "symbolic-ref", "-q", "--short", "HEAD")
    if not branch:
        return None
    remote = _git_maybe(root, "config", f"branch.{branch}.remote")
    merge = _git_maybe(root, "config", f"branch.{branch}.merge")
    if not remote or remote == "." or not merge or not merge.startswith("refs/heads/"):
        return None
    url = _git_maybe(root, "config", f"remote.{remote}.url") or ""
    return Upstream(branch, remote, merge.removeprefix("refs/heads/"), url)


def branch_name(changed: dict[str, bytes]) -> str:
    """The upgrade branch, named after the files it carries."""
    h = hashlib.sha256()
    for path in sorted(changed):
        h.update(path.encode() + b"\0" + hashlib.sha256(changed[path]).digest())
    return UPGRADE_BRANCH + h.hexdigest()[:12]


def push_upgrade(root: Path, up: SchemaUpgrade) -> None:
    """Commit the shipped files onto what the store's branch tracks, on a
    branch named after them, and push it, unless the upstream has it already.
    The store's branch, index and files stay as they are. Sets ``up.branch``,
    left None when the upstream already holds the files."""
    root = Path(root)
    if up.upstream is None or up.written or not up.changed:
        return
    remote = up.upstream.remote
    base = _git_maybe(root, "rev-parse", "--verify", "-q", f"{up.upstream.branch}@{{upstream}}")
    if not base:
        raise ScaffoldError(f"{up.upstream.branch} tracks {remote}/{up.upstream.base}, which was never fetched")
    with tempfile.TemporaryDirectory() as tmp:
        env = {**os.environ, "GIT_INDEX_FILE": str(Path(tmp) / "index")}
        _git(root, "read-tree", base, env=env)
        for path in up.paths:
            blob = _git(root, "hash-object", "-w", "--stdin", input=up.changed[path]).strip()
            _git(root, "update-index", "--add", "--cacheinfo", f"100644,{blob},{path}", env=env)
        tree = _git(root, "write-tree", env=env).strip()
    if tree == _git(root, "rev-parse", f"{base}^{{tree}}").strip():
        return
    branch = branch_name(up.changed)
    up.branch = branch
    env = {**os.environ, **_REMOTE_ENV}
    if _git(root, "ls-remote", "--heads", remote, f"refs/heads/{branch}", env=env, timeout=_REMOTE_TIMEOUT).strip():
        return
    commit = _git(root, *_AUTHOR, "commit-tree", tree, "-p", base, "-m", UPGRADE_MESSAGE).strip()
    _git(root, "push", "-q", remote, f"{commit}:refs/heads/{branch}", env=env, timeout=_REMOTE_TIMEOUT)
    up.pushed = True


def _git(cwd: Path, *args: str, env: dict[str, str] | None = None, input: bytes | None = None,
         timeout: float | None = None) -> str:
    command = next((a for a in args if not a.startswith("-") and "=" not in a), args[0])
    try:
        r = subprocess.run(["git", *args], cwd=cwd, check=True, capture_output=True, env=env, input=input,
                           timeout=timeout)
    except FileNotFoundError as exc:
        raise ScaffoldError("git is not installed") from exc
    except subprocess.TimeoutExpired as exc:
        raise ScaffoldError(f"git {command} did not finish in {timeout:.0f}s") from exc
    except subprocess.CalledProcessError as exc:
        raise ScaffoldError(f"git {command} failed: {exc.stderr.decode(errors='replace').strip()}") from exc
    return r.stdout.decode(errors="replace")


def _git_maybe(cwd: Path, *args: str) -> str | None:
    """The output of a git query, or None when it fails."""
    try:
        return _git(cwd, *args).strip() or None
    except ScaffoldError:
        return None
