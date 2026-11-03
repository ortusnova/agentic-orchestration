"""Workbench configuration, read from the environment."""

from __future__ import annotations

import os
import re
import shlex
from dataclasses import dataclass, field
from pathlib import Path


DEFAULT_AGENT_MODEL = "anthropic/claude-opus-5-5"

SOURCE_URL = "https://github.com/ortusnova/agentic-orchestration"

PACKAGE = Path(__file__).resolve().parent


def bundled(in_package: str, in_source: str) -> Path:
    """A file the wheel ships inside the package; from a source checkout,
    the same file where it lives in the source tree."""
    path = PACKAGE / in_package
    return path if path.exists() else PACKAGE.parents[1] / in_source


class ConfigError(Exception):
    """The configuration is incomplete or invalid."""


# A pull request that is not merged yet is looked up again at least every ten
# minutes: the recheck interval, plus at most one poll tick.
MAX_DELIVERY_RECHECK = 540.0


def _recheck(value: str) -> float:
    if not value.strip():
        return 300.0
    try:
        seconds = float(value)
    except ValueError as exc:
        raise ConfigError(f"WORKBENCH_DELIVERY_RECHECK_SECONDS is not a number: {value!r}") from exc
    if not 1 <= seconds <= MAX_DELIVERY_RECHECK:
        raise ConfigError(f"WORKBENCH_DELIVERY_RECHECK_SECONDS must be between 1 and {MAX_DELIVERY_RECHECK:g}")
    return seconds


SORTIE_NAME = re.compile(r"[a-z0-9][a-z0-9-]*")


def _peers(env: dict[str, str]) -> tuple[tuple[str, str, str], ...]:
    """WORKBENCH_SORTIES: other Sorties, as ``name=url`` separated by commas,
    such as ``taichi=http://127.0.0.1:7781``. Each one's token is in
    WORKBENCH_SORTIE_TOKEN_<NAME>, or in the file WORKBENCH_SORTIE_TOKEN_<NAME>_FILE
    names; <NAME> is the name in capitals, with _ for -."""
    out: list[tuple[str, str, str]] = []
    own = env.get("WORKBENCH_SORTIE_NAME", "").strip() or "sortie"
    for item in (env.get("WORKBENCH_SORTIES") or "").split(","):
        item = item.strip()
        if not item:
            continue
        name, sep, url = item.partition("=")
        name, url = name.strip(), url.strip().rstrip("/")
        if not sep or not SORTIE_NAME.fullmatch(name) or not url.startswith(("http://", "https://")):
            raise ConfigError(f"WORKBENCH_SORTIES: {item!r} is not name=http(s)://host:port, with a lower-case name")
        if name == own or any(p[0] == name for p in out):
            raise ConfigError(f"WORKBENCH_SORTIES: the Sortie name {name!r} is used twice")
        var = "WORKBENCH_SORTIE_TOKEN_" + name.upper().replace("-", "_")
        token = env.get(var, "").strip()
        if not token and env.get(var + "_FILE", "").strip():
            try:
                token = Path(env[var + "_FILE"].strip()).expanduser().read_text().strip()
            except OSError as exc:
                raise ConfigError(f"{var}_FILE: {exc}") from exc
        out.append((name, url, token))
    return tuple(out)


