import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import ChatHarness from "./ChatHarness.svelte";
import HomeHarness from "./HomeHarness.svelte";
import { graph, node, synced } from "./fixtures";
import ProjectView from "../src/components/ProjectView.svelte";
import type { HomeDoc } from "../src/lib/home";
import { session as identity } from "../src/lib/identity.svelte";
import { actionText, resultText, resultTone } from "../src/lib/supervisor";
import type { SessionClient } from "../src/lib/session";
import type { GraphNode, MergedGraph, Supervisor, SupervisorAction, ViewState } from "../src/lib/types";

// The supervisor's actions and its pause (agent-platform/run-supervision#The
// supervisor acts only through a scoped API, and each action is recorded).
// The documents are the backend's, built from the fake Sortie's state.

globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;

const C = "billing";
const MERGED = `${C}/b1`;
const REFUSED = `${C}/b2`;
const REPO = "ortusnova/agentic-orchestration";
const PULL_URL = `https://github.com/${REPO}/pull/75`;
const COMMIT = "6684940c0ffee6684940c0ffee6684940c0ffee0";
const MERGED_AT = Date.parse("2026-11-03T10:12:00Z") / 1000;
const REFUSED_AT = Date.parse("2026-11-03T09:00:00Z") / 1000;

const merge: SupervisorAction = {
  id: "a-2", at: MERGED_AT, action: "record_merge", identifier: "T-1", key: MERGED, label: "b1", title: "Invoices",
  url: "https://tracker/1", pull: { ref: `${REPO}#75`, url: PULL_URL },
  evidence: [
    { name: null, value: "The forge shows the pull request merged.", url: null },
    { name: "merge commit", value: COMMIT, url: `https://github.com/${REPO}/commit/${COMMIT}` },
  ],
  result: "done", detail: null,
};
const refused: SupervisorAction = {
  id: "a-1", at: REFUSED_AT, action: "POST /api/v1/{identifier}/hold", identifier: "T-2", key: REFUSED, label: "b2", title: "Taxes",
  url: "https://tracker/2", pull: null, evidence: [{ name: null, value: "It looks stuck.", url: null }],
  result: "refused", detail: "forbidden: not a supervisor action",
};

const supervisor = (extra: Partial<Supervisor> = {}): Supervisor =>
  ({ status: "active", paused_at: null, paused_by: null, reason: null, actions: [merge, refused], total: 2, ...extra });

function homeDoc(): HomeDoc {
  return {
    generated_at: 0, plan: { valid: true, errors: 0, has_graph: true }, milestones_declared: true, story_progress: "live",
    health: { verdict: "healthy", reasons: [], slots: 3, busy: 0, free: 3, running: [], held: [], runway: 3 },
    milestones: [], goal: null, next: null, critical_path: null, timeline: null, not_ready: [], questions: [],
    needs_person: { total: 0, questions: 0, approve: 0, approve_stories: 0, spec: 0, operator: 0, nearest: null },
  };
}

function story(key: string, ident: string, actions: SupervisorAction[]): GraphNode {
  const n = synced(key, "In Review", { supervisor_actions: actions });
  return { ...n, tracker: { ...n.tracker!, identifier: ident } };
}

function doc(sup: Supervisor | null = supervisor(), dispatched = true): MergedGraph {
  const d = graph([node(C), story(MERGED, "T-1", sup ? [merge] : []), story(REFUSED, "T-2", sup ? [refused] : [])], [],
    { home: homeDoc(), supervisor: sup });
  d.sortie = { ...d.sortie, dispatched, project: "T" };
  return d;
}

let app: ReturnType<typeof mount> | undefined;
afterEach(() => {
  if (app) unmount(app);
  app = undefined;
  document.body.innerHTML = "";
  identity.identity = null;
  vi.unstubAllGlobals();
});

const settle = async () => { for (let i = 0; i < 8; i++) { await tick(); await Promise.resolve(); } flushSync(); };
const $ = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);
const $$ = <T extends Element = HTMLElement>(sel: string) => [...document.querySelectorAll<T>(sel)];
const text = (sel: string) => $(sel)?.textContent?.replace(/\s+/g, " ").trim() ?? "";
const approver = () => (identity.identity = { name: "Ann Approver", email: "", roles: ["planner", "approver"], id: "a" });
const planner = () => (identity.identity = { name: "Pat Planner", email: "", roles: ["planner"], id: "p" });

type Home = ReturnType<typeof mount> & { setDoc: (d: MergedGraph) => void };
async function home(d: MergedGraph, onsupervisor?: (op: "pause" | "resume") => Promise<string | null>) {
  app = mount(HomeHarness, { target: document.body, props: { initial: d, onsupervisor } });
  await settle();
  return app as Home;
}

