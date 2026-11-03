import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, describe, expect, it } from "vitest";
import Harness from "./HomeHarness.svelte";
import { graph, synced } from "./fixtures";
import {
  changedStories, mapShare, slackText, slackWidth, visualSpans,
  type HomeDoc, type HomeMilestone, type NotReadyItem,
} from "../src/lib/home";
import { parseRoute } from "../src/lib/router";
import type { MergedGraph } from "../src/lib/types";

// jsdom has no ResizeObserver; the timeline measures its width with one.
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;

type H = { setDoc: (d: MergedGraph) => void; mark: (keys: string[]) => void };
let app: (ReturnType<typeof mount> & H) | undefined;
afterEach(() => {
  if (app) unmount(app);
  app = undefined;
  document.body.innerHTML = "";
});

const settle = async () => { for (let i = 0; i < 5; i++) { await tick(); await Promise.resolve(); } flushSync(); };
const $ = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);
const $$ = (sel: string) => [...document.querySelectorAll<HTMLElement>(sel)];
const text = (sel: string) => $(sel)?.textContent?.replace(/\s+/g, " ").trim() ?? "";

function milestone(id: string, short: string, extra: Partial<HomeMilestone> = {}): HomeMilestone {
  const c = (done: number, total: number) => ({ done, total });
  return {
    id, title: `${short}: ${id}`, short, goal: false, next: false, done: false, implicit: false, depends_on: [], gate: null, changes: [id],
    progress: { stories: c(0, 0), agent_tasks: c(0, 0), person_tasks: c(0, 0), metal_tasks: c(0, 0), operator_tasks: c(0, 0) },
    ...extra,
  };
}

function item(key: string, extra: Partial<NotReadyItem> = {}): NotReadyItem {
  const story = key.includes("/");
  return {
    kind: story ? "story" : "change", key, label: story ? key.split("/")[1] : key, title: `Do ${key}`, change: key.split("/")[0],
    milestone: "m1", reason: "not_approved", column: "approve", slack: 0, urgency: "now", critical: false, dependents: 0, order: 0,
    rank: 1, after: [], action: { kind: "approve", keys: [key] }, ...extra,
  };
}

/** The homelab plan in miniature: M0 done, M1 a third done and next, M2 the goal. */
function homeDoc(extra: Partial<HomeDoc> = {}): HomeDoc {
  const items: NotReadyItem[] = [
    item("apw/worker-daemon", { rank: 1, critical: true }),
    item("apw/homelab-instance", { rank: 2, reason: "no_spec", column: "spec", slack: 1, urgency: "soon", critical: true,
      after: ["apw/control-plane-images"], action: { kind: "plan", story: "apw/homelab-instance" } }),
    item("rhts/nested-opt-in", { rank: 3, reason: "no_spec", column: "spec", slack: 4, urgency: "soon", action: { kind: "plan", story: "rhts/nested-opt-in" } }),
    item("p3#3.7", { kind: "task", rank: 4, label: "3.7", title: "Enroll the **YubiKey**, declare `p1` a mgmt peer.", milestone: "m2",
      reason: "operator_task", column: "operator", slack: 39, urgency: "later", change: "p3", after_milestones: ["M1"],
      task: { id: "p3#3.7", change: "p3", number: "3.7", text: "Enroll", kind: "operator", line: 4 }, action: { kind: "task", epic: "p3", task: "p3#3.7" } }),
    item("trust", { kind: "change", rank: 5, stories: ["trust/a", "trust/b", "trust/c"], slack: 2, urgency: "soon",
      action: { kind: "review", epic: "trust", keys: ["trust/a", "trust/b", "trust/c"] } }),
    item("apw/later", { rank: null, reason: "waits_on_work", column: null, slack: 3, urgency: "soon", action: { kind: "plan", story: "apw/later" } }),
  ];
  return {
    generated_at: 0, version: 3,
    plan: { valid: true, errors: 0, has_graph: true }, milestones_declared: true, story_progress: "live",
    health: { verdict: "idle_now", reasons: [{ code: "idle", text: "3 agent slots are free and 0 stories are ready" }],
      slots: 3, busy: 0, free: 3, running: [], held: [], runway: 0,
      history: [{ end: 1, busy: 2, runway: 1 }, { end: 2, busy: 0, runway: 0 }] },
    milestones: [
      milestone("m0", "M0", { done: true }),
      milestone("m1", "M1", { next: true, gate: "apw/gate",
        progress: { stories: { done: 7, total: 21 }, agent_tasks: { done: 101, total: 151 }, person_tasks: { done: 0, total: 3 },
          metal_tasks: { done: 0, total: 0 }, operator_tasks: { done: 0, total: 3 } } }),
      milestone("m2", "M2", { goal: true,
        progress: { stories: { done: 0, total: 0 }, agent_tasks: { done: 0, total: 0 }, person_tasks: { done: 0, total: 40 },
          metal_tasks: { done: 0, total: 40 }, operator_tasks: { done: 0, total: 0 } } }),
    ],
    goal: "m2", next: "m1",
    critical_path: { milestone: "m1", gate: "apw/gate", stories: [
      { key: "apw/worker-daemon", label: "worker-daemon", title: "Worker daemon", change: "apw", needs: "not_approved", running: false, estimate: null },
      { key: "apw/gate", label: "gate", title: "The gate", change: "apw", needs: "waits_on_work", running: false, estimate: null },
    ] },
    not_ready: items,
    needs_person: { total: 5, approve: 2, approve_stories: 4, spec: 2, operator: 1, nearest: "apw/worker-daemon" },
    timeline: { axis: "stories_and_tasks", here: 0.5, to_goal: 0.31, spans: [
      { id: "m0", start: 0, end: 0.25, size: 10, done: 10, blockers: [] },
      { id: "m1", start: 0.25, end: 1, size: 30, done: 10, blockers: [
        { key: "apw/worker-daemon", label: "worker-daemon", reason: "not_approved", rank: 1 },
        { key: "rhts/nested-opt-in", label: "nested-opt-in", reason: "no_spec", rank: 3 },
      ] },
      { id: "m2", start: 1, end: 1, size: 0, done: 0, blockers: [{ key: "p3#3.7", label: "3.7", reason: "operator_task", rank: 4 }] },
    ] },
    ...extra,
  };
}

