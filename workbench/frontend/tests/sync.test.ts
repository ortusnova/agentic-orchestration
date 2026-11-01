import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, describe, expect, it } from "vitest";
import SyncHarness from "./SyncHarness.svelte";
import GraphHarness from "./GraphHarness.svelte";
import ChatHarness from "./ChatHarness.svelte";
import { planDoc } from "./fixtures";
import type { SyncClient } from "../src/lib/sync";
import { errorTarget } from "../src/lib/sync";
import { GraphModel } from "../src/lib/model";
import type { SessionClient } from "../src/lib/session";
import { session as identity } from "../src/lib/identity.svelte";
import type { GraphNode, SyncStatus as SyncStatusT } from "../src/lib/types";

class FakeSync implements SyncClient {
  approved: { keys?: string[]; epic?: string }[] = [];
  calls: string[] = [];
  async retry() { this.calls.push("retry"); return status({ state: "syncing" }); }
  async approve(t: { keys?: string[]; epic?: string }) { this.approved.push(t); return {}; }
  async pullBack(k: string) { this.calls.push(`pull-back ${k}`); return {}; }
  async cancel(k: string) { this.calls.push(`cancel ${k}`); return {}; }
  async supersede(k: string) { this.calls.push(`supersede ${k}`); return {}; }
}

function status(extra: Partial<SyncStatusT> = {}): SyncStatusT {
  return { state: "synced", synced_at: 1_800_000_000, failed_at: null, reason: null, result: null, retry_at: null, runs: 1, ...extra };
}

let app: ReturnType<typeof mount> | undefined;
afterEach(() => {
  if (app) unmount(app);
  app = undefined;
  document.body.innerHTML = "";
});
async function settle() { for (let i = 0; i < 6; i++) { await tick(); await Promise.resolve(); } flushSync(); }
const $ = <T extends Element = HTMLElement>(s: string) => document.querySelector<T>(s);

describe("the tracker notice", () => {
  function show(st: SyncStatusT | null, sync = new FakeSync()) {
    app = mount(SyncHarness, { target: document.body, props: { status: st, onretry: () => sync.retry() } });
    return sync;
  }

  it("shows nothing while the tracker matches the plan, a sync runs, or none has run", async () => {
    for (const st of [status({ state: "syncing" }), status(), status({ state: "idle", synced_at: null, runs: 0 }), null]) {
      show(st);
      await settle();
      expect($("[data-sync]")).toBeNull();
      expect(document.body.textContent!.toLowerCase()).not.toContain("sync");
      unmount(app!); app = undefined; document.body.innerHTML = "";
    }
  });

  it("says the tracker is not updated, why, and tries again", async () => {
    const sync = show(status({ state: "failed", failed_at: 1_800_000_100, retry_at: 1_800_000_160,
      reason: { code: "sortie_unreachable", message: "cannot reach Sortie: connection refused" } }));
    await settle();
    expect($("[data-sync]")!.dataset.sync).toBe("failed");
    expect($("[data-sync]")!.textContent).toContain("Tracker not updated");
    $<HTMLButtonElement>("[data-sync]")!.click();
    await settle();
    expect($("[role='alert']")!.textContent).toContain("Sortie is unreachable");
    expect(document.body.textContent).toContain("Trying again at");
    $<HTMLButtonElement>("[data-act='retry-sync']")!.click();
    await settle();
    expect(sync.calls).toEqual(["retry"]);
  });

  it("drops the notice once a sync succeeds", async () => {
    show(status({ state: "failed", failed_at: 1_800_000_100, retry_at: 1_800_000_160,
      reason: { code: "sortie_unreachable", message: "cannot reach Sortie: connection refused" } }));
    await settle();
    $<HTMLButtonElement>("[data-sync]")!.click();
    await settle();
    expect($("[role='alert']")!.textContent).toContain("Sortie is unreachable");
    (app as unknown as { update: (s: SyncStatusT) => void }).update(status({ state: "syncing" }));
    await settle();
    expect($("[data-sync]")).toBeNull();
    (app as unknown as { update: (s: SyncStatusT) => void }).update(status({ synced_at: 1_800_000_200, runs: 2 }));
    await settle();
    expect($("[data-sync]")).toBeNull();
    expect($("[data-sync-result]")).toBeNull();
    expect(document.body.textContent!.toLowerCase()).not.toContain("tracker not updated");
  });

  it("names a failed write, what it updated first, and a missing capability", async () => {
    show(status({ state: "failed", reason: { code: "tracker_unsupported_operation", message: "the tracker lacks link_dependency" },
      result: { created: [{ plan_key: "billing/b1", identifier: "T-1" }], failed: { op: "create", plan_key: "billing/b2", error: "boom" } } }));
    await settle();
    $<HTMLButtonElement>("[data-sync]")!.click();
    await settle();
    expect($("[role='alert']")!.textContent).toContain("link_dependency");
    expect($("[data-sync-failed]")!.textContent).toContain("billing/b2");
    expect($("[data-sync-list='written']")!.textContent).toContain("T-1");
  });

  it("says a read-only workbench is read-only", async () => {
    show(status({ state: "disabled" }));
    await settle();
    expect($("[data-sync]")!.textContent).toContain("Read-only");
  });

  it("calls a story nobody approved not approved", () => {
    const doc = planDoc();
    const s = doc.nodes.find((n) => n.kind === "story")!;
    Object.assign(s, { status: "draft", tracker: null });
    expect(new GraphModel(doc).statusLabel(s)).toBe("Not approved");
  });
});

