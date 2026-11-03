/** The workbench shows what each worker is doing and what waits on it
 * (agent-platform/run-supervision): the home's slot lanes, the Workers view
 * and a story's run state, from fleet documents as the backend serves them
 * against the fake Sortie. */
import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import ChatHarness from "./ChatHarness.svelte";
import HomeHarness from "./HomeHarness.svelte";
import { graph, synced } from "./fixtures";
import WorkersPage from "../src/components/WorkersPage.svelte";
import WorkersHarness from "./WorkersHarness.svelte";
import WorkerPane from "../src/components/WorkerPane.svelte";
import { boxGeometry, duration, type Fleet, type Job, type Lanes, type Pull, type Stuck, type Wait, type Worker } from "../src/lib/fleet";
import type { HomeDoc, HomeQuestion } from "../src/lib/home";
import { session as identity } from "../src/lib/identity.svelte";
import { parseRoute, watchHref } from "../src/lib/router";
import type { SessionClient } from "../src/lib/session";
import type { StoryActions } from "../src/lib/sync";
import type { GraphNode, MergedGraph } from "../src/lib/types";
import type { WorkerClient, WorkerHandlers } from "../src/lib/worker";

globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;

let app: ReturnType<typeof mount> | undefined;
beforeEach(() => { identity.identity = { name: "Ann", email: "", roles: ["planner", "approver"], id: "a" }; });
afterEach(() => {
  if (app) unmount(app);
  app = undefined;
  identity.identity = null;
  document.body.innerHTML = "";
});
const settle = async () => { for (let i = 0; i < 6; i++) { await tick(); await Promise.resolve(); } flushSync(); };
const $ = <T extends Element = HTMLElement>(s: string) => document.querySelector<T>(s);
const $$ = (s: string) => [...document.querySelectorAll<HTMLElement>(s)];
const text = (s: string) => $(s)?.textContent?.replace(/\s+/g, " ").trim() ?? "";

const NOW = 1_793_700_000;
const QUESTION = "Which disk holds the seed image: the NVMe or the SATA one?";
const ref = (identifier: string, key: string) => ({ identifier, key, label: key.split("/")[1], title: `Title ${key}` });

function job(identifier: string, key: string, extra: Partial<Job> = {}): Job {
  return {
    ...ref(identifier, key), sortie: "taichi", worker: "taichi", phase: "live_proof", phase_text: "live proof", stage: "build",
    step: "seed-vm-recreate", step_at: NOW - 40, started_at: NOW - 1500, elapsed: 1500, ends_at: NOW + 1200, estimated: true,
    session_id: "s34", watchable: true, hold: null, pull: null, slot: 1, ...extra,
  };
}

function stuck(identifier: string, key: string, sortie: string, extra: Partial<Stuck>): Stuck {
  return { ...ref(identifier, key), sortie, worker: sortie, kind: "question", reason: `asks: ${QUESTION}`, since: NOW - 900,
    action: "answer", slot: 2, ...extra };
}

function wait(identifier: string, key: string, number: number): Wait {
  return { ...ref(identifier, key), sortie: "taichi", kind: "live_proof", reason: "its live proof needs a working vm chain",
    release: "the live proof running here passes", since: NOW - 3000,
    pull: { number, repository: "ortusnova/homelab", url: `https://github.com/ortusnova/homelab/pull/${number}`, waits_on: "live_proof" } };
}

function worker(name: string, extra: Partial<Worker> = {}): Worker {
  return {
    name, sortie: name, remote: false, last_seen: NOW, slots: 2, slots_known: true, busy: 0,
    capacity: { cpu: 32, mem_gib: 128, disk_gib: 2000 }, used: { cpu: 8, mem_gib: 20, disk_gib: 200 },
    verdict: "idle", reasons: [], lanes: [{ slot: 1, job: null, stuck: null, extra: false }, { slot: 2, job: null, stuck: null, extra: false }],
    jobs: [], stuck: [], waits: [], next: [], unmanaged: [], draining: null, ...extra,
  };
}