function doc(home: HomeDoc | null, extra: Partial<MergedGraph> = {}): MergedGraph {
  return graph([synced("apw"), synced("apw/worker-daemon")], [], { home, ...extra });
}

async function render(d: MergedGraph, onapprove?: (keys: string[]) => Promise<string | null>) {
  app = mount(Harness, { target: document.body, props: { initial: d, ...(onapprove ? { onapprove } : {}) } }) as typeof app;
  await settle();
  return app!;
}

describe("home: the timeline", () => {
  it("draws a labelled tick per milestone, the goal marked, and we-are-here at the share done", async () => {
    await render(doc(homeDoc()));
    expect($$("[data-milestone]").map((e) => e.dataset.milestone)).toEqual(["m0", "m1", "m2"]);
    const goal = $("[data-milestone='m2']")!;
    expect(goal.classList.contains("goal")).toBe(true);
    expect(goal.textContent).toContain("goal");
    expect(text("[data-milestone='m1'] .cnt")).toContain("7 / 21 stories");
    expect(text("[data-milestone='m1'] .cnt")).toContain("101 / 151 agent tasks");
    expect(text("[data-milestone='m2'] .cnt")).toContain("0 / 40 person tasks");
    expect(text("[data-milestone='m0'] .cnt")).toBe("done");
    expect(text("h1")).toContain("M2 is the goal · 31% of the work to it is done");
  });

  it("places we-are-here a third of the way into M1 when M1 is a third done", () => {
    const spans = [
      { id: "m0", start: 0, end: 0.2, size: 1, done: 1, blockers: [] },
      { id: "m1", start: 0.2, end: 0.8, size: 3, done: 1, blockers: [] },
      { id: "m2", start: 0.8, end: 1, size: 1, done: 0, blockers: [] },
    ];
    const v = visualSpans(spans);
    const x = mapShare(0.4, spans, v);
    expect((x - v[1].x0) / (v[1].x1 - v[1].x0)).toBeCloseTo(1 / 3);
  });

  it("gives every span room for its label, and keeps the scale whole", () => {
    const v = visualSpans([
      { id: "a", start: 0, end: 0.01, size: 1, done: 0, blockers: [] },
      { id: "b", start: 0.01, end: 0.99, size: 98, done: 0, blockers: [] },
      { id: "c", start: 0.99, end: 1, size: 1, done: 0, blockers: [] },
    ], 0.1);
    expect(v[0].x1 - v[0].x0).toBeCloseTo(0.1);
    expect(v[2].x1 - v[2].x0).toBeCloseTo(0.1);
    expect(v[2].x1).toBeCloseTo(1);
  });

  it("marks each blocker that needs a person, named on focus, in its milestone", async () => {
    await render(doc(homeDoc()));
    const cross = $("[data-blocker='rhts/nested-opt-in']")!;
    expect(cross.getAttribute("aria-label")).toBe("Blocker in M1: rhts/nested-opt-in, no spec");
    expect(cross.textContent).toContain("rhts/nested-opt-in · no spec");
    expect(cross.tagName).toBe("A");
    expect(parseRoute(cross.getAttribute("href")!)).toMatchObject({ page: "graph", view: { epic: "rhts", selected: "rhts/nested-opt-in" } });
    expect($("[data-blocker='p3#3.7']")!.getAttribute("aria-label")).toContain("Blocker in M2");
  });
});

