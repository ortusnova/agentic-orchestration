import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import ChatHarness from "./ChatHarness.svelte";
import { planDoc, synced } from "./fixtures";
import type { WorkerCommand } from "../src/lib/api";
import type { BrokerEvent } from "../src/lib/chat";
import { session as identity } from "../src/lib/identity.svelte";
import type { SessionClient } from "../src/lib/session";
import type { StoryActions } from "../src/lib/sync";
import type { GraphNode, MergedGraph } from "../src/lib/types";
import type { WorkerClient, WorkerHandlers } from "../src/lib/worker";

class FakeWorker implements WorkerClient {
  sent: Record<string, unknown>[] = [];
  commands: WorkerCommand[] = [];
  h: WorkerHandlers | null = null;
  closed = false;
  async messages() { return { commands: this.commands }; }
  async command(cmd: Record<string, unknown>) {
    this.sent.push(cmd);
    if (cmd.op === "steer") this.commands.push({ id: this.commands.length + 1, op: "steer", text: cmd.text as string, session_id: "s1", actor_id: "a", actor_name: "Ann", time: Date.now() / 1000 });
    return {};
  }
  subscribe(h: WorkerHandlers) { this.h = h; return () => { this.closed = true; }; }
}

const noSession: SessionClient = {
  status: async () => ({ active: false }), start: async () => ({ active: true }), stop: async () => ({ active: false }),
  command: async () => ({}), subscribe: () => () => {},
  config: async () => ({ options: [], ready: true }), setConfig: async () => ({ options: [], ready: true }),
};

let app: ReturnType<typeof mount> | undefined;
beforeEach(() => { identity.identity = { name: "Ann", email: "", roles: ["planner", "approver"], id: "a" }; });
afterEach(() => {
  if (app) unmount(app);
  app = undefined;
  identity.identity = null;
  document.body.innerHTML = "";
});
async function settle() { for (let i = 0; i < 6; i++) { await tick(); await Promise.resolve(); } flushSync(); }
const $ = <T extends Element = HTMLElement>(s: string) => document.querySelector<T>(s);

function workerDoc(): { doc: MergedGraph; open: GraphNode } {
  const doc = planDoc();
  doc.sortie.dispatched = true;
  const open = doc.nodes.find((n) => n.plan_key === "accounts-api/open")!;
  open.runtime = { status: "running", turn_count: 3, last_message: "writing tests", stage: "implement", session_id: "s1", watchable: true, held: false, holder: null };
  return { doc, open };
}

type Harness = ReturnType<typeof mount> & { watch: (n: GraphNode | null) => void; select: (k: string | null) => void };
function harness(doc: MergedGraph, worker: FakeWorker, extra: Record<string, unknown> = {}): Harness {
  const h = mount(ChatHarness, { target: document.body, props: { initial: doc, session: noSession, workerClient: worker, ...extra } }) as Harness;
  app = h;
  return h;
}

const ev = (seq: number, text: string): BrokerEvent => ({
  seq, time: "2026-10-17T00:00:00Z", type: "update", update: { update: { sessionUpdate: "agent_message_chunk", content: { text } } },
});

describe("worker list", () => {
  it("lists running workers with their stage, and offers to watch them", async () => {
    const { doc, open } = workerDoc();
    const watched: string[] = [];
    harness(doc, new FakeWorker(), { onwatch: (n: GraphNode) => watched.push(n.plan_key) });
    await settle();
    const row = $(`[data-list='Workers'] [data-worker='${open.tracker!.identifier}']`)!;
    expect(row.textContent).toContain("running · implement · turn 3");
    expect(row.textContent).toContain("writing tests");
    row.querySelector<HTMLButtonElement>("[data-act='watch']")!.click();
    expect(watched).toEqual(["accounts-api/open"]);
  });

  it("says when Sortie runs no agents for the project", async () => {
    const { doc } = workerDoc();
    doc.sortie.dispatched = false;
    harness(doc, new FakeWorker());
    await settle();
    expect($("[data-note='not-dispatched']")).not.toBeNull();
  });

  it("offers a running story's worker in its details", async () => {
    const { doc, open } = workerDoc();
    const other = synced("billing/run", "Todo", {}, { status: "running", turn_count: 1, last_message: null, watchable: false });
    doc.nodes.push(other);
    const watched: string[] = [];
    const h = harness(doc, new FakeWorker(), { onwatch: (n: GraphNode) => watched.push(n.plan_key) });
    await settle();
    h.select(open.plan_key);
    await settle();
    $<HTMLButtonElement>("section.context .chips [data-act='watch']")!.click();
    expect(watched).toEqual([open.plan_key, open.plan_key]);
    h.select(other.plan_key);
    await settle();
    expect($("section.context .chips [data-act='watch']")).toBeNull();
    expect(watched).toEqual([open.plan_key, open.plan_key]);
  });
});

