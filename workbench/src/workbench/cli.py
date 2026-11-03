"""workbench command line: serve the app, or hash the operator password."""

from __future__ import annotations

import argparse
import getpass
import sys


def is_loopback(host: str) -> bool:
    import ipaddress

    if host == "localhost":
        return True
    try:
        return ipaddress.ip_address(host.strip("[]")).is_loopback
    except ValueError:
        return False


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="workbench")
    sub = parser.add_subparsers(dest="command", required=True)
    serve = sub.add_parser("serve", help="run the workbench server")
    serve.add_argument("--host", default="127.0.0.1")
    serve.add_argument("--port", type=int, default=8790)
    serve.add_argument("--reload", action="store_true", help="restart when the backend's Python code changes (development)")
    sub.add_parser("hash-password", help="read a password and print its hash for WORKBENCH_PASSWORD_HASH")
    pv = sub.add_parser("preview-sync", help="deploy, follow and remove the previews of the review: ui stories' pull requests, once")
    pv.add_argument("--repo", required=True, help="owner/repo of the pull requests to preview")
    pv.add_argument("--record", required=True, help="the previews' record, which the live workbench reads as WORKBENCH_PREVIEWS")
    pv.add_argument("--deploy", required=True, help="the command that runs `up IDENTIFIER BRANCH SHA PORT ADDRESS` and `down IDENTIFIER`")
    pv.add_argument("--live-url", default="", help="the live workbench's address; each preview gets a name beside it")
    pv.add_argument("--first-port", type=int, default=8800)
    pv.add_argument("--handoff-state", default="In Review", help="the state a story waits in once its review passed")
    pv.add_argument("--branch-prefix", default="sortie/")
    args = parser.parse_args(argv)

    if args.command == "preview-sync":
        return preview_sync(args)

    if args.command == "hash-password":
        from .auth import hash_password

        password = getpass.getpass("operator password: ") if sys.stdin.isatty() else sys.stdin.readline().rstrip("\n")
        if not password:
            print("workbench: empty password", file=sys.stderr)
            return 1
        print(hash_password(password))
        return 0

    import logging

    import uvicorn

    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(name)s: %(message)s")
    logging.getLogger("httpx").setLevel(logging.WARNING)

    from pathlib import Path

    from .config import Config, ConfigError

    try:
        config = Config.from_env()  # fail here, once, rather than in every reloaded worker
    except ConfigError as exc:
        print(f"workbench: {exc}", file=sys.stderr)
        return 2
    if not config.oidc and not is_loopback(args.host) and not config.allow_local_mode:
        print(f"workbench: local mode (one shared password) on {args.host}, which is not a loopback address: "
              "set WORKBENCH_OIDC_ISSUER, or WORKBENCH_ALLOW_LOCAL_MODE=1 to accept it", file=sys.stderr)
        return 2
    src = Path(__file__).resolve().parents[1]
    uvicorn.run(
        "workbench.app:create_app_from_env",
        factory=True,
        host=args.host,
        port=args.port,
        log_level="info",
        reload=args.reload,
        # Event streams never end by themselves, and uvicorn otherwise waits
        # for every open response before it stops: a --reload would leave the
        # old worker waiting forever, accepting nothing.
        timeout_graceful_shutdown=3,
        reload_dirs=[str(src / "workbench"), str(src / "planlib")] if args.reload else None,
    )
    return 0


def preview_sync(args) -> int:
    """One pass of the previews. Sortie and GitHub are read with the
    workbench's own settings: WORKBENCH_SORTIE_URL, WORKBENCH_SORTIE_TOKEN,
    WORKBENCH_HULY_PROJECT, WORKBENCH_GITHUB_TOKEN, WORKBENCH_GITHUB_API_URL."""
    import asyncio
    import logging
    import os
    import shlex
    from pathlib import Path

    from .delivery import GitHubClient
    from .preview import CommandDeployer, Settings, sync_once
    from .sortie import SortieClient

    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(name)s: %(message)s")
    logging.getLogger("httpx").setLevel(logging.WARNING)
    env = os.environ
    url, project = env.get("WORKBENCH_SORTIE_URL", "").strip(), env.get("WORKBENCH_HULY_PROJECT", "").strip()
    if not url or not project or "/" not in args.repo:
        print("workbench: preview-sync needs WORKBENCH_SORTIE_URL, WORKBENCH_HULY_PROJECT and --repo owner/repo",
              file=sys.stderr)
        return 2
    settings = Settings(repo=args.repo, record=Path(args.record).expanduser(), live_url=args.live_url,
                        first_port=args.first_port, handoff_state=args.handoff_state, branch_prefix=args.branch_prefix)

    async def run() -> int:
        sortie = SortieClient(url.rstrip("/"), env.get("WORKBENCH_SORTIE_TOKEN", ""))
        github = GitHubClient((env.get("WORKBENCH_GITHUB_API_URL", "").strip() or "https://api.github.com").rstrip("/"),
                              env.get("WORKBENCH_GITHUB_TOKEN", "").strip())
        try:
            return await sync_once(settings, sortie.bind(project), github, CommandDeployer(shlex.split(args.deploy)))
        finally:
            await sortie.aclose()
            await github.aclose()

    return asyncio.run(run())


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())