describe("the supervisor on the home", () => {
  it("shows its status and its recent actions, newest first, each with the story it touched", async () => {
    approver();
    await home(doc());
    expect($("[data-supervisor-status]")!.dataset.supervisorStatus).toBe("active");
    expect(text("[data-supervisor-status]")).toBe("Acting");
    expect($$("[data-supervisor] .act").map((li) => li.dataset.action)).toEqual(["record_merge", "POST /api/v1/{identifier}/hold"]);
  });

  // Scenario: An action and its evidence
  it("names a recorded merge's pull request, the merge commit and the time, and links to its story and pull request", async () => {
    await home(doc());
    const act = $("[data-supervisor] [data-action='record_merge']")!;
    expect(act.querySelector(".what")!.textContent).toBe("Recorded a merge");
    expect(act.querySelector(".res")!.textContent).toBe("done");
    expect(act.querySelector("time")!.getAttribute("datetime")).toBe("2026-11-03T10:12:00.000Z");
    const storyLink = act.querySelector<HTMLAnchorElement>(`a[data-story='${MERGED}']`)!;
    expect(storyLink.textContent).toBe("b1");
    expect(storyLink.getAttribute("href")).toBe(`#/p/homelab/graph?epic=${C}&sel=${encodeURIComponent(MERGED)}`);
    const pull = act.querySelector<HTMLAnchorElement>("a[data-pull]")!;
    expect(pull.href).toBe(PULL_URL);
    expect(pull.textContent!.trim()).toBe(`${REPO}#75`);
    const commit = act.querySelector<HTMLAnchorElement>("[data-evidence='merge commit'] a")!;
    expect(commit.textContent).toBe(COMMIT.slice(0, 7));
    expect(commit.href).toBe(`https://github.com/${REPO}/commit/${COMMIT}`);
    expect(act.querySelector("[data-evidence=''] dd")!.textContent).toBe("The forge shows the pull request merged.");
  });

  // Scenario: A refused action
  it("shows a refused route as refused, with why, beside its story", async () => {
    await home(doc());
    const act = $("[data-supervisor] [data-action='POST /api/v1/{identifier}/hold']")!;
    expect(act.dataset.result).toBe("refused");
    expect(act.querySelector(".what")!.textContent).toBe("Tried POST /api/v1/{identifier}/hold");
    expect(act.querySelector(".res")!.textContent).toBe("refused");
    expect(act.querySelector(".detail")!.textContent).toBe("forbidden: not a supervisor action");
    expect(act.querySelector(`a[data-story='${REFUSED}']`)).not.toBeNull();
    expect(act.querySelector("[data-pull]")).toBeNull();
  });

  it("pauses and resumes for an approver, and shows who paused it", async () => {
    approver();
    const ops: string[] = [];
    const h = await home(doc(), async (op) => { ops.push(op); return null; });
    expect($("[data-act='resume-supervisor']")).toBeNull();
    $<HTMLButtonElement>("[data-act='pause-supervisor']")!.click();
    await settle();
    expect(ops).toEqual(["pause"]);
    h.setDoc(doc(supervisor({ status: "paused", paused_by: "Ann Approver", paused_at: MERGED_AT })));
    await settle();
    expect(text("[data-supervisor-status]")).toBe("Paused");
    expect(text("[data-supervisor] .by")).toContain("paused by Ann Approver");
    expect($("[data-act='pause-supervisor']")).toBeNull();
    $<HTMLButtonElement>("[data-act='resume-supervisor']")!.click();
    await settle();
    expect(ops).toEqual(["pause", "resume"]);
  });

  it("says why a pause did not go through", async () => {
    approver();
    await home(doc(), async () => "this Sortie has no supervisor to pause or resume");
    $<HTMLButtonElement>("[data-act='pause-supervisor']")!.click();
    await settle();
    expect(text("[data-supervisor] [role='alert']")).toBe("this Sortie has no supervisor to pause or resume");
  });

  it("offers a planner no Pause", async () => {
    planner();
    await home(doc(), async () => null);
    expect(text("[data-supervisor-status]")).toBe("Acting");
    expect($("[data-act='pause-supervisor']")).toBeNull();
  });

  it("says when Sortie reports no supervisor, or does not run this project", async () => {
    approver();
    const h = await home(doc(null), async () => null);
    expect($("[data-note='no-supervisor']")).not.toBeNull();
    expect($("[data-act='pause-supervisor']")).toBeNull();
    h.setDoc(doc(null, false));
    await settle();
    expect($("[data-note='not-dispatched']")).not.toBeNull();
  });

  it("says when it has taken no action", async () => {
    await home(doc(supervisor({ actions: [], total: 0 })));
    expect($("[data-note='no-actions']")).not.toBeNull();
  });
});

const noSession: SessionClient = {
  status: async () => ({ active: false }), start: async () => ({ active: true }), stop: async () => ({ active: false }),
  command: async () => ({}), subscribe: () => () => {},
  config: async () => ({ options: [], ready: true }), setConfig: async () => ({ options: [], ready: true }),
};

