"""Who is signed in: the identity in a signed, HttpOnly session cookie, and its roles.

In OIDC mode the identity comes from the team's provider (``oidc.py``). In
local mode, for development, one password signs in a local identity with
both roles.
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import secrets
from dataclasses import dataclass

from itsdangerous import BadSignature, SignatureExpired, URLSafeTimedSerializer

COOKIE_NAME = "workbench_session"
LOGIN_COOKIE_NAME = "workbench_login"
LOGIN_MAX_AGE = 600

PLANNER = "planner"
APPROVER = "approver"
LOCAL_ISSUER = "local"
# A preview's review (workbench.preview): the control plane's egress gate
# gives each request of a review job to a preview the previews' review key
# and the job, in these headers, and drops any value the job sent.
REVIEW_ISSUER = "review"
REVIEW_KEY_HEADER = "X-Workbench-Review-Key"
REVIEW_JOB_HEADER = "X-Workbench-Review-Job"

_N, _R, _P = 2**14, 8, 1


def hash_password(password: str, *, salt: bytes | None = None) -> str:
    """Return "scrypt$n$r$p$salt$hash" for password."""
    salt = salt or secrets.token_bytes(16)
    digest = hashlib.scrypt(password.encode(), salt=salt, n=_N, r=_R, p=_P, dklen=32)
    b64 = lambda b: base64.urlsafe_b64encode(b).decode().rstrip("=")  # noqa: E731
    return f"scrypt${_N}${_R}${_P}${b64(salt)}${b64(digest)}"


def verify_password(password: str, stored: str) -> bool:
    try:
        scheme, n, r, p, salt, digest = stored.split("$")
        if scheme != "scrypt":
            return False
        unb64 = lambda s: base64.urlsafe_b64decode(s + "=" * (-len(s) % 4))  # noqa: E731
        got = hashlib.scrypt(password.encode(), salt=unb64(salt), n=int(n), r=int(r), p=int(p), dklen=32)
        return hmac.compare_digest(got, unb64(digest))
    except (ValueError, TypeError):
        return False


@dataclass(frozen=True)
class Identity:
    """The signed-in person. ``iss`` and ``sub`` are the stable key."""

    iss: str
    sub: str
    name: str
    email: str
    roles: tuple[str, ...]
    mode: str  # "oidc" or "local"

    @property
    def key(self) -> str:
        """The stable identity as one string: issuer and subject."""
        return f"{self.iss}|{self.sub}"

    def has(self, role: str) -> bool:
        return role in self.roles

    def public(self) -> dict:
        """What the browser may see about the person."""
        return {"name": self.name, "email": self.email, "roles": list(self.roles), "id": self.key}


def local_identity() -> Identity:
    """The one person of local mode. Its issuer is ``local``, so no record made
    in local mode can be mistaken for a provider identity."""
    return Identity(iss=LOCAL_ISSUER, sub="operator", name="Local operator", email="", roles=(PLANNER, APPROVER), mode="local")


def review_identity(key: str, header_key: str | None, job: str | None) -> Identity | None:
    """The review of a preview, signed in by the previews' review key that the
    egress gate sets on its requests: ``Review of <job>``, with both roles,
    so it can click every action. None without a key, or with another one."""
    if not key or not header_key or not hmac.compare_digest(key.encode(), header_key.encode()):
        return None
    job = (job or "").strip()[:64] or "review"
    return Identity(iss=REVIEW_ISSUER, sub=job, name=f"Review of {job}", email="", roles=(PLANNER, APPROVER), mode="local")


def roles_from_groups(groups: object, planner_group: str, approver_group: str) -> tuple[str, ...]:
    """Map the provider's group claim to roles. The approver group grants both
    roles; neither group grants none."""
    if isinstance(groups, str):
        groups = [groups]
    if not isinstance(groups, (list, tuple)):
        return ()
    names = {str(g) for g in groups}
    if approver_group and approver_group in names:
        return (PLANNER, APPROVER)
    if planner_group and planner_group in names:
        return (PLANNER,)
    return ()


class Sessions:
    """Issues and reads signed session cookies that carry the identity."""

    def __init__(self, secret_key: str, max_age: int):
        self._serializer = URLSafeTimedSerializer(secret_key, salt="workbench-session")
        self._login = URLSafeTimedSerializer(secret_key, salt="workbench-login")
        self.max_age = max_age

    def issue(self, identity: Identity) -> str:
        return self._serializer.dumps({
            "iss": identity.iss, "sub": identity.sub, "name": identity.name, "email": identity.email,
            "roles": list(identity.roles), "mode": identity.mode, "nonce": secrets.token_hex(8),
        })

    def identity(self, cookie: str | None) -> Identity | None:
        """The identity of a valid cookie. A cookie from before identities, or
        one that is forged or expired, reads as nobody."""
        if not cookie:
            return None
        try:
            data = self._serializer.loads(cookie, max_age=self.max_age)
        except (BadSignature, SignatureExpired):
            return None
        if not isinstance(data, dict) or not data.get("iss") or not data.get("sub"):
            return None
        roles = tuple(r for r in data.get("roles", []) if r in (PLANNER, APPROVER))
        if not roles:
            return None
        return Identity(iss=data["iss"], sub=data["sub"], name=str(data.get("name", "")), email=str(data.get("email", "")),
                        roles=roles, mode=str(data.get("mode", "oidc")))

    def valid(self, cookie: str | None) -> bool:
        return self.identity(cookie) is not None

    def issue_login(self, attempt: dict) -> str:
        """A sign-in attempt in progress: state, nonce, PKCE verifier, return path."""
        return self._login.dumps(attempt)

    def login(self, cookie: str | None) -> dict | None:
        if not cookie:
            return None
        try:
            data = self._login.loads(cookie, max_age=LOGIN_MAX_AGE)
        except (BadSignature, SignatureExpired):
            return None
        return data if isinstance(data, dict) else None


def safe_next(path: str | None) -> str:
    """A return path on the workbench itself, or the home page."""
    if not path or not path.startswith("/") or path.startswith("//") or "\\" in path:
        return "/"
    return path