describe("home: the focal points", () => {
  it("shows the verdict in words, with its reasons, the slots and the runway, and no tiles", async () => {
    await render(doc(homeDoc()));
    expect($("[data-verdict]")!.dataset.verdict).toBe("idle_now");
    expect(text("[data-verdict] .big")).toBe("Idle now");
    expect(text("[data-verdict] .reasons")).toBe("3 agent slots are free and 0 stories are ready");
    expect(text("[data-summary]")).toBe("0 of 3 slots busy");
    // The four tiles are gone, and the count of what needs a person is the section's.
    expect($$("[data-tile]")).toHaveLength(0);
    expect($("[aria-label='Execution health']")).toBeNull();
    expect(text("#needs-h")).toContain("Needs a person");
  });

  it("shows the critical path to the next milestone's gate, each story with what it needs", async () => {
    await render(doc(homeDoc()));
    expect(text(".path h2")).toContain("to the M1 gate");
    const nodes = $$(".chain .node");
    expect(nodes.map((n) => [n.querySelector(".mono")!.textContent, n.querySelector("b")!.textContent])).toEqual([["not approved", "worker-daemon"], ["gate", "gate"]]);
    expect(nodes[0].classList.contains("need")).toBe(true);
  });

  it("ranks the items that need a person across three columns, with slack in a bar and in words", async () => {
    await render(doc(homeDoc()));
    const col = (c: string) => $$(`[data-column='${c}'] .item`).map((e) => [e.dataset.key, e.dataset.rank]);
    expect(col("approve")).toEqual([["apw/worker-daemon", "1"], ["trust", "5"]]);
    expect(col("spec")).toEqual([["apw/homelab-instance", "2"], ["rhts/nested-opt-in", "3"]]);
    expect(col("operator")).toEqual([["p3#3.7", "4"]]);
    expect($$(".item.hot").map((e) => e.dataset.rank)).toEqual(["1", "2", "3"]);
    const wd = $("[data-key='apw/worker-daemon']")!;
    expect(wd.querySelector(".sl")!.textContent).toBe("blocking now");
    expect(wd.querySelector(".sl")!.className).toContain("u-now-text");
    expect(wd.querySelector(".crit")!.textContent).toBe("critical");
    expect(text("[data-key='apw/homelab-instance'] .sl")).toBe("blocks in 1 story");
    expect(text("[data-key='apw/homelab-instance'] .note")).toBe("apw · after control-plane-images");
    expect(text("[data-key='p3#3.7'] .sl")).toBe("blocks after M1, ~39 stories");
    expect(text("[data-key='p3#3.7'] .name")).toBe("Enroll the YubiKey, declare p1 a mgmt peer.");
    expect(text("[data-key='trust'] .btn")).toBe("Review 3");
    expect(text("[data-key='trust'] .note")).toBe("a, b, c · reviewed together");
    expect(text("[data-waits-on-work]")).toContain("1 more story waits on work only");
  });

  it("acts on each item: approve in place, or plan in the graph at the item", async () => {
    const approved: string[][] = [];
    await render(doc(homeDoc()), async (keys) => { approved.push(keys); return keys[0] === "trust/a" ? "Approving needs the approver role." : null; });
    $<HTMLButtonElement>("[data-key='apw/worker-daemon'] button.btn")!.click();
    await settle();
    expect(approved).toEqual([["apw/worker-daemon"]]);
    const plan = $<HTMLAnchorElement>("[data-key='apw/homelab-instance'] a.btn")!;
    expect(plan.textContent).toBe("Plan in graph");
    expect(parseRoute(plan.getAttribute("href")!)).toMatchObject({ page: "graph", view: { epic: "apw", selected: "apw/homelab-instance" } });
    const task = $<HTMLAnchorElement>("[data-key='p3#3.7'] a.btn")!;
    expect(task.textContent).toBe("Open task");
    expect(parseRoute(task.getAttribute("href")!)).toMatchObject({ view: { epic: "p3" } });
  });

  it("says what an approval that failed needs", async () => {
    await render(doc(homeDoc()), async () => "Approving needs the approver role.");
    $<HTMLButtonElement>("[data-key='apw/worker-daemon'] button.btn")!.click();
    await settle();
    expect(text("[data-key='apw/worker-daemon'] [role=alert]")).toBe("Approving needs the approver role.");
  });

  it("shows five items a column until asked for all", async () => {
    const many = Array.from({ length: 8 }, (_, i) => item(`apw/s${i}`, { rank: i + 1 }));
    await render(doc(homeDoc({ not_ready: many })));
    expect($$("[data-column='approve'] .item")).toHaveLength(5);
    $<HTMLButtonElement>("[data-column='approve'] .more")!.click();
    await settle();
    expect($$("[data-column='approve'] .item")).toHaveLength(8);
  });

  it("shows each milestone's measures apart, and marks the next and the goal", async () => {
    await render(doc(homeDoc()));
    const row = (id: string) => [...$(`[data-milestone-row='${id}']`)!.querySelectorAll("td")]
      .map((c) => [...c.childNodes].map((x) => x.textContent?.trim()).filter(Boolean).join(" | "));
    expect(row("m1")).toEqual(["7 / 21", "101 / 151", "0 / 3 | 0 metal · 3 operator", "next"]);
    expect(row("m2")).toEqual(["—", "—", "0 / 40 | 40 metal · 0 operator", "ahead | goal"]);
    const link = $<HTMLAnchorElement>("[data-milestone-row='m1'] a")!;
    expect(parseRoute(link.getAttribute("href")!)).toMatchObject({ page: "graph", view: { mark: "m1", epic: null } });
  });
});

