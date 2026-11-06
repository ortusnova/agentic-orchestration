#!/usr/bin/env python3
"""Delete the traces older than DAYS days through Langfuse's public API.

    retention.py DAYS

telemetry.sh retention runs it daily. Langfuse's public API selects by age:
GET /api/public/v2/observations takes toStartTime, so every observation
that started before the cutoff names its trace; DELETE /api/public/traces
then deletes those traces, 1,000 at a time, with their observations and
scores (Langfuse's worker deletes them from ClickHouse and the bucket within
minutes). No ClickHouse TTL is needed, and Langfuse's own deletion path keeps
ClickHouse, the bucket and its own records consistent.

Environment: LANGFUSE_HOST (http://127.0.0.1:3000), LANGFUSE_PUBLIC_KEY and
LANGFUSE_SECRET_KEY_FILE (the project's key pair). Standard library only.
"""

import base64
import datetime
import json
import os
import sys
import urllib.parse
import urllib.request

BATCH = 1000
PAGE = 1000


def request(method, url, auth, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("Authorization", "Basic " + auth)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    with urllib.request.urlopen(req, timeout=120) as resp:
        raw = resp.read()
    return json.loads(raw) if raw else None


def old_trace_ids(host, auth, cutoff):
    """Each trace with an observation that started before cutoff, once."""
    seen = set()
    params = {
        "fromStartTime": "1970-01-01T00:00:00Z",
        "toStartTime": cutoff,
        "fields": "core",
        "limit": PAGE,
    }
    while True:
        url = host + "/api/public/v2/observations?" + urllib.parse.urlencode(params)
        page = request("GET", url, auth)
        for obs in page.get("data") or []:
            tid = obs.get("traceId")
            if tid and tid not in seen:
                seen.add(tid)
                yield tid
        cursor = (page.get("meta") or {}).get("cursor")
        if not cursor:
            return
        params["cursor"] = cursor


def main(argv):
    if len(argv) != 2 or not argv[1].isdigit() or int(argv[1]) < 1:
        print("usage: retention.py DAYS", file=sys.stderr)
        return 2
    days = int(argv[1])
    host = os.environ.get("LANGFUSE_HOST", "http://127.0.0.1:3000").rstrip("/")
    with open(os.environ["LANGFUSE_SECRET_KEY_FILE"]) as f:
        secret = f.read().strip()
    auth = base64.b64encode(f"{os.environ['LANGFUSE_PUBLIC_KEY']}:{secret}".encode()).decode()
    now = datetime.datetime.now(datetime.timezone.utc)
    cutoff = (now - datetime.timedelta(days=days)).strftime("%Y-%m-%dT%H:%M:%S.000Z")

    # Read every page before the first deletion, so that no page moves
    # under the reading.
    ids = list(old_trace_ids(host, auth, cutoff))
    for i in range(0, len(ids), BATCH):
        request("DELETE", host + "/api/public/traces", auth, {"traceIds": ids[i : i + BATCH]})
    print(f"retention: {len(ids)} trace(s) started before {cutoff} ({days} days) submitted for deletion")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