describe("validation panel", () => {
  it("lists every error and selecting one selects its node", async () => {
    const doc = planDoc();
    doc.validation = {
      valid: false, version: 2,
      errors: [
        { code: "cycle", message: "deliverables depend on each other in a cycle", file: "x", change: "billing", deliverable: "tax", members: ["billing/tax", "billing/invoice"] },
        { code: "unknown_change", message: "ghost is not active", file: "openspec/plan.yaml", change: "ghost", deliverable: null, members: [] },
      ],
    };
    const h = mount(GraphHarness, { target: document.body, props: { initial: doc } }) as ReturnType<typeof mount> & { getView: () => { epic: string | null; selected: string | null } };
    app = h;
    await settle();
    const errs = document.querySelectorAll<HTMLButtonElement>(".validation .err");
    expect(errs).toHaveLength(2);
    expect(errs[1].disabled).toBe(true);
    errs[0].click();
    await settle();
    expect(h.getView()).toMatchObject({ epic: "billing", selected: "billing/tax" });
    expect($(".card[data-key='billing/tax']")!.getAttribute("aria-pressed")).toBe("true");
  });

  it("finds an error's node", () => {
    const has = (k: string) => ["a", "a/b"].includes(k);
    expect(errorTarget({ change: "a", deliverable: "b", members: [] }, has)).toBe("a/b");
    expect(errorTarget({ change: "a", deliverable: "zz", members: [] }, has)).toBe("a");
    expect(errorTarget({ change: null, deliverable: null, members: [] }, has)).toBeNull();
  });
});

const noSession: SessionClient = {
  status: async () => ({ active: false }), start: async (_message: string) => ({ active: true }), stop: async () => ({ active: false }),
  command: async () => ({}), subscribe: () => () => {},
  config: async () => ({ options: [], ready: true }), setConfig: async () => ({ options: [], ready: true }),
};

function chat(doc: ReturnType<typeof planDoc>, sync: FakeSync) {
  const h = mount(ChatHarness, { target: document.body, props: { initial: doc, session: noSession, actions: sync } }) as ReturnType<typeof mount> & { select: (k: string | null) => void; setEpic: (k: string | null) => void };
  app = h;
  return h;
}