describe("home: honest when sources fail", () => {
  it("says the plan is invalid and shows the last valid plan", async () => {
    await render(doc(homeDoc({ plan: { valid: false, errors: 2, has_graph: true } })));
    expect(text("[data-notice='invalid']")).toContain("The plan has 2 problems; this page shows the last valid plan.");
    expect($$("[data-milestone]")).toHaveLength(3);
  });

  it("says Sortie is unreachable, keeps the tasks, and marks story progress as last known", async () => {
    const h = homeDoc({ story_progress: "last_known" });
    h.health = { ...h.health, verdict: "stuck", reasons: [{ code: "sortie_unreachable", text: "Sortie is unreachable: refused" }], slots: null, busy: null, free: null, runway: null };
    await render(doc(h, { sortie: { reachable: false, error: "refused", capabilities: [], generated_at: null } }));
    expect(text("[data-notice='sortie']")).toContain("story progress is the last known");
    expect(text("[data-verdict] .big")).toBe("Stuck");
    expect(text("[data-verdict] .reasons")).toBe("Sortie is unreachable: refused");
    expect(text("[data-milestone='m1'] .cnt")).toContain("stories last known");
    expect(text("[data-milestone='m1'] .cnt")).toContain("101 / 151 agent tasks");
  });

  it("without a milestones file, shows one milestone and where to declare them", async () => {
    const h = homeDoc({ milestones_declared: false, goal: "project", next: "project" });
    h.milestones = [milestone("project", "The plan", { goal: true, next: true, implicit: true })];
    h.timeline = { axis: "stories_and_tasks", here: 0, to_goal: 0, spans: [{ id: "project", start: 0, end: 1, size: 1, done: 0, blockers: [] }] };
    await render(doc(h));
    expect(text("[data-notice='no-milestones']")).toContain("openspec/milestones.yaml");
    expect($$("[data-milestone]")).toHaveLength(1);
  });

  it("when everything is done, says so", async () => {
    const h = homeDoc({ next: null, critical_path: null, not_ready: [] });
    h.health = { ...h.health, verdict: "done", reasons: [{ code: "done", text: "Every planned story is done" }] };
    h.milestones = h.milestones.map((m) => ({ ...m, done: true, next: false }));
    await render(doc(h));
    expect(text("[data-verdict] .big")).toBe("Done");
    expect(text("h1")).toContain("the goal is reached");
    expect($(".path")).toBeNull();
    expect(text("[data-column='approve'] .none")).toBe("Nothing to approve.");
  });

  it("shows no verdict its sources cannot support", async () => {
    const h = homeDoc();
    h.health = { ...h.health, verdict: null, reasons: [{ code: "not_dispatched", text: "Sortie runs agents for its configured project only, not T" }] };
    await render(doc(h));
    expect(text("[data-verdict] .big")).toBe("No verdict");
  });

  it("before any valid plan, shows an empty state", async () => {
    await render(doc(null));
    expect(text(".empty")).toContain("Nothing to show yet");
  });
});

