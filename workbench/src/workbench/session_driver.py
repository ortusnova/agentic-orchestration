"""The ACP client of one planning session, run as its own detached process.

The broker ends its session when its driver closes the broker's standard
input. So the driver must outlive the workbench backend: a reload, a crash,
or Ctrl-C on the backend must not end the conversation. The backend starts
this module with ``start_new_session=True`` and talks to the session only
through the broker's HTTP viewer transport.

The driver starts the broker, opens one ACP session in the spec store,
selects the model and thinking level through the session's ACP config
options (and ends the session if the configured model is not offered),
sends the planning prompt with the person's first message as the first turn, and then answers what reaches
the driver: a permission request no viewer answered is cancelled, never
granted on a person's behalf. It records its state in ``state.json`` and
the choosable config options in ``config.json`` in the runtime directory,
takes config changes from the backend on ``ctl.sock`` there (ACP lets only
the client set them, and the broker relays no such request from viewers),
and exits when the broker exits or on SIGTERM.

Run: ``python -m workbench.session_driver <runtime-dir>``; the directory
holds ``spec.json``, written by the backend.
"""

from __future__ import annotations

import asyncio
import json
import os
import secrets
import signal
import socket
import sys
import time
from pathlib import Path
from typing import Any, AsyncIterator

ACP_PROTOCOL_VERSION = 1
REGISTRY_TIMEOUT = 15.0


def write_json(path: Path, data: dict[str, Any]) -> None:
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(data))
    os.replace(tmp, path)


class ConfigError(Exception):
    """A session config option could not be set."""


# The ACP config option categories a person may choose in the workbench.
MODEL = "model"
THOUGHT_LEVEL = "thought_level"
CATEGORIES = (MODEL, THOUGHT_LEVEL)
CONTROL_SOCKET = "ctl.sock"


def option_for(options: list[dict[str, Any]], category: str) -> dict[str, Any] | None:
    """The session config option of one ACP category, if the agent offers it."""
    return next((o for o in options if o.get("category") == category), None)


def select_values(option: dict[str, Any]) -> list[dict[str, Any]]:
    """The values a select option offers, flattened from groups when it has them."""
    values: list[dict[str, Any]] = []
    for entry in option.get("options") or []:
        values.extend(entry["options"] if "options" in entry else [entry])
    return values