describe("selecting a running story", () => {
  function autoHarness(doc: MergedGraph, w: FakeWorker, extra: Record<string, unknown> = {}) {
    const watched: string[] = [];
    let h: Harness;
    h = harness(doc, w, { onwatch: (n: GraphNode) => { watched.push(n.plan_key); h.watch(n); }, ...extra });
    return { h, watched };
  }

  it("opens its worker's session instead of the planning chat", async () => {
    const { doc, open } = workerDoc();
    const { h, watched } = autoHarness(doc, new FakeWorker());
    await settle();
    expect($("textarea[aria-label='Message']")).not.toBeNull();
    h.select(open.plan_key);
    await settle();
    expect(watched).toEqual([open.plan_key]);
    expect($(`[data-worker='${open.tracker!.identifier}'] .state`)!.textContent).toContain(`Watching ${open.tracker!.identifier}`);
    expect($("textarea[aria-label='Message']")).toBeNull();
  });

  it("opens it from a deep link that already selects the story", async () => {
    const { doc, open } = workerDoc();
    const { watched } = autoHarness(doc, new FakeWorker(), { initialView: { epic: null, selected: open.plan_key, showCross: true } });
    await settle();
    expect(watched).toEqual([open.plan_key]);
    expect($(`[data-worker='${open.tracker!.identifier}']`)).not.toBeNull();
  });

  it("stays on planning after going back, until the selection changes", async () => {
    const { doc, open } = workerDoc();
    const { h, watched } = autoHarness(doc, new FakeWorker());
    await settle();
    h.select(open.plan_key);
    await settle();
    $<HTMLButtonElement>("[data-act='back-to-planning']")!.click();
    await settle();
    expect($("textarea[aria-label='Message']")).not.toBeNull();
    expect(watched).toEqual([open.plan_key]);
    h.select(null);
    await settle();
    h.select(open.plan_key);
    await settle();
    expect(watched).toEqual([open.plan_key, open.plan_key]);
  });

  it("leaves the planning chat for a story whose worker cannot be watched", async () => {
    const { doc } = workerDoc();
    const other = synced("billing/run", "Todo", {}, { status: "running", turn_count: 1, last_message: null, watchable: false });
    doc.nodes.push(other);
    const { h, watched } = autoHarness(doc, new FakeWorker());
    await settle();
    h.select(other.plan_key);
    await settle();
    expect(watched).toEqual([]);
    expect($("textarea[aria-label='Message']")).not.toBeNull();
  });
});

