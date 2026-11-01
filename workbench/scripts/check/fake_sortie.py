"""A fake Sortie and fake worker brokers, for checking the workbench's UI by hand.

Usage: fake_sortie.py [--port 7690] [--token TOKEN] [--project HLAB]

Serves the Sortie routes the workbench reads (plan graph, state, issue,
broker) for the plan that seed_store.py writes: story billing/b1 runs as
<PROJECT>-1, and its broker (served here under /r<N>) replays a few events,
including a permission request. Commands sent to a broker are kept.

Drive it with:
  POST /admin/next      the story's next run (a retry or the next stage)
  POST /admin/end       Sortie runs nothing more for the story
  POST /admin/park      billing/b2 (<PROJECT>-2) stops with a question; an
                        answer through POST /api/v1/<PROJECT>-2/answer releases it
  GET  /admin/commands  what the brokers received
"""

import argparse
import asyncio
import json
import time

import uvicorn
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse, StreamingResponse

app = FastAPI()
# The plan seed_store.py writes; the first story runs.
PLAN_KEYS = ["billing/b1", "billing/b2", "accounts/a3"]
TOKEN = "check-sortie-token"
PROJECT = "HLAB"
PORT = 7690
state = {"run": 1, "ended": False, "commands": [], "park": None, "answers": []}
QUESTION = ("billing/b2 says to charge in the customer's currency, but accounts/a3 stores every balance in EUR.\n\n"
            "Should b2 convert at charge time, or should a3 store the currency too?")


def ident(i: int = 1) -> str:
    return f"{PROJECT}-{i}"


def running():
    if state["ended"]:
        return None
    n = state["run"]
    return {"issue_identifier": ident(), "session_id": f"s{n}", "rule_name": "implement" if n == 1 else "review",
            "turn_count": 4, "last_message": "writing the charge handler", "held": False, "holder": "",
            "broker": {"http_url": f"http://127.0.0.1:{PORT}/r{n}"}}


def node(i, key, rt=None):
    return {"id": f"id-{i}", "identifier": ident(i), "plan_key": key, "kind": "story" if "/" in key else "epic",
            "title": key, "state": "In Progress" if rt else "Todo", "terminal": False, "url": None, "parent_id": None, "runtime": rt}


@app.get("/api/v1/plan/graph")
def graph():
    r = running()
    rt = {"status": "running", "turn_count": 4, "last_message": "writing the charge handler", "attempt": None} if r else None
    return {"project": PROJECT, "dispatched": True, "generated_at": "now", "capabilities": [], "edges": [],
            "plan_states": None, "nodes": [node(i + 1, k, rt if i == 0 else None) for i, k in enumerate(PLAN_KEYS)]}


@app.get("/api/v1/state")
def st():
    r = running()
    return {"running": [r] if r else [], "retrying": [], "budget_exhausted": [],
            "parked": [{"issue_identifier": ident(2), "reason": "agent_blocked", **state["park"]}] if state["park"] else []}


@app.get("/api/v1/{key}/broker")
def broker(key: str, request: Request):
    assert request.headers.get("authorization") == f"Bearer {TOKEN}"
    r = running()
    if not r or key != ident():
        return JSONResponse({"error": {"code": "broker_not_found", "message": key}}, status_code=404)
    return {"http_url": r["broker"]["http_url"], "viewer_token": f"viewer-{state['run']}"}


@app.post("/api/v1/{key}/answer")
async def answer(key: str, request: Request):
    assert request.headers.get("authorization") == f"Bearer {TOKEN}"
    if not state["park"] or key != ident(2):
        return JSONResponse({"error": {"code": "not_parked", "message": f"{key} is not parked"}}, status_code=409)
    state["answers"].append(await request.json())
    state["park"] = None
    return {"issue_identifier": key, "released": True}


@app.get("/api/v1/{key}")
def issue(key: str):
    if key == ident(2) and state["park"]:
        return {"issue_identifier": key, "running": None, "retry": None, "parked": state["park"]}
    r = running()
    if not r or key != ident():
        return JSONResponse({"error": {"code": "issue_not_found", "message": key}}, status_code=404)
    return {"issue_identifier": key, "running": r, "retry": None}


# Admin, for the check: the next run, then the end.
@app.post("/admin/next")
def nxt():
    state["run"] += 1
    return {"run": state["run"]}


@app.post("/admin/end")
def end():
    state["ended"] = True
    return {"ended": True}


@app.get("/admin/commands")
def cmds():
    return state["commands"]


@app.post("/admin/park")
def park():
    state["park"] = {"statement": QUESTION, "parked_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    return {"parked": ident(2), "answers": state["answers"]}


def ev(seq, **kw):
    return {"seq": seq, "time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), **kw}


def chunk(seq, text):
    return ev(seq, type="update", update={"update": {"sessionUpdate": "agent_message_chunk", "content": {"text": text}}})


@app.get("/r{n}/events")
async def events(n: int, request: Request):
    assert request.headers.get("authorization") == f"Bearer viewer-{n}", "viewer token"
    after = int(request.headers.get("last-event-id") or 0)

    async def gen():
        replay = [ev(1, type="state", floor="driver", driverTurn="active"),
                  chunk(2, f"Run {n}: reading billing/b1 and its acceptance criteria. "),
                  ev(3, type="update", update={"update": {"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "read src/billing/charge.py", "status": "completed"}}),
                  ev(4, type="permission", id="p1", request={"toolCall": {"title": "run pytest tests/billing"}, "options": [
                      {"optionId": "allow", "name": "Allow", "kind": "allow_once"}, {"optionId": "deny", "name": "Deny", "kind": "reject_once"}]})]
        for e in replay:
            if e["seq"] > after:
                yield f"id: {e['seq']}\ndata: {json.dumps(e)}\n\n"
        seq = 4
        while state["run"] == n and not state["ended"]:
            await asyncio.sleep(1)
            for c in state["commands"]:
                if c.get("_seen") is None and c["run"] == n:
                    c["_seen"] = True
                    seq += 1
                    e = ev(seq, type="state", floor="human" if c["op"] == "interrupt" else "driver", driverTurn="none")
                    yield f"id: {seq}\ndata: {json.dumps(e)}\n\n"
            yield ": keepalive\n\n"

    return StreamingResponse(gen(), media_type="text/event-stream")


@app.post("/r{n}/commands")
async def commands(n: int, request: Request):
    assert request.headers.get("authorization") == f"Bearer viewer-{n}"
    body = await request.json()
    state["commands"].append({**body, "run": n})
    return {"ok": True}


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int, default=7690)
    ap.add_argument("--token", default=TOKEN)
    ap.add_argument("--project", default=PROJECT)
    args = ap.parse_args()
    TOKEN, PROJECT, PORT = args.token, args.project, args.port
    uvicorn.run(app, host="127.0.0.1", port=args.port, log_level="warning")