describe("the supervisor beside a story", () => {
  async function select(key: string) {
    const h = mount(ChatHarness, { target: document.body, props: { initial: doc(), session: noSession } }) as
      ReturnType<typeof mount> & { select: (k: string | null) => void; setEpic: (k: string | null) => void };
    app = h;
    h.setEpic(C);
    h.select(key);
    await settle();
  }

  it("shows the merge it recorded in the story's details, with the pull request and the merge commit", async () => {
    await select(MERGED);
    const box = $(`[data-supervisor-actions='${MERGED}']`)!;
    expect(box).not.toBeNull();
    expect([...box.querySelectorAll<HTMLElement>(".act")].map((a) => a.dataset.action)).toEqual(["record_merge"]);
    expect(box.querySelector<HTMLAnchorElement>("a[data-pull]")!.href).toBe(PULL_URL);
    expect(box.querySelector("[data-evidence='merge commit'] a")!.textContent).toBe(COMMIT.slice(0, 7));
  });

  it("shows the refusal beside the story it was about, and nothing beside a story it did not touch", async () => {
    await select(REFUSED);
    expect([...document.querySelectorAll<HTMLElement>(`[data-supervisor-actions='${REFUSED}'] .act`)].map((a) => a.dataset.result)).toEqual(["refused"]);
    expect($("[data-supervisor-actions] [data-action='record_merge']")).toBeNull();
  });
});

// Through the workbench's routes, as the live preview is clicked.

class FakeEventSource {
  static last: FakeEventSource | null = null;
  listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
  onerror: (() => void) | null = null;
  readyState = 1;
  constructor(readonly url: string) { FakeEventSource.last = this; }
  addEventListener(name: string, fn: (e: MessageEvent) => void) { (this.listeners[name] ??= []).push(fn); }
  emit(name: string, data: unknown) { for (const fn of this.listeners[name] ?? []) fn(new MessageEvent(name, { data: JSON.stringify(data) })); }
  close() { this.readyState = 2; }
}

/** The workbench backend over a fake Sortie: a pause or resume changes the
 * supervisor's status in Sortie, and the next graph shows it. */
function backend() {
  let sup = supervisor();
  const calls: { method: string; url: string }[] = [];
  const fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    calls.push({ method, url });
    const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    if (url.endsWith("/api/projects/homelab")) return json({ project: { id: 1, slug: "homelab", name: "Homelab" }, tracker: null, tracker_error: null });
    if (url.endsWith("/graph")) return json(doc(sup));
    const op = url.match(/\/supervisor\/(pause|resume)$/)?.[1];
    if (op && method === "POST") {
      sup = op === "pause" ? supervisor({ status: "paused", paused_by: "Ann Approver", paused_at: MERGED_AT }) : supervisor();
      queueMicrotask(() => FakeEventSource.last?.emit("graph", doc(sup)));
      return json({ supervisor: sup });
    }
    return json({});
  });
  return { fetch, calls, status: () => sup.status };
}

describe("on the project's page", () => {
  it("Pause and Resume change the supervisor's status in Sortie", async () => {
    approver();
    const be = backend();
    vi.stubGlobal("fetch", be.fetch);
    vi.stubGlobal("EventSource", FakeEventSource);
    const view: ViewState = { epic: null, selected: null, showCross: true };
    app = mount(ProjectView, { target: document.body, props: { slug: "homelab", page: "home", view, onmissing: () => {} } });
    await settle();
    expect(text("[data-supervisor-status]")).toBe("Acting");
    $<HTMLButtonElement>("[data-act='pause-supervisor']")!.click();
    await settle();
    expect(be.calls).toContainEqual({ method: "POST", url: "/api/projects/homelab/supervisor/pause" });
    expect(be.status()).toBe("paused");
    expect(text("[data-supervisor-status]")).toBe("Paused");
    $<HTMLButtonElement>("[data-act='resume-supervisor']")!.click();
    await settle();
    expect(be.calls).toContainEqual({ method: "POST", url: "/api/projects/homelab/supervisor/resume" });
    expect(be.status()).toBe("active");
    expect(text("[data-supervisor-status]")).toBe("Acting");
  });
});

describe("how Sortie's records read", () => {
  it("names each action in Sortie's words, done or tried", () => {
    const a = (action: string, result: string) => ({ ...merge, action, result });
    expect(actionText(a("release", "done"))).toBe("Released a parked story");
    expect(actionText(a("merge", "rejected"))).toBe("Tried to merge the pull request");
    expect(actionText(a("live_proof", "failed"))).toBe("Tried to request a live proof");
    expect(actionText(a("POST /api/v1/plan/sync", "refused"))).toBe("Tried POST /api/v1/plan/sync");
  });

  it("shows a rejected action, one Sortie found did not hold, in the refused tone", () => {
    expect(resultText("rejected")).toBe("rejected");
    expect(resultTone("rejected")).toBe("refused");
    expect(resultTone("failed")).toBe("failed");
  });

  it("offers no pause for a supervisor that is off", async () => {
    approver();
    await home(doc(supervisor({ status: "off", reason: "it runs no supervisor session to pause" })), async () => null);
    expect(text("[data-supervisor-status]")).toBe("Off");
    expect($("[data-act='pause-supervisor']")).toBeNull();
    expect($("[data-act='resume-supervisor']")).toBeNull();
    expect($$("[data-supervisor] .act")).toHaveLength(2);
  });
});
