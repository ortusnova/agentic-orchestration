import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import ChatHarness from "./ChatHarness.svelte";
import GraphHarness from "./GraphHarness.svelte";
import HomeHarness from "./HomeHarness.svelte";
import { graph, synced } from "./fixtures";
import ProjectView from "../src/components/ProjectView.svelte";
import type { HomeDoc, HomeQuestion, NotReadyItem } from "../src/lib/home";
import { session as identity } from "../src/lib/identity.svelte";
import { pageTitle, questionsLabel, stoppedAt } from "../src/lib/questions";
import type { SessionClient } from "../src/lib/session";
import type { StoryActions } from "../src/lib/sync";
import type { GraphNode, MergedGraph, Question, ViewState } from "../src/lib/types";

// jsdom has no ResizeObserver; the timeline measures its width with one.
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;

const KEY = "apw/worker-daemon";
const STATEMENT = "ONS-25 was dispatched into `ortusnova/sortie`, but its acceptance criteria say the daemon lives in agentic-orchestration.\n\n"
  + "Please either record agentic-orchestration on ONS-25 and re-dispatch it, or confirm that the daemon should be built in ortusnova/sortie instead.";
const PARKED_AT = Date.parse("2026-11-01T18:35:26Z") / 1000;

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
const $$ = (sel: string) => [...document.querySelectorAll<HTMLElement>(sel)];
const text = (sel: string) => $(sel)?.textContent?.replace(/\s+/g, " ").trim() ?? "";
const flat = (s: string) => s.replace(/`/g, "").replace(/\s+/g, " ").trim();
const approver = () => (identity.identity = { name: "Ann Approver", email: "", roles: ["planner", "approver"], id: "a" });
const planner = () => (identity.identity = { name: "Pat Planner", email: "", roles: ["planner"], id: "p" });

const question = (extra: Partial<Question> = {}): Question =>
  ({ identifier: "ONS-25", statement: STATEMENT, parked_at: PARKED_AT, reason: "agent blocked", ...extra });

function homeQuestion(): HomeQuestion {
  return { ...question(), key: KEY, label: "worker-daemon", change: "apw", title: "The worker daemon", url: null, rank: 1 };
}

function item(key: string, rank: number): NotReadyItem {
  return {
    kind: "story", key, label: key.split("/")[1], title: `Do ${key}`, change: key.split("/")[0], milestone: "m1",
    reason: "not_approved", column: "approve", slack: 0, urgency: "now", critical: false, dependents: 0, order: 0,
    rank, after: [], action: { kind: "approve", keys: [key] },
  };
}

function homeDoc(questions: HomeQuestion[]): HomeDoc {
  const asks = questions.map((q) => ({ code: "question", text: `${q.key} asks: ${q.statement}`, key: q.key }));
  return {
    generated_at: 0, plan: { valid: true, errors: 0, has_graph: true }, milestones_declared: true, story_progress: "live",
    health: { verdict: asks.length ? "stuck" : "idle_now", reasons: asks.length ? asks : [{ code: "idle", text: "3 agent slots are free and 0 stories are ready" }],
      slots: 3, busy: 0, free: 3, running: [], held: [], runway: 0 },
    milestones: [], goal: null, next: null, critical_path: null, timeline: null,
    not_ready: [item("apw/other", questions.length + 1)],
    questions,
    needs_person: { total: questions.length + 1, questions: questions.length, approve: 1, approve_stories: 1, spec: 0, operator: 0,
      nearest: questions[0]?.key ?? "apw/other" },
  };
}

function doc(parked: boolean): MergedGraph {
  return graph(
    [synced("apw", "In Progress"), synced(KEY, "In Progress", { question: parked ? question() : null }), synced("apw/other")],
    [], { home: homeDoc(parked ? [homeQuestion()] : []) },
  );
}

type Home = ReturnType<typeof mount> & { setDoc: (d: MergedGraph) => void };
async function home(d: MergedGraph, onanswer: (key: string, text: string) => Promise<string | null> = async () => null) {
  app = mount(HomeHarness, { target: document.body, props: { initial: d, onanswer } });
  await settle();
  return app as Home;
}

async function answer(textValue: string, scope = "") {
  const box = $<HTMLTextAreaElement>(`${scope} [data-question] textarea`)!;
  box.value = textValue;
  box.dispatchEvent(new Event("input", { bubbles: true }));
  flushSync();
  $<HTMLFormElement>(`${scope} [data-question] form`)!.requestSubmit();
  await settle();
}

describe("Scenario: A question on the home page", () => {
  it("lists the parked story first, under Answer, with the whole statement, when it stopped, and an answer box", async () => {
    approver();
    await home(doc(true));
    expect($$("[data-column]").map((c) => c.dataset.column)).toEqual(["answer", "approve", "spec", "operator"]);
    const first = $("[data-column='answer'] .item")!;
    expect(first.dataset.key).toBe(KEY);
    expect(first.dataset.rank).toBe("1");
    expect($("[data-column='approve'] .item")!.dataset.rank).toBe("2");
    expect(text("[data-column='answer'] .name")).toBe("worker-daemon");
    expect(text("[data-column='answer'] .note")).toBe("ONS-25 · The worker daemon");
    // The whole statement: both paragraphs, nothing cut.
    expect(flat(text(`[data-question='${KEY}'] .statement`))).toBe(flat(STATEMENT));
    expect($$(`[data-question='${KEY}'] .statement p`)).toHaveLength(2);
    expect($(`[data-question='${KEY}'] time`)!.getAttribute("datetime")).toBe("2026-11-01T18:35:26.000Z");
    expect($(`[data-question='${KEY}'] textarea`)).not.toBeNull();
    expect(text("[data-tile='needs']")).toContain("1 question to answer");
    expect(text("[data-tile='needs']")).toContain("nearest: worker-daemon, which asks a question");
  });

  it("appears within one refresh, and leaves as soon as Sortie no longer reports the park", async () => {
    approver();
    const h = await home(doc(false));
    expect($("[data-column='answer'] .item")).toBeNull();
    expect(text("[data-column='answer'] .none")).toBe("No agent waits on an answer.");
    h.setDoc(doc(true));
    await settle();
    expect($("[data-column='answer'] .item")!.dataset.key).toBe(KEY);
    h.setDoc(doc(false));
    await settle();
    expect($("[data-column='answer'] .item")).toBeNull();
  });

  it("names the question in the execution panel, never only stuck", async () => {
    await home(doc(true));
    expect(text("[data-verdict] .big")).toBe("Stuck");
    const reason = $("[data-verdict] [data-reason='question']")!;
    expect(flat(reason.textContent!)).toBe(flat(`${KEY} asks: ${STATEMENT}`));
    expect(reason.querySelector("a")!.getAttribute("href")).toContain("graph");
  });
});

describe("a story the plan replaces", () => {
  const replaced = (): MergedGraph => {
    const d = doc(true);
    d.home!.questions = [{ ...homeQuestion(), replaced_by: { key: "apw/worker-daemon-in-ao", label: "worker-daemon-in-ao", title: "The worker daemon, in agentic-orchestration" } }];
    return d;
  };

  it("says which story replaces it, and closes it as replaced after a confirmation", async () => {
    approver();
    const closed: string[] = [];
    app = mount(HomeHarness, { target: document.body, props: { initial: replaced(), onreplace: async (k: string) => { closed.push(k); return null; } } });
    await settle();
    expect(text(`[data-question='${KEY}'] [data-replaced-by]`)).toContain("worker-daemon-in-ao");
    $<HTMLButtonElement>(`[data-question='${KEY}'] [data-act='replace']`)!.click();
    await settle();
    expect(closed).toEqual([]);
    expect(text(`[data-question='${KEY}'] .confirm`)).toContain("Cancel apw/worker-daemon");
    $<HTMLButtonElement>(`[data-question='${KEY}'] [data-act='confirm-replace']`)!.click();
    await settle();
    expect(closed).toEqual(["apw/worker-daemon-in-ao"]);
    expect(text(`[data-question='${KEY}'] [role=status]`)).toContain("Closed as replaced by worker-daemon-in-ao");
  });

  it("offers no close to a planner, and none when nothing replaces the story", async () => {
    planner();
    app = mount(HomeHarness, { target: document.body, props: { initial: replaced(), onreplace: async () => null } });
    await settle();
    expect($(`[data-question='${KEY}'] [data-act='replace']`)).toBeNull();
    unmount(app); app = undefined; document.body.innerHTML = "";
    approver();
    await home(doc(true));
    expect($(`[data-question='${KEY}'] [data-replaced-by]`)).toBeNull();
  });
});

describe("Scenario: Answered", () => {
  it("sends an approver's answer, and says it went", async () => {
    approver();
    const sent: [string, string][] = [];
    await home(doc(true), async (k, t) => { sent.push([k, t]); return null; });
    await answer("  agentic-orchestration  ");
    expect(sent).toEqual([[KEY, "agentic-orchestration"]]);
    expect(text(`[data-question='${KEY}'] [role=status]`)).toContain("Answer sent");
  });

  it("says why an answer did not go through, and keeps the box", async () => {
    approver();
    await home(doc(true), async () => "apw/worker-daemon no longer waits on an answer");
    await answer("yes");
    expect(text(`[data-question='${KEY}'] [role=alert]`)).toBe("apw/worker-daemon no longer waits on an answer");
    expect($(`[data-question='${KEY}'] textarea`)).not.toBeNull();
  });

  it("sends nothing empty", async () => {
    approver();
    const sent: string[] = [];
    await home(doc(true), async (_k, t) => { sent.push(t); return null; });
    await answer("   ");
    expect(sent).toEqual([]);
  });

  it("shows a planner the question, without an answer box", async () => {
    planner();
    await home(doc(true));
    expect(flat(text(`[data-question='${KEY}'] .statement`))).toBe(flat(STATEMENT));
    expect($(`[data-question='${KEY}'] textarea`)).toBeNull();
    expect(text(`[data-question='${KEY}'] [data-note='approver']`)).toBe("Answering needs the approver role.");
  });
});

class FakeActions implements StoryActions {
  answers: [string, string][] = [];
  async approve() { return {}; }
  async pullBack() { return {}; }
  async cancel() { return {}; }
  async supersede() { return {}; }
  async answer(k: string, t: string) { this.answers.push([k, t]); return {}; }
}

const noSession: SessionClient = {
  status: async () => ({ active: false }), start: async () => ({ active: true }), stop: async () => ({ active: false }),
  command: async () => ({}), subscribe: () => () => {},
  config: async () => ({ options: [], ready: true }), setConfig: async () => ({ options: [], ready: true }),
};

describe("the story's card", () => {
  it("shows the same question and takes the answer", async () => {
    approver();
    const actions = new FakeActions();
    const h = mount(ChatHarness, { target: document.body, props: { initial: doc(true), session: noSession, actions } }) as
      ReturnType<typeof mount> & { select: (k: string | null) => void };
    app = h;
    h.select(KEY);
    await settle();
    expect(text(`[data-asks='${KEY}'] > b`)).toBe("The agent asks");
    expect(flat(text(`[data-asks='${KEY}'] .statement`))).toBe(flat(STATEMENT));
    expect($(`[data-asks='${KEY}'] time`)!.getAttribute("datetime")).toBe("2026-11-01T18:35:26.000Z");
    await answer("agentic-orchestration", `[data-asks='${KEY}']`);
    expect(actions.answers).toEqual([[KEY, "agentic-orchestration"]]);
  });

  it("badges the parked story and its epic in the graph", async () => {
    const view: ViewState = { epic: "apw", selected: null, showCross: true };
    app = mount(GraphHarness, { target: document.body, props: { initial: doc(true), initialView: view } });
    flushSync();
    const card = $(`.card[data-key='${KEY}']`)!;
    expect(card.querySelector(".badge.ask")!.textContent).toBe("asks a question");
    expect(card.getAttribute("aria-label")).toContain("asks a question");
    expect(card.querySelector(".meta")!.textContent).toBe("waits on an answer");
  });
});

// Scenario: Elsewhere in the workbench ----------------------------------------------

class FakeEventSource {
  static last: FakeEventSource | null = null;
  listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
  onerror: (() => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  readyState = 1;
  constructor(readonly url: string) { FakeEventSource.last = this; }
  addEventListener(name: string, fn: (e: MessageEvent) => void) { (this.listeners[name] ??= []).push(fn); }
  emit(name: string, data: unknown) { for (const fn of this.listeners[name] ?? []) fn(new MessageEvent(name, { data: JSON.stringify(data) })); }
  close() { this.readyState = 2; }
}

function backend(first: MergedGraph) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    if (url.endsWith("/api/projects/homelab")) {
      return json({ project: { id: 1, slug: "homelab", name: "Homelab" }, tracker: null, tracker_error: null });
    }
    if (url.endsWith("/graph")) return json(first);
    if (url.includes("/session")) return json({ active: false, options: [], ready: true });
    return json({});
  });
}

describe("Scenario: Elsewhere in the workbench", () => {
  it("shows 1 question in the project bar and the page title of the graph view, without a reload", async () => {
    planner();
    vi.stubGlobal("fetch", backend(doc(false)));
    vi.stubGlobal("EventSource", FakeEventSource);
    const view: ViewState = { epic: null, selected: null, showCross: true };
    app = mount(ProjectView, { target: document.body, props: { slug: "homelab", page: "graph", view, onmissing: () => {} } });
    await settle();
    expect($("[aria-label='Plan graph']")).not.toBeNull();
    expect(document.title).toBe("Homelab · Plan workbench");
    expect($("[data-questions]")).toBeNull();

    FakeEventSource.last!.emit("graph", doc(true));
    await settle();
    const bar = $(".top [data-questions]")!;
    expect(bar.textContent!.trim()).toBe("1 question");
    expect(document.title).toBe("1 question · Homelab · Plan workbench");

    FakeEventSource.last!.emit("graph", doc(false));
    await settle();
    expect($("[data-questions]")).toBeNull();
    expect(document.title).toBe("Homelab · Plan workbench");
  });

  it("counts every open question, in words", () => {
    const two = graph([synced("a/x", "In Progress", { question: question() }), synced("a/y", "In Progress", { question: question() })]);
    expect(two.nodes.filter((n: GraphNode) => n.question).length).toBe(2);
    expect(questionsLabel(1)).toBe("1 question");
    expect(questionsLabel(2)).toBe("2 questions");
    expect(pageTitle("Homelab", 2)).toBe("2 questions · Homelab · Plan workbench");
    expect(pageTitle("", 0)).toBe("Plan workbench");
    expect(stoppedAt(null)).toBeNull();
    expect(stoppedAt(PARKED_AT, (PARKED_AT + 300) * 1000)!.text).toContain("5 min ago");
  });
});