@dataclass(frozen=True)
class Config:
    """Everything the backend needs. Tokens stay server-side: no response carries them."""

    # Where the registry database and the stores of created projects live.
    home: Path
    sortie_url: str
    sortie_token: str
    # Local mode only: the operator password's hash. Empty in OIDC mode.
    password_hash: str
    secret_key: str
    broker_command: list[str] = field(default_factory=lambda: ["acp-broker"])
    agent_command: list[str] = field(default_factory=lambda: ["opencode", "acp"])
    # Selected through the agent's ACP "model" config option; empty keeps the agent's default.
    agent_model: str = ""
    # A store to register as a project at startup, once, with its tracker project.
    store: Path | None = None
    huly_project: str = ""
    prompt_file: Path | None = None
    frontend_dir: Path | None = None
    # The parent of each project's session runtime directory.
    runtime_dir: Path | None = None
    session_max_age: int = 12 * 3600
    debounce_ms: int = 300
    poll_seconds: float = 5.0
    # A failed sync is retried after this many seconds, doubling up to the ceiling.
    sync_backoff_first: float = 1.0
    sync_backoff_max: float = 60.0
    cookie_secure: bool = False
    # Off, the workbench never writes to the tracker on its own: no automatic
    # sync, no pull-back of edited stories, no story action that calls Sortie.
    # For a second, read-only workbench beside the one that owns the project.
    sync_enabled: bool = True
    # GitHub, where the workbench looks up the pull requests a story names in
    # delivered_by. Without a token it still looks up public repositories.
    github_token: str = ""
    github_api_url: str = "https://api.github.com"
    # How often a pull request that is not merged yet is looked up again.
    delivery_recheck_seconds: float = 300.0
    # OpenID Connect. With no issuer, the workbench runs in local mode.
    oidc_issuer: str = ""
    oidc_client_id: str = ""
    oidc_client_secret: str = ""
    oidc_redirect_url: str = ""
    oidc_roles_claim: str = "groups"
    # Some providers (Dex, Okta) return the groups claim only for a "groups" scope.
    oidc_scopes: str = "openid profile email"
    oidc_planner_group: str = ""
    oidc_approver_group: str = ""
    # The name of the Sortie at sortie_url, as the workbench shows its host,
    # and the other Sorties whose workers it shows: (name, url, token).
    sortie_name: str = "sortie"
    peer_sorties: tuple[tuple[str, str, str], ...] = ()
    # Local mode on a non-loopback address, which `workbench serve` refuses otherwise.
    allow_local_mode: bool = False
    # Where the people who use this workbench can get its source, as the
    # AGPL requires. Whoever runs a modified workbench points it at theirs.
    source_url: str = SOURCE_URL

    @property
    def oidc(self) -> bool:
        return bool(self.oidc_issuer)

    @classmethod
    def from_env(cls, env: dict[str, str] | None = None) -> "Config":
        env = dict(os.environ if env is None else env)

        def need(key: str) -> str:
            value = env.get(key, "").strip()
            if not value:
                raise ConfigError(f"{key} is required")
            return value

        default_home = Path(env.get("XDG_DATA_HOME") or Path.home() / ".local" / "share") / "plan-workbench"
        home = Path(env.get("WORKBENCH_HOME") or default_home).expanduser().resolve()
        store = None
        huly_project = env.get("WORKBENCH_HULY_PROJECT", "").strip()
        if env.get("WORKBENCH_STORE", "").strip():
            store = Path(env["WORKBENCH_STORE"].strip()).expanduser().resolve()
            if not (store / "openspec").is_dir():
                raise ConfigError(f"WORKBENCH_STORE {store} has no openspec/ directory")
            if not huly_project:
                raise ConfigError("WORKBENCH_HULY_PROJECT is required with WORKBENCH_STORE: the Huly project the store syncs into")
        issuer = env.get("WORKBENCH_OIDC_ISSUER", "").strip().rstrip("/")
        secret = env.get("WORKBENCH_OIDC_CLIENT_SECRET", "").strip()
        if not secret and env.get("WORKBENCH_OIDC_CLIENT_SECRET_FILE", "").strip():
            try:
                secret = Path(env["WORKBENCH_OIDC_CLIENT_SECRET_FILE"].strip()).expanduser().read_text().strip()
            except OSError as exc:
                raise ConfigError(f"WORKBENCH_OIDC_CLIENT_SECRET_FILE: {exc}") from exc
        if issuer:
            for key in ("WORKBENCH_OIDC_CLIENT_ID", "WORKBENCH_OIDC_REDIRECT_URL", "WORKBENCH_OIDC_PLANNER_GROUP", "WORKBENCH_OIDC_APPROVER_GROUP"):
                need(key)
            if not secret:
                raise ConfigError("WORKBENCH_OIDC_CLIENT_SECRET (or WORKBENCH_OIDC_CLIENT_SECRET_FILE) is required with WORKBENCH_OIDC_ISSUER")
        password_hash = env.get("WORKBENCH_PASSWORD_HASH", "").strip()
        if not issuer and not password_hash:
            raise ConfigError("WORKBENCH_PASSWORD_HASH is required without WORKBENCH_OIDC_ISSUER (local mode)")
        prompt = env.get("WORKBENCH_PROMPT_FILE") or str(bundled("prompts/planning-session.md", "prompts/planning-session.md"))
        frontend = env.get("WORKBENCH_FRONTEND_DIR") or str(bundled("frontend", "frontend/dist"))
        return cls(
            home=home,
            store=store,
            huly_project=huly_project,
            sortie_url=need("WORKBENCH_SORTIE_URL").rstrip("/"),
            sortie_token=env.get("WORKBENCH_SORTIE_TOKEN", ""),
            sortie_name=env.get("WORKBENCH_SORTIE_NAME", "").strip() or "sortie",
            peer_sorties=_peers(env),
            password_hash=password_hash,
            secret_key=need("WORKBENCH_SECRET_KEY"),
            broker_command=shlex.split(env.get("WORKBENCH_BROKER_COMMAND", "acp-broker")),
            agent_command=shlex.split(env.get("WORKBENCH_AGENT_COMMAND", "opencode acp")),
            agent_model=env.get("WORKBENCH_AGENT_MODEL", DEFAULT_AGENT_MODEL).strip(),
            prompt_file=Path(prompt),
            frontend_dir=Path(frontend),
            runtime_dir=Path(env["WORKBENCH_RUNTIME_DIR"]).expanduser() if env.get("WORKBENCH_RUNTIME_DIR") else None,
            cookie_secure=env.get("WORKBENCH_COOKIE_SECURE", "") == "1",
            sync_enabled=env.get("WORKBENCH_SYNC", "1").strip() != "0",
            github_token=env.get("WORKBENCH_GITHUB_TOKEN", "").strip(),
            github_api_url=(env.get("WORKBENCH_GITHUB_API_URL", "").strip() or "https://api.github.com").rstrip("/"),
            delivery_recheck_seconds=_recheck(env.get("WORKBENCH_DELIVERY_RECHECK_SECONDS", "")),
            oidc_issuer=issuer,
            oidc_client_id=env.get("WORKBENCH_OIDC_CLIENT_ID", "").strip(),
            oidc_client_secret=secret,
            oidc_redirect_url=env.get("WORKBENCH_OIDC_REDIRECT_URL", "").strip(),
            oidc_roles_claim=env.get("WORKBENCH_OIDC_ROLES_CLAIM", "").strip() or "groups",
            oidc_scopes=env.get("WORKBENCH_OIDC_SCOPES", "").strip() or "openid profile email",
            oidc_planner_group=env.get("WORKBENCH_OIDC_PLANNER_GROUP", "").strip(),
            oidc_approver_group=env.get("WORKBENCH_OIDC_APPROVER_GROUP", "").strip(),
            allow_local_mode=env.get("WORKBENCH_ALLOW_LOCAL_MODE", "") == "1",
            source_url=env.get("WORKBENCH_SOURCE_URL", "").strip() or SOURCE_URL,
        )