/** taichi runs ONS-34's live proof in slot 1 and ONS-50's question holds slot 2; p1's slot 1 is held by ONS-68's session budget. */
function fleet(): Fleet {
  const proof = job("ONS-34", "homelab/vm-chain");
  const q = stuck("ONS-50", "homelab/question", "taichi", {});
  const budget = stuck("ONS-68", "agentic/budget", "p1", { kind: "budget", reason: "used 32 of its 32 sessions", action: "sessions", slot: 1 });
  const waits = [wait("ONS-52", "homelab/node-a", 152), wait("ONS-48", "homelab/node-b", 148), wait("ONS-61", "homelab/node-c", 161)];
  return {
    generated_at: NOW,
    sorties: [{ name: "p1", reachable: true, error: null }, { name: "taichi", reachable: true, error: null }],
    workers: [
      worker("p1", { slots: 3, verdict: "stuck", stuck: [budget],
        reasons: [{ code: "budget", text: "slot 1: budget used 32 of its 32 sessions", identifier: "ONS-68", key: "agentic/budget", action: "sessions" }],
        lanes: [{ slot: 1, job: null, stuck: budget, extra: false }, { slot: 2, job: null, stuck: null, extra: false }, { slot: 3, job: null, stuck: null, extra: false }] }),
      worker("taichi", { busy: 1, verdict: "stuck", jobs: [proof], stuck: [q], waits, next: waits,
        reasons: [{ code: "question", text: `slot 2: question asks: ${QUESTION}`, identifier: "ONS-50", key: "homelab/question", action: "answer" }],
        lanes: [{ slot: 1, job: proof, stuck: null, extra: false }, { slot: 2, job: null, stuck: q, extra: false }],
        unmanaged: [{ name: "brian-scratch", since: NOW - 7200, age: 7200, age_text: "2.0 h", state: "running" }] }),
    ],
    pulls: [
      ...waits.map((w): Pull => ({ ...ref(w.identifier!, w.key!), number: w.pull!.number, repository: "ortusnova/homelab", url: w.pull!.url,
        waits_on: "live_proof", waits_text: "waits on a live proof", reason: w.reason, since: w.since, sortie: "taichi", worker: "taichi", stuck: false })),
      { ...ref("ONS-70", "agentic/ready"), number: 77, repository: "ortusnova/agentic-orchestration", url: "https://github.com/ortusnova/agentic-orchestration/pull/77",
        waits_on: "none", waits_text: "ready to merge", reason: null, since: NOW - 4000, sortie: "p1", worker: "p1", stuck: true },
    ],
  };
}

function lanesOf(f: Fleet): Lanes {
  const reasons = f.workers.flatMap((w) => w.reasons.map((r) => ({ ...r, worker: w.name, text: `${w.name} ${r.text}` })));
  return { verdict: "stuck", reasons, busy: 1, slots: 5, runway: 2, generated_at: NOW, workers: f.workers };
}

const homeQuestion: HomeQuestion = { identifier: "ONS-50", statement: QUESTION, parked_at: NOW - 900, reason: "agent blocked",
  key: "homelab/question", label: "question", change: "homelab", title: "Title homelab/question", url: null, rank: 1 };

function homeDoc(f: Fleet): HomeDoc {
  return {
    generated_at: NOW, plan: { valid: true, errors: 0, has_graph: true }, milestones_declared: true, story_progress: "live",
    health: { verdict: "stuck", reasons: [], slots: 5, busy: 1, free: 4, running: [], held: [], runway: 2 },
    milestones: [{ id: "m1", title: "M1: vm-complete", short: "M1", goal: true, next: true, done: false, implicit: false, depends_on: [], gate: null, changes: ["homelab"],
      progress: { stories: { done: 1, total: 3 }, agent_tasks: { done: 0, total: 0 }, person_tasks: { done: 0, total: 0 }, metal_tasks: { done: 0, total: 0 }, operator_tasks: { done: 0, total: 0 } } }],
    goal: "m1", next: "m1", critical_path: null, not_ready: [], questions: [homeQuestion],
    needs_person: { total: 1, questions: 1, approve: 0, approve_stories: 0, spec: 0, operator: 0, nearest: "homelab/question" },
    timeline: { axis: "stories_and_tasks", here: 0.3, to_goal: 0.3, spans: [{ id: "m1", start: 0, end: 1, size: 3, done: 1, blockers: [] }] },
    lanes: lanesOf(f),
  };
}

const homeGraph = (f: Fleet): MergedGraph => graph([synced("homelab"), synced("homelab/question")], [], { home: homeDoc(f), fleet: f });