function story(doc: ReturnType<typeof planDoc>, extra: Partial<GraphNode>): GraphNode {
  const n = doc.nodes.find((x) => x.kind === "story")!;
  Object.assign(n, extra);
  return n;
}

describe("approver actions", () => {
  afterEach(() => { identity.identity = null; });
  const approver = () => { identity.identity = { name: "Ann", email: "", roles: ["planner", "approver"], id: "a" }; };

  it("approves a never-approved epic in one action", async () => {
    approver();
    const doc = planDoc();
    doc.nodes.find((n) => n.plan_key === "billing")!.approval = "draft";
    for (const n of doc.nodes.filter((x) => x.change === "billing" && x.kind === "story")) { n.approval = "draft"; n.plan_status = "draft"; }
    const sync = new FakeSync();
    const h = chat(doc, sync);
    await settle();
    h.select("billing");
    await settle();
    expect($(".chips [data-act='approve-epic']")).not.toBeNull();
    $<HTMLButtonElement>("[data-act='approve-epic']")!.click();
    await settle();
    expect(sync.approved).toEqual([{ epic: "billing" }]);
  });

  it("approves one draft story and explains a held one", async () => {
    approver();
    const doc = planDoc();
    const s = story(doc, { plan_status: "draft", approval: "draft" });
    const sync = new FakeSync();
    const h = chat(doc, sync);
    await settle();
    h.select(s.plan_key);
    await settle();
    // Approve sits beside the status chips, above the spec, so it is seen without scrolling.
    expect($(".chips [data-act='approve']")).not.toBeNull();
    expect($("section.context .sel .acts")).toBeNull();
    $<HTMLButtonElement>("[data-act='approve']")!.click();
    await settle();
    expect(sync.approved).toEqual([{ keys: [s.plan_key] }]);
    unmount(h); document.body.innerHTML = "";
    const doc2 = planDoc();
    const held = story(doc2, { plan_status: "held", held_by: ["billing/zz"], approval: "approved" });
    const h2 = chat(doc2, sync);
    await settle();
    h2.select(held.plan_key);
    await settle();
    expect($("[data-why='held']")!.textContent).toContain("billing/zz");
    expect($("[data-act='approve']")).toBeNull();
  });

  it("pulls back a released story and shows a re-review reason", async () => {
    approver();
    const doc = planDoc();
    const s = story(doc, { plan_status: "released", approval: "approved" });
    const sync = new FakeSync();
    const h = chat(doc, sync);
    await settle();
    h.select(s.plan_key);
    await settle();
    expect($(".chips [data-act='pull-back']")).not.toBeNull();
    $<HTMLButtonElement>("[data-act='pull-back']")!.click();
    await settle();
    expect(sync.calls).toEqual([`pull-back ${s.plan_key}`]);
  });

  it("asks before it cancels a story, in the page", async () => {
    approver();
    const doc = planDoc();
    const s = story(doc, { plan_status: "frozen", frozen: true,
      tracker: { id: "i1", identifier: "T-1", title: "t", state: "In Progress", terminal: false, url: null, parent_id: null } });
    const sync = new FakeSync();
    const h = chat(doc, sync);
    await settle();
    h.select(s.plan_key);
    await settle();
    expect($(".chips [data-act='cancel']")).not.toBeNull();
    $<HTMLButtonElement>("[data-act='cancel']")!.click();
    await settle();
    expect(sync.calls).toEqual([]);
    expect($("[data-confirm='cancel']")!.textContent).toContain("need re-review");
    $<HTMLButtonElement>("[data-confirm='cancel'] [data-act='confirm']")!.click();
    await settle();
    expect(sync.calls).toEqual([`cancel ${s.plan_key}`]);
    expect($("[data-confirm]")).toBeNull();
  });

  it("explains a re-review", async () => {
    approver();
    const doc = planDoc();
    const s = story(doc, { plan_status: "needs_rereview", rereview: { reason: "blocker_canceled", ref: "billing/a3" } });
    const h = chat(doc, new FakeSync());
    await settle();
    h.select(s.plan_key);
    await settle();
    expect($("[data-why='rereview']")!.textContent).toContain("billing/a3 was cancelled");
    expect($("[data-act='approve']")).not.toBeNull();
  });

  it("shows held and re-review on the story cards", async () => {
    const doc = planDoc();
    const [a, b] = doc.nodes.filter((x) => x.kind === "story" && x.change === doc.nodes.find((n) => n.kind === "story")!.change);
    Object.assign(a, { plan_status: "held", held_by: ["x/y"], tracker: null, runtime: null, status: "draft" });
    Object.assign(b, { plan_status: "needs_rereview", rereview: { reason: "blocker_canceled", ref: "x/y" }, tracker: null, runtime: null, status: "draft" });
    app = mount(GraphHarness, { target: document.body, props: { initial: doc } });
    await settle();
    (app as unknown as { getView: () => { epic: string | null } }).getView().epic = a.change;
    await settle();
    expect($(`[data-key='${a.plan_key}'] .meta`)!.textContent).toBe("held for approval");
    expect($(`[data-key='${b.plan_key}'] .meta`)!.textContent).toBe("needs re-review");
  });

  it("shows a cancelled story as cancelled, not done, and leaves it out of its epic", async () => {
    const doc = planDoc();
    const inv = doc.nodes.find((n) => n.plan_key === "billing/invoice")!;
    inv.tracker = { ...inv.tracker!, state: "Canceled", terminal: true };
    const billing = doc.nodes.find((n) => n.plan_key === "billing")!;
    // Without plan control there is no cancel state: a terminal state is done.
    expect(new GraphModel(doc).statusClass(inv)).toBe("done");
    doc.plan_states = { release: "Todo", backlog: "Backlog", cancel: "Canceled", done: "Done" };
    const model = new GraphModel(doc);
    expect(model.statusClass(inv)).toBe("cancelled");
    expect(model.isDone(inv)).toBe(false);
    const all = model.storiesOf("billing").length;
    expect(model.progress(billing)).toEqual({ done: 0, total: all - 1 });
    app = mount(GraphHarness, { target: document.body, props: { initial: doc } });
    await settle();
    (app as unknown as { getView: () => { epic: string | null } }).getView().epic = "billing";
    await settle();
    expect($("[data-key='billing/invoice']")!.classList.contains("cancelled")).toBe(true);
    expect($("[data-key='billing/invoice'] .meta")!.textContent).toBe("cancelled");
    expect(document.body.textContent).toContain("1 cancelled");
  });

  it("does not call a story waiting for approval ready", async () => {
    const doc = planDoc();
    const s = doc.nodes.find((n) => n.plan_key === "billing/invoice")!;
    const model = new GraphModel(doc);
    expect(model.isReady(s)).toBe(true);
    for (const st of ["draft", "held", "needs_rereview"] as const) {
      s.plan_status = st;
      expect(new GraphModel(doc).isReady(s), st).toBe(false);
    }
    s.plan_status = "ready";
    expect(new GraphModel(doc).isReady(s)).toBe(true);
    s.plan_status = "draft";
    app = mount(GraphHarness, { target: document.body, props: { initial: doc } });
    await settle();
    (app as unknown as { getView: () => { epic: string | null } }).getView().epic = "billing";
    await settle();
    expect($("[data-key='billing/invoice'] .meta")!.textContent).toBe("not approved");
  });

  it("hides every action from a planner", async () => {
    identity.identity = { name: "Pat", email: "", roles: ["planner"], id: "p" };
    const doc = planDoc();
    const s = story(doc, { plan_status: "draft", approval: "draft" });
    const h = chat(doc, new FakeSync());
    await settle();
    h.select(s.plan_key);
    await settle();
    expect($("[data-plan-status='draft']")).not.toBeNull();
    expect(document.querySelectorAll("[data-act]").length).toBe(0);
  });
});
