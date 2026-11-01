#!/usr/bin/env python3
"""Print the freshest live Claude subscription OAuth token on this host.

Sources, in order of preference when equally fresh:
  1. $STAGE0_HOME/claude-oauth-token: a long-lived `claude setup-token`
     token (no expiry known, so it always wins);
  2. opencode's active Anthropic credential in ~/.local/share/opencode/opencode.db,
     which opencode refreshes while the operator uses it;
  3. Claude Code's ~/.claude/.credentials.json.
Prints nothing and exits 1 when no source holds an unexpired token.
With --expiry, prints the chosen token's expiry (epoch ms, 0 if unknown) instead.
With --setup-token-only, only source 1 counts (the homelab profile reads
exactly that file, and falls back to no login).
"""
import json
import os
import sqlite3
import sys
import time

home = os.path.expanduser("~")
stage0 = os.environ.get("STAGE0_HOME", os.path.join(home, ".local/share/ortus-nova/stage0"))
now_ms = time.time() * 1000
found = []  # (expiry ms, token); expiry inf for a long-lived token

path = os.path.join(stage0, "claude-oauth-token")
if os.path.isfile(path) and os.path.getsize(path):
    found.append((float("inf"), open(path).read().strip()))

only_setup_token = "--setup-token-only" in sys.argv[1:]

db = os.path.join(home, ".local/share/opencode/opencode.db")
if not only_setup_token and os.path.isfile(db):
    try:
        c = sqlite3.connect(f"file:{db}?mode=ro", uri=True, timeout=5)
        for (value,) in c.execute("select value from credential where integration_id = 'anthropic' and active = 1"):
            v = json.loads(value)
            if v.get("type") == "oauth" and v.get("access"):
                found.append((float(v.get("expires") or 0), v["access"]))
    except (sqlite3.Error, ValueError):
        pass

creds = os.path.join(home, ".claude/.credentials.json")
if not only_setup_token and os.path.isfile(creds):
    try:
        o = json.load(open(creds))["claudeAiOauth"]
        found.append((float(o.get("expiresAt") or 0), o["accessToken"]))
    except (KeyError, ValueError):
        pass

live = [f for f in found if f[0] > now_ms + 5 * 60 * 1000]
if not live:
    sys.exit(1)
expiry, token = max(live, key=lambda f: f[0])
if "--expiry" in sys.argv[1:]:
    print(0 if expiry == float("inf") else int(expiry))
else:
    print(token)