describe("Scenario: A live proof running on taichi", () => {
  it("shows the proof job with its step and time, and the three pull requests waiting on it with the reason", async () => {
    app = mount(WorkersPage, { target: document.body, props: { fleet: fleet(), slug: "homelab" } });
    await settle();
    const row = $("[data-worker-row='taichi']")!;
    const proof = row.querySelector<HTMLElement>("[data-job='ONS-34']")!;
    expect(proof.textContent).toContain("vm-chain");
    expect(proof.textContent).toContain("live proof");
    expect(proof.querySelector("[data-step]")!.textContent).toContain("seed-vm-recreate");
    expect(proof.textContent).toContain("25 min");
    const waiting = [...row.querySelectorAll<HTMLElement>("[data-wait='live_proof']")];
    expect(waiting.map((w) => w.dataset.waitFor)).toEqual(["ONS-52", "ONS-48", "ONS-61"]);
    for (const w of waiting) expect(w.textContent).toContain("waits for a live proof: its live proof needs a working vm chain");
    expect(waiting[0].querySelector("a[href='https://github.com/ortusnova/homelab/pull/152']")).not.toBeNull();
    expect($$("[data-worker-row='taichi'] [data-capacity] li").map((li) => li.textContent)).toEqual(["1 / 2 slots", "8 / 32 CPU", "20 / 128 GiB memory", "200 / 2000 GiB disk"]);
  });

  it("updates in place when the job reports its next step", async () => {
    const h = mount(WorkersHarness, { target: document.body, props: { initial: fleet() } }) as ReturnType<typeof mount> & { setFleet: (f: Fleet) => void };
    app = h;
    await settle();
    const next = fleet();
    next.workers[1].jobs[0] = { ...next.workers[1].jobs[0], step: "seed-vm-boot" };
    h.setFleet(next);
    await settle();
    expect(text("[data-worker-row='taichi'] [data-job='ONS-34'] [data-step]")).toContain("seed-vm-boot");
  });
});

describe("Scenario: A pull request with nothing left to do", () => {
  it("reads ready to merge, and stuck when it stays so", async () => {
    app = mount(WorkersPage, { target: document.body, props: { fleet: fleet(), slug: "agentic" } });
    await settle();
    const pr = $("[data-pull-row='ONS-70']")!;
    expect(pr.dataset.waitsOn).toBe("none");
    expect(pr.querySelector("[data-waits]")!.textContent).toContain("ready to merge");
    expect(pr.querySelector("[data-waits]")!.textContent).toContain("stuck: nothing merges it");
    expect(text("[data-pull-row='ONS-52'] [data-waits]")).toBe("waits on a live proof");
  });
});

