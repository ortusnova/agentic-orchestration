"""Sign-in routes and the identity and role checks every other route uses."""

from __future__ import annotations

import secrets
from typing import Callable

from fastapi import APIRouter, Depends, HTTPException, Request, Response
from fastapi.responses import RedirectResponse
from pydantic import BaseModel

from .auth import (
    APPROVER, COOKIE_NAME, LOGIN_COOKIE_NAME, LOGIN_MAX_AGE, PLANNER, REVIEW_JOB_HEADER, REVIEW_KEY_HEADER, Identity,
    Sessions, local_identity, review_identity, roles_from_groups, safe_next, verify_password,
)
from .config import Config
from .oidc import OIDCClient, OIDCError, pkce_pair


class LoginBody(BaseModel):
    password: str


def make_checks(sessions: Sessions, review_key: str = "") -> tuple[Callable, Callable[[str], Callable]]:
    """``current_identity``, a dependency that returns the signed-in person or
    answers 401, and ``require_role(role)``, one that also answers 403. With
    ``review_key``, a preview's, a request that carries it in
    ``REVIEW_KEY_HEADER`` is its review's (``review_identity``)."""

    def current_identity(request: Request) -> Identity:
        identity = sessions.identity(request.cookies.get(COOKIE_NAME))
        if identity is None and review_key:
            identity = review_identity(review_key, request.headers.get(REVIEW_KEY_HEADER),
                                       request.headers.get(REVIEW_JOB_HEADER))
        if identity is None:
            raise HTTPException(status_code=401, detail="sign in first")
        return identity

    def require_role(role: str) -> Callable:
        def check(identity: Identity = Depends(current_identity)) -> Identity:
            if not identity.has(role):
                raise HTTPException(status_code=403, detail={"code": "role_required", "message": f"this needs the {role} role"})
            return identity

        check.required_role = role  # read by the route-check test
        return check

    return current_identity, require_role


def auth_router(config: Config, sessions: Sessions, oidc: OIDCClient | None, current_identity: Callable) -> APIRouter:
    router = APIRouter(prefix="/api")

    def set_session(response: Response, identity: Identity) -> None:
        response.set_cookie(
            COOKIE_NAME, sessions.issue(identity), max_age=config.session_max_age,
            httponly=True, samesite="strict", secure=config.cookie_secure, path="/",
        )

    @router.get("/auth/mode")
    def mode() -> dict:
        """Public: how a person signs in here."""
        return {"mode": "oidc" if oidc else "local"}

    @router.get("/about")
    def about() -> dict:
        """Public: the license, and where to get the source, which the AGPL
        requires offering to everyone who uses the workbench."""
        return {"name": "plan-workbench", "license": "AGPL-3.0-or-later", "source_url": config.source_url}

    @router.post("/login")
    def login(body: LoginBody, response: Response) -> dict:
        if oidc is not None:
            raise HTTPException(status_code=404, detail="sign in through the identity provider")
        if not verify_password(body.password, config.password_hash):
            raise HTTPException(status_code=401, detail="wrong password")
        set_session(response, local_identity())
        return {"signed_in": True}

    @router.get("/auth/login")
    async def oidc_login(next: str = "/") -> Response:
        if oidc is None:
            raise HTTPException(status_code=404, detail="the workbench runs in local mode")
        state, nonce = secrets.token_urlsafe(24), secrets.token_urlsafe(24)
        verifier, challenge = pkce_pair()
        try:
            url = await oidc.authorize_url(state, nonce, challenge)
        except Exception as exc:  # the provider is unreachable or misconfigured
            raise HTTPException(status_code=502, detail={"code": "provider_unavailable", "message": f"the identity provider is unavailable: {exc}"}) from exc
        r = RedirectResponse(url, status_code=302)
        # Lax: the provider's redirect back is a cross-site top-level GET,
        # which a Strict cookie would not travel with.
        r.set_cookie(LOGIN_COOKIE_NAME, sessions.issue_login({"state": state, "nonce": nonce, "verifier": verifier, "next": safe_next(next)}),
                     max_age=LOGIN_MAX_AGE, httponly=True, samesite="lax", secure=config.cookie_secure, path="/api/auth")
        return r

    @router.get("/auth/callback")
    async def callback(request: Request, code: str = "", state: str = "", error: str = "") -> Response:
        if oidc is None:
            raise HTTPException(status_code=404, detail="the workbench runs in local mode")
        attempt = sessions.login(request.cookies.get(LOGIN_COOKIE_NAME))

        def refuse(reason: str) -> Response:
            # A refused sign-in also ends any earlier session in this
            # browser, so the refusal never leaves someone else signed in.
            r = RedirectResponse("/#/signin?error=" + reason, status_code=302)
            r.delete_cookie(LOGIN_COOKIE_NAME, path="/api/auth")
            r.delete_cookie(COOKIE_NAME, path="/")
            return r

        if error:
            return refuse("provider_refused")
        if attempt is None:
            return refuse("expired")
        if not state or not secrets.compare_digest(state, attempt.get("state", "")):
            return refuse("unverified")
        try:
            claims = await oidc.exchange(code, attempt["verifier"], attempt["nonce"])
        except OIDCError:
            return refuse("unverified")
        roles = roles_from_groups(claims.get(config.oidc_roles_claim), config.oidc_planner_group, config.oidc_approver_group)
        if not roles:
            return refuse("no_role")
        identity = Identity(iss=str(claims["iss"]).rstrip("/"), sub=str(claims["sub"]),
                            name=str(claims.get("name") or claims.get("preferred_username") or claims.get("email") or claims["sub"]),
                            email=str(claims.get("email", "")), roles=roles, mode="oidc")
        r = RedirectResponse(safe_next(attempt.get("next")), status_code=302)
        set_session(r, identity)
        r.delete_cookie(LOGIN_COOKIE_NAME, path="/api/auth")
        return r

    @router.post("/logout")
    async def logout(response: Response) -> dict:
        response.delete_cookie(COOKIE_NAME, path="/")
        end = None
        if oidc is not None:
            try:
                end = await oidc.end_session_url()
            except Exception:  # signing out locally never depends on the provider
                end = None
        return {"signed_in": False, "end_session_url": end}

    @router.get("/me")
    def me(identity: Identity = Depends(current_identity)) -> dict:
        return {"signed_in": True, "mode": identity.mode, "identity": identity.public()}

    return router


__all__ = ["APPROVER", "PLANNER", "auth_router", "make_checks"]