describe("home: live", () => {
  it("finds the stories whose state changed between two graphs", () => {
    const before = graph([synced("apw/a", "Todo"), synced("apw/b", "Todo")]);
    const after = graph([synced("apw/a", "Done"), synced("apw/b", "Todo")]);
    const first = changedStories(new Map(), before.nodes);
    expect(first.changed).toEqual([]);
    expect(changedStories(first.now, after.nodes).changed).toEqual(["apw/a"]);
  });

  it("marks a changed story with words, not with color or motion alone", async () => {
    const a = await render(doc(homeDoc()));
    a.mark(["apw/worker-daemon"]);
    await settle();
    const wd = $("[data-key='apw/worker-daemon']")!;
    expect(wd.classList.contains("moved")).toBe(true);
    expect(wd.querySelector(".upd")!.textContent).toBe("updated");
    expect($(".chain .node.moved")).not.toBeNull();
    expect($("[data-blocker='apw/worker-daemon']")!.classList.contains("moved")).toBe(true);
  });

  it("updates in place when a new home arrives", async () => {
    const a = await render(doc(homeDoc()));
    const h = homeDoc();
    h.health = { ...h.health, verdict: "healthy", reasons: [{ code: "enough", text: "A runway of 4 stories against 3 agent slots" }], busy: 3, runway: 4 };
    a.setDoc(doc(h));
    await settle();
    expect(text("[data-verdict] .big")).toBe("Healthy");
    expect(text("[data-summary]")).toBe("3 of 3 slots busy");
  });

  it("says every slack in words beside its bar, shorter for less slack", () => {
    expect(slackText(item("a/b", { slack: 0 }))).toBe("blocking now");
    expect(slackText(item("a/b", { slack: 2 }))).toBe("blocks in 2 stories");
    expect(slackWidth(0)).toBeLessThan(slackWidth(1));
    expect(slackWidth(1)).toBeLessThan(slackWidth(4));
    expect(slackWidth(40)).toBe(100);
  });
});

describe("home: the stories milestone work waits on", () => {
  const RDW = "record-delivered-work";
  const by = `${RDW}/delivered-by`;

  /** homelab-pilot is in M1 and depends on delivered-by, whose change is in no milestone. */
  function waitedDoc(): HomeDoc {
    const base = homeDoc();
    const m1 = base.milestones[1];
    return {
      ...base,
      milestones: [base.milestones[0], {
        ...m1, changes: [...m1.changes, RDW], waits_on: [by],
        progress: { ...m1.progress, stories: { done: 7, total: 22 } },
      }, base.milestones[2]],
      critical_path: { milestone: "m1", gate: "rhts/homelab-pilot", stories: [
        { key: by, label: "delivered-by", title: "Delivered by", change: RDW, needs: "not_approved", running: false, estimate: null, waited_on: true },
        { key: "rhts/homelab-pilot", label: "homelab-pilot", title: "The pilot", change: "rhts", needs: "waits_on_work", running: false, estimate: null, waited_on: false },
      ] },
      not_ready: [item(by, { rank: 1, critical: true, after: [] }), ...base.not_ready.filter((i) => i.key !== "apw/worker-daemon")],
    };
  }

  it("lists a dependency outside every milestone with M1's work, under its own change", async () => {
    await render(doc(waitedDoc()));
    const row = $(`[data-key='${by}']`)!;
    expect(row.closest("[data-column]")!.getAttribute("data-column")).toBe("approve");
    expect(text(`[data-key='${by}'] .name`)).toBe("delivered-by");
    expect(text(`[data-key='${by}'] .note`)).toBe(`${RDW} · not approved`);
    const link = $<HTMLAnchorElement>(`[data-key='${by}'] .name`)!;
    expect(parseRoute(link.getAttribute("href")!)).toMatchObject({ page: "graph", view: { epic: RDW, selected: by } });
  });

  it("names it with its own change on the critical path", async () => {
    await render(doc(waitedDoc()));
    const nodes = $$(".chain .node");
    expect(nodes.map((n) => n.querySelector("b")!.textContent)).toEqual(["delivered-by", "homelab-pilot"]);
    expect(nodes[0].querySelector("[data-change]")!.textContent).toBe(RDW);
    expect(nodes[1].querySelector("[data-change]")).toBeNull();
  });

  it("counts it in M1's story total", async () => {
    await render(doc(waitedDoc()));
    expect(text("[data-milestone-row='m1'] td")).toBe("7 / 22");
    expect(text("[data-milestone='m1'] .cnt")).toContain("7 / 22 stories");
  });
});