describe("watching a worker", () => {
  it("shows the session in place of the planning chat, and returns", async () => {
    const { doc, open } = workerDoc();
    const w = new FakeWorker();
    const h = harness(doc, w);
    await settle();
    h.watch(open);
    await settle();
    expect($(`[data-worker='${open.tracker!.identifier}'] .state`)!.textContent).toContain(`Watching ${open.tracker!.identifier}`);
    expect($(".session-bar .sub")!.textContent).toContain("accounts-api/open · implement");
    h.select(open.plan_key);
    await settle();
    expect($("section.context .chips [data-act='watch']")).toBeNull();
    w.h!.onRun({ identifier: open.tracker!.identifier, session_id: "s1", stage: "implement", follow: false });
    w.h!.onEvent(ev(1, "reading the code"));
    w.h!.onEvent({ seq: 2, time: "2026-10-17T00:00:01Z", type: "state", floor: "human" });
    await settle();
    expect($(".log")!.textContent).toContain("reading the code");
    expect($(".session-bar .sub")!.textContent).toContain("floor: human");
    $<HTMLButtonElement>("[data-act='back-to-planning']")!.click();
    await settle();
    expect(w.closed).toBe(true);
    expect($("[data-worker]:not(li)")).toBeNull();
    expect($("textarea[aria-label='Message']")).not.toBeNull();
  });

  it("lets an approver steer, take over, hand back, and cancel, and names who sent a message", async () => {
    const { doc, open } = workerDoc();
    const w = new FakeWorker();
    const cancelled: string[] = [];
    const actions = { cancel: async (k: string) => { cancelled.push(k); } } as unknown as StoryActions;
    const h = harness(doc, w, { actions });
    await settle();
    h.watch(open);
    await settle();
    w.h!.onRun({ identifier: "x", session_id: "s1", stage: "implement", follow: false });
    const box = $<HTMLTextAreaElement>("textarea[aria-label='Steer the agent']")!;
    box.value = "use the invoice table";
    box.dispatchEvent(new Event("input"));
    await settle();
    $<HTMLFormElement>("form.composer")!.requestSubmit();
    await settle();
    expect(w.sent).toEqual([{ op: "steer", text: "use the invoice table" }]);
    expect($(".msg.user")!.textContent).toBe("Annuse the invoice table");
    $<HTMLButtonElement>("[data-act='interrupt']")!.click();
    await settle();
    $<HTMLButtonElement>("[data-act='handback']")!.click();
    await settle();
    expect(w.sent.map((c) => c.op)).toEqual(["steer", "interrupt", "handback"]);
    $<HTMLButtonElement>("[data-act='cancel-worker']")!.click();
    await settle();
    expect(cancelled).toEqual([]);
    $<HTMLButtonElement>("[data-confirm='cancel'] [data-act='keep']")!.click();
    await settle();
    expect($("[data-confirm='cancel']")).toBeNull();
    $<HTMLButtonElement>("[data-act='cancel-worker']")!.click();
    await settle();
    $<HTMLButtonElement>("[data-confirm='cancel'] [data-act='confirm']")!.click();
    await settle();
    expect(cancelled).toEqual([open.plan_key]);
  });

  it("lets a planner only watch", async () => {
    identity.identity = { name: "Pat", email: "", roles: ["planner"], id: "p" };
    const { doc, open } = workerDoc();
    const w = new FakeWorker();
    const h = harness(doc, w);
    await settle();
    h.watch(open);
    await settle();
    w.h!.onEvent({ seq: 1, time: "2026-10-17T00:00:00Z", type: "permission", id: "p1",
      request: { toolCall: { title: "run tests" }, options: [{ optionId: "allow", name: "Allow", kind: "allow_once" }] } });
    await settle();
    expect($("[data-permission='p1']")!.textContent).toContain("run tests");
    expect($("[data-permission='p1'] button")).toBeNull();
    expect($("form.composer")).toBeNull();
    expect($("[data-note='watch-only']")).not.toBeNull();
    expect(document.querySelectorAll("[data-act='interrupt'], [data-act='handback'], [data-act='cancel-worker']").length).toBe(0);
  });

  it("follows the story to its next run, then says it ended", async () => {
    const { doc, open } = workerDoc();
    const w = new FakeWorker();
    const h = harness(doc, w);
    await settle();
    h.watch(open);
    await settle();
    w.h!.onRun({ identifier: "x", session_id: "s1", stage: "implement", follow: false });
    w.h!.onEvent(ev(1, "first run"));
    await settle();
    w.h!.onRun({ identifier: "x", session_id: "s2", stage: "review", follow: true });
    w.h!.onEvent(ev(1, "second run"));
    await settle();
    const text = $(".log")!.textContent!;
    expect(text).not.toContain("first run");
    expect(text).toContain("Following the new run (review)");
    expect(text).toContain("second run");
    expect($(".session-bar .sub")!.textContent).toContain("review");
    w.h!.onEnded();
    await settle();
    expect($(".log")!.textContent).toContain("The worker ended");
    expect($<HTMLTextAreaElement>("textarea[aria-label='Steer the agent']")!.disabled).toBe(true);
  });
});