describe("Scenario: The home page's top row", () => {
  it("shows the workers' slot lanes below the milestone timeline, and none of the four tiles", async () => {
    app = mount(HomeHarness, { target: document.body, props: { initial: homeGraph(fleet()) } });
    await settle();
    const score = $(".score")!;
    const lanes = $(".lanes-home")!;
    expect(score.compareDocumentPosition(lanes) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect($$("[data-tile]")).toHaveLength(0);
    for (const tile of ["Execution", "Agent slots", "Runway"]) expect($$("h3").map((h) => h.textContent)).not.toContain(tile);
    // One lane per agent slot of each worker.
    expect($$("[data-worker='p1'] [data-lane]")).toHaveLength(3);
    expect($$("[data-worker='taichi'] [data-lane]")).toHaveLength(2);
    // The verdict and its reasons, the busy slots, and what runs next; no count of what needs a person.
    expect(text("[data-verdict] .big")).toBe("Stuck");
    expect(text("[data-summary]")).toBe("1 of 5 slots busy · 2 stories ready");
    expect(text("[data-verdict] .reasons")).toContain(`taichi slot 2: question asks: ${QUESTION}`);
    expect(text("[data-worker='taichi'] [data-next]")).toContain("node-a (its live proof needs a working vm chain)");
    expect(text(".lanes-home")).not.toContain("Needs a person");
  });

  it("reads a stuck slot as stuck in its own lane, with why, and a running job as a box from its start to its end", async () => {
    app = mount(HomeHarness, { target: document.body, props: { initial: homeGraph(fleet()) } });
    await settle();
    const lane = $("[data-worker='taichi'] [data-lane='2']")!;
    expect(lane.dataset.stuck).toBe("true");
    expect(lane.getAttribute("aria-label")).toBe(`slot 2: stuck, question asks: ${QUESTION}`);
    expect(text("[data-worker='taichi'] [data-why='question']")).toContain(`Stuck: question asks: ${QUESTION}`);
    expect($("[data-worker='taichi']")!.dataset.workerVerdict).toBe("stuck");
    const box = $("[data-worker='taichi'] [data-job='ONS-34']")!;
    expect(box.textContent).toContain("live proof · seed-vm-recreate · 25 min");
    expect(box.style.left).not.toBe("");
    expect(parseFloat(box.style.width)).toBeGreaterThan(5);
  });
});

describe("Scenario: Unblock from the lane", () => {
  it("Answer in taichi's lane opens ONS-50's question with an answer box, and sends the answer", async () => {
    const answers: [string, string][] = [];
    app = mount(HomeHarness, { target: document.body, props: { initial: homeGraph(fleet()), onanswer: async (k: string, t: string) => { answers.push([k, t]); return null; } } });
    await settle();
    expect($("[data-worker='taichi'] [data-resolve]")).toBeNull();
    $<HTMLButtonElement>("[data-worker='taichi'] [data-why='question'] [data-act='answer']")!.click();
    await settle();
    const box = $("[data-worker='taichi'] [data-resolve='answer']")!;
    expect(box.textContent).toContain(QUESTION);
    box.querySelector<HTMLTextAreaElement>("textarea")!.value = "the NVMe";
    box.querySelector<HTMLTextAreaElement>("textarea")!.dispatchEvent(new Event("input"));
    await settle();
    box.querySelector<HTMLFormElement>("form")!.requestSubmit();
    await settle();
    expect(answers).toEqual([["homelab/question", "the NVMe"]]);
  });

  it("p1's lane offers more sessions for ONS-68, and grants them", async () => {
    const grants: [string, number][] = [];
    app = mount(HomeHarness, { target: document.body, props: { initial: homeGraph(fleet()), onsessions: async (k: string, n: number) => { grants.push([k, n]); return null; } } });
    await settle();
    $<HTMLButtonElement>("[data-worker='p1'] [data-why='budget'] [data-act='sessions']")!.click();
    await settle();
    $<HTMLFormElement>("[data-worker='p1'] [data-resolve='sessions'] form")!.requestSubmit();
    await settle();
    expect(grants).toEqual([["agentic/budget", 4]]);
    expect(text("[role='status']")).toContain("4 more sessions granted to budget");
  });

  it("a planner sees the stuck lane but cannot grant", async () => {
    identity.identity = { name: "Pat", email: "", roles: ["planner"], id: "p" };
    app = mount(HomeHarness, { target: document.body, props: { initial: homeGraph(fleet()) } });
    await settle();
    expect($<HTMLButtonElement>("[data-worker='p1'] [data-why='budget'] [data-act='sessions']")!.disabled).toBe(true);
  });

  it("Open on a running job links to that agent's session, which the graph opens", async () => {
    app = mount(HomeHarness, { target: document.body, props: { initial: homeGraph(fleet()) } });
    await settle();
    const open = $<HTMLAnchorElement>("[data-worker='taichi'] [data-act='open']")!;
    expect(open.getAttribute("href")).toBe(watchHref("homelab", "homelab/vm-chain", "ONS-34"));
    const route = parseRoute(open.getAttribute("href")!);
    expect(route.name === "project" && route.page === "graph" && route.view.watch === "ONS-34" && route.view.selected === "homelab/vm-chain").toBe(true);
  });
});

class FakeWorker implements WorkerClient {
  sent: Record<string, unknown>[] = [];
  h: WorkerHandlers | null = null;
  async messages() { return { commands: [] }; }
  async command(cmd: Record<string, unknown>) { this.sent.push(cmd); return {}; }
  subscribe(h: WorkerHandlers) { this.h = h; return () => {}; }
}

describe("Open: the agent's session", () => {
  const vmNode = (): GraphNode => synced("homelab/vm-chain", "In Progress", {}, { status: "running", watchable: true, session_id: "s34", stage: "build" });

  it("while it runs, the operator sends it a message", async () => {
    const w = new FakeWorker();
    app = mount(WorkerPane, { target: document.body, props: { node: vmNode(), client: w, onclose: () => {} } });
    await settle();
    w.h!.onRun({ identifier: "ONS-34", session_id: "s34", stage: "build", follow: false });
    await settle();
    const area = $<HTMLTextAreaElement>("textarea")!;
    area.value = "keep the old disk";
    area.dispatchEvent(new Event("input"));
    await settle();
    $<HTMLFormElement>("form.composer")!.requestSubmit();
    await settle();
    expect(w.sent).toEqual([{ op: "steer", text: "keep the old disk" }]);
  });

  it("after it ran, shows its last transcript", async () => {
    const w = new FakeWorker();
    app = mount(WorkerPane, { target: document.body, props: { node: vmNode(), client: w, onclose: () => {} } });
    await settle();
    w.h!.onTranscript!({ identifier: "ONS-34", dispatch_id: "d-34", sortie: "taichi",
      events: [{ seq: 1, at: "2026-11-02T18:00:00Z", event: "notification", text: "seed vm recreated" }] });
    w.h!.onEnded();
    await settle();
    expect(document.body.textContent).toContain("Its last session, as taichi's Sortie kept it:");
    expect(document.body.textContent).toContain("seed vm recreated");
    expect(document.body.textContent).toContain("That was the last session.");
  });
});

describe("A story's panel: its run state and the action that unblocks it", () => {
  const noSession: SessionClient = {
    status: async () => ({ active: false }), start: async () => ({ active: true }), stop: async () => ({ active: false }),
    command: async () => ({}), subscribe: () => () => {},
    config: async () => ({ options: [], ready: true }), setConfig: async () => ({ options: [], ready: true }),
  };

  function panel(n: GraphNode, actions: Partial<StoryActions> = {}, onwatch?: (n: GraphNode) => void) {
    const doc = graph([synced("homelab"), n]);
    app = mount(ChatHarness, { target: document.body, props: { initial: doc, session: noSession, actions: actions as StoryActions, onwatch,
      initialView: { epic: "homelab", selected: n.plan_key, showCross: true } } });
  }

  it("says why a story waits, what releases it, and what its pull request waits on", async () => {
    panel(synced("homelab/node-a", "In Review", { run_state: { status: "waiting", worker: "taichi", sortie: "taichi", slot: null,
      why: "its live proof needs a working vm chain", release: "the live proof running here passes", kind: "live_proof", action: "pull",
      phase: null, step: null, pull: { number: 152, repository: "ortusnova/homelab", url: "https://github.com/ortusnova/homelab/pull/152",
        waits_on: "live_proof", waits_text: "waits on a live proof", reason: "its live proof needs a working vm chain", stuck: false } } }));
    await settle();
    const r = $("[data-run-state='waiting']")!;
    expect(r.textContent).toContain("Waiting on taichi");
    expect(text("[data-run-state] [data-why]")).toBe("its live proof needs a working vm chain");
    expect(text("[data-run-state] [data-release]")).toBe("Released by the live proof running here passes.");
    expect(text("[data-run-pull]")).toContain("#152 waits on a live proof");
    expect($<HTMLAnchorElement>("[data-run-state] [data-act='pull']")!.href).toBe("https://github.com/ortusnova/homelab/pull/152");
  });

  it("a story its session budget holds offers more sessions there", async () => {
    const grants: [string, number][] = [];
    panel(synced("homelab/budget", "In Progress", { run_state: { status: "stuck", worker: "p1", sortie: "p1", slot: 1,
      why: "used 32 of its 32 sessions", release: "more sessions", kind: "budget", action: "sessions", phase: "build", step: null, pull: null } }),
    { sessions: async (k: string, n: number) => { grants.push([k, n]); return {}; } });
    await settle();
    expect(text("[data-run-state='stuck'] b")).toBe("Stuck on p1, slot 1");
    $<HTMLFormElement>("[data-run-state] form")!.requestSubmit();
    await settle();
    expect(grants).toEqual([["homelab/budget", 4]]);
    expect(text("[data-run-state] [role='status']")).toContain("p1 Sortie runs it again");
  });

  it("a running story opens its agent's session", async () => {
    const opened: string[] = [];
    panel(synced("homelab/vm-chain", "In Progress", { run_state: { status: "running", worker: "taichi", sortie: "taichi", slot: 1,
      why: "live proof runs on taichi, at seed-vm-recreate", release: null, kind: null, action: "open", phase: "live_proof", step: "seed-vm-recreate", pull: null } }),
    {}, (n) => opened.push(n.plan_key));
    await settle();
    opened.length = 0;
    $<HTMLButtonElement>("[data-run-state] [data-act='open']")!.click();
    expect(opened).toEqual(["homelab/vm-chain"]);
  });
});

describe("Scenario: A machine Sortie did not start", () => {
  it("shows it on taichi's row with its name and age", async () => {
    app = mount(WorkersPage, { target: document.body, props: { fleet: fleet(), slug: "homelab" } });
    await settle();
    expect(text("[data-worker-row='taichi'] [data-unmanaged='brian-scratch']")).toBe("brian-scratch running, for 2.0 h");
    expect($("[data-worker-row='p1'] [data-unmanaged]")).toBeNull();
  });
});

describe("the lanes' geometry and words", () => {
  it("places a box from its start to its end in the window, clipped to it", () => {
    expect(boxGeometry(50, 75, { start: 0, end: 100 })).toEqual({ left: 50, width: 25 });
    expect(boxGeometry(-50, 10, { start: 0, end: 100 })).toEqual({ left: 0, width: 10 });
    expect(boxGeometry(90, 400, { start: 0, end: 100 }).width).toBe(10);
  });
  it("says a duration in words", () => {
    expect([duration(40), duration(1500), duration(7200), duration(null)]).toEqual(["40 s", "25 min", "2.0 h", "—"]);
  });
});