def public_options(options: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """The choosable options as the browser sees them: one per category, values flattened."""
    out = []
    for category in CATEGORIES:
        opt = option_for(options, category)
        if opt is not None:
            out.append({
                "id": opt["id"], "category": category, "name": opt.get("name") or opt["id"],
                "currentValue": opt.get("currentValue"),
                "options": [{"value": v.get("value"), "name": v.get("name") or v.get("value")} for v in select_values(opt)],
            })
    return out


class Driver:
    def __init__(self, runtime: Path):
        self.runtime = runtime
        self.spec = json.loads((runtime / "spec.json").read_text())
        self.key: str = self.spec["key"]
        self.state: dict[str, Any] = {
            "key": self.key, "store": self.spec["store"], "driver_pid": os.getpid(), "broker_pid": None,
            "http_url": None, "token_file": None, "acp_session_id": None,
            # Drivers from before ctl.sock existed write no such flag: their sessions cannot change model.
            "can_configure": True,
            "model": None, "thought_level": None, "started_at": time.time(), "ended_at": None, "error": None,
        }
        self.stop = asyncio.Event()
        self.ready = asyncio.Event()  # the session is open and its first choices applied
        self.config_lock = asyncio.Lock()
        self.session_id: str | None = None
        self.options: list[dict[str, Any]] = []
        self.proc: asyncio.subprocess.Process | None = None
        self.next_id = 0
        self.waiting: dict[int, asyncio.Future] = {}

    def save(self, **changes: Any) -> None:
        self.state.update(changes)
        write_json(self.runtime / "state.json", self.state)

    async def run(self) -> int:
        loop = asyncio.get_running_loop()
        for sig in (signal.SIGTERM, signal.SIGINT):
            loop.add_signal_handler(sig, self.stop.set)
        try:
            await self.start_broker()
            control = await self.serve_control()
            reader = asyncio.create_task(self.read())
            setup = asyncio.create_task(self.open_session())
            stopper = asyncio.create_task(self.stop.wait())
            await asyncio.wait({reader, stopper}, return_when=asyncio.FIRST_COMPLETED)
            if reader.done() and not reader.cancelled() and reader.exception() is not None:
                exc = reader.exception()
                self.save(error=f"lost the agent's output: {type(exc).__name__}: {exc}")
            setup.cancel()
            control.close()
            (self.runtime / CONTROL_SOCKET).unlink(missing_ok=True)
        except Exception as exc:  # recorded for the backend to show
            self.save(error=str(exc))
        finally:
            await self.stop_broker()
            self.save(ended_at=time.time())
        return 0

    async def start_broker(self) -> None:
        token = secrets.token_urlsafe(32)
        token_file = self.runtime / f"{self.key}.token"
        token_file.write_text(token)
        os.chmod(token_file, 0o600)
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        registry = self.runtime / "registry"
        argv = [
            *self.spec["broker_command"],
            "-key", self.key,
            "-registry-dir", str(registry),
            "-http", f"127.0.0.1:{port}",
            "-viewer-token-file", str(token_file),
            "-handback-grace", "24h",
            "--", *self.spec["agent_command"],
        ]
        # The broker keeps its own copy of the stderr file; ours closes once it has started.
        with open(self.runtime / f"{self.key}.stderr", "wb") as stderr:
            self.proc = await asyncio.create_subprocess_exec(
                *argv, cwd=self.spec["store"],
                stdin=asyncio.subprocess.PIPE, stdout=asyncio.subprocess.PIPE, stderr=stderr,
            )
        self.save(broker_pid=self.proc.pid, token_file=str(token_file))

        deadline = time.monotonic() + REGISTRY_TIMEOUT
        while time.monotonic() < deadline:
            if self.proc.returncode is not None:
                raise RuntimeError(f"acp-broker exited with code {self.proc.returncode}")
            for rec_path in registry.glob(f"{self.key}.*.json"):
                try:
                    rec = json.loads(rec_path.read_text())
                except (OSError, ValueError):
                    continue
                if rec.get("pid") == self.proc.pid and rec.get("http_url"):
                    self.save(http_url=rec["http_url"].rstrip("/"))
                    return
            await asyncio.sleep(0.05)
        raise RuntimeError("acp-broker did not register in time")

    async def stop_broker(self) -> None:
        proc = self.proc
        if proc is None or proc.returncode is not None:
            return
        if proc.stdin and not proc.stdin.is_closing():
            proc.stdin.close()
        proc.terminate()
        try:
            await asyncio.wait_for(proc.wait(), timeout=10)
        except TimeoutError:
            proc.kill()
            await proc.wait()

    async def open_session(self) -> None:
        try:
            await self._open_session()
        except Exception as exc:  # recorded for the backend to show; the session stays up
            self.save(error=str(exc))

    async def _open_session(self) -> None:
        await self.call("initialize", {
            "protocolVersion": ACP_PROTOCOL_VERSION,
            "clientCapabilities": {"fs": {"readTextFile": False, "writeTextFile": False}, "terminal": False},
            "clientInfo": {"name": "plan-workbench", "version": "0.1.0"},
        })
        new = await self.call("session/new", {"cwd": self.spec["store"], "mcpServers": []})
        self.session_id = new.get("sessionId")
        self.save(acp_session_id=self.session_id)
        self.note_options(new.get("configOptions") or [])
        try:
            await self.apply_choices()
        except ConfigError as exc:
            # Planning on another model than the operator chose would go
            # unnoticed, so the session ends and says why.
            self.save(error=str(exc))
            self.stop.set()
            return
        self.ready.set()
        self.note_options(self.options)  # now marked ready
        # The first turn: the planning instructions, then the person's first message.
        blocks = [{"type": "text", "text": t} for t in (self.spec.get("prompt"), self.spec.get("message")) if t]
        if blocks:
            await self.call("session/prompt", {"sessionId": self.session_id, "prompt": blocks})

    async def apply_choices(self) -> None:
        """Select the model and thinking level before the first turn.

        The person's last choice wins when the agent still offers it; else
        the operator's configured model, which must be offered. A thinking
        level is set only when the person chose one: otherwise the model's
        own default stays.
        """
        preferred = self.spec.get("preferred") or {}
        configured = self.spec.get("model") or ""
        model_opt = option_for(self.options, MODEL)
        offered = {v.get("value") for v in select_values(model_opt)} if model_opt else set()
        model = preferred.get(MODEL) if preferred.get(MODEL) in offered else configured
        if model:
            await self.set_option(MODEL, model)
        level = preferred.get(THOUGHT_LEVEL)
        level_opt = option_for(self.options, THOUGHT_LEVEL)
        if level and level_opt and level in {v.get("value") for v in select_values(level_opt)}:
            await self.set_option(THOUGHT_LEVEL, level)

    def note_options(self, options: list[dict[str, Any]]) -> None:
        """Keep the agent's latest config options; the backend reads them from the runtime directory."""
        self.options = options
        public = public_options(options)
        write_json(self.runtime / "config.json", {"options": public, "ready": self.ready.is_set()})
        if not self.ready.is_set():
            # While the choices are applied the agent passes through a new
            # model's default thinking level: the session shows neither yet.
            return
        current = {o["category"]: o["currentValue"] for o in public}
        self.save(model=current.get(MODEL), thought_level=current.get(THOUGHT_LEVEL))

    async def set_option(self, category: str, value: str) -> None:
        """Set one config option through ACP and confirm the agent took it."""
        async with self.config_lock:
            option = option_for(self.options, category)
            if option is None:
                raise ConfigError(f"the agent offers no {category} choice, so {value} cannot be selected")
            if option.get("currentValue") == value:
                return
            if value not in {v.get("value") for v in select_values(option)}:
                raise ConfigError(f"the agent does not offer the {category.replace('_', ' ')} {value}")
            try:
                result = await self.call("session/set_config_option",
                                         {"sessionId": self.session_id, "configId": option["id"], "value": value})
            except RuntimeError as exc:
                raise ConfigError(f"the agent did not select {value}: {exc}") from exc
            options = result.get("configOptions")
            if options is not None:
                self.note_options(options)
            current = (option_for(self.options, category) or {}).get("currentValue")
            if current != value:
                raise ConfigError(f"the agent reports {current}, not {value}")

    async def serve_control(self) -> asyncio.base_events.Server:
        """A socket in the private runtime directory on which the backend asks
        for config changes: one JSON line in, one JSON line out."""
        async def handle(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
            try:
                req = json.loads(await reader.readline())
                if req.get("category") not in CATEGORIES or not isinstance(req.get("value"), str):
                    raise ConfigError("category must be model or thought_level, with a string value")
                if not self.ready.is_set():
                    raise ConfigError("the session is still starting")
                await self.set_option(req["category"], req["value"])
                reply: dict[str, Any] = {"ok": True, "options": public_options(self.options), "ready": True}
            except (ConfigError, ValueError, AttributeError) as exc:
                reply = {"ok": False, "error": str(exc)}
            writer.write((json.dumps(reply) + "\n").encode())
            await writer.drain()
            writer.close()

        path = self.runtime / CONTROL_SOCKET
        path.unlink(missing_ok=True)
        server = await asyncio.start_unix_server(handle, path=str(path))
        os.chmod(path, 0o600)
        return server

    async def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        self.next_id += 1
        msg_id = self.next_id
        fut: asyncio.Future = asyncio.get_running_loop().create_future()
        self.waiting[msg_id] = fut
        await self.send({"jsonrpc": "2.0", "id": msg_id, "method": method, "params": params})
        reply = await fut
        if "error" in reply:
            raise RuntimeError(f"{method}: {reply['error'].get('message')}")
        return reply.get("result") or {}

    async def send(self, msg: dict[str, Any]) -> None:
        assert self.proc and self.proc.stdin
        self.proc.stdin.write((json.dumps(msg) + "\n").encode())
        await self.proc.stdin.drain()

    async def read(self) -> None:
        assert self.proc and self.proc.stdout
        async for line in read_lines(self.proc.stdout):
            try:
                msg = json.loads(line)
            except ValueError:
                continue
            if "method" not in msg and msg.get("id") in self.waiting:
                self.waiting.pop(msg["id"]).set_result(msg)
            elif "method" in msg and "id" in msg:
                if msg["method"] == "session/request_permission":
                    await self.send({"jsonrpc": "2.0", "id": msg["id"], "result": {"outcome": {"outcome": "cancelled"}}})
                else:
                    await self.send({"jsonrpc": "2.0", "id": msg["id"],
                                     "error": {"code": -32601, "message": f"{msg['method']} is not supported by the workbench"}})


async def read_lines(stream: asyncio.StreamReader, chunk: int = 1 << 16) -> AsyncIterator[bytes]:
    """Yield newline-terminated lines of any length. ``StreamReader.readline``
    fails on a line longer than its buffer limit (64 KiB by default), and an
    ACP message carrying a long tool result is easily longer."""
    buf = bytearray()
    while data := await stream.read(chunk):
        buf += data
        start = 0
        while (end := buf.find(b"\n", start)) != -1:
            yield bytes(buf[start:end + 1])
            start = end + 1
        del buf[:start]
    if buf:
        yield bytes(buf)


def main() -> int:
    runtime = Path(sys.argv[1])
    return asyncio.run(Driver(runtime).run())


if __name__ == "__main__":
    sys.exit(main())
