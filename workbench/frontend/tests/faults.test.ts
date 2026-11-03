/** Platform faults and their person actions (agent-platform/run-supervision): the home lists each fault only a
 * person can clear under Needs a person, beside the agents' questions, with its cause, its action and where to take
 * it, the stories it holds, and a Done that asks Sortie to check again. A story's card shows its failure's full cause. */
import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import ChatHarness from "./ChatHarness.svelte";
import HomeHarness from "./HomeHarness.svelte";
import { graph, synced } from "./fixtures";
import type { HomeDoc, HomeFault, HomeQuestion } from "../src/lib/home";
import { session as identity } from "../src/lib/identity.svelte";
import { openQuestions } from "../src/lib/questions";
import type { SessionClient } from "../src/lib/session";
import type { StoryActions } from "../src/lib/sync";
import type { Failure, GraphNode, MergedGraph } from "../src/lib/types";

globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;

let app: ReturnType<typeof mount> | undefined;
afterEach(() => {
  if (app) unmount(app);
  app = undefined;
  identity.identity = null;
  document.body.innerHTML = "";
});
const settle = async () => { for (let i = 0; i < 8; i++) { await tick(); await Promise.resolve(); } flushSync(); };
const $ = <T extends Element = HTMLElement>(s: string) => document.querySelector<T>(s);
const $$ = (s: string) => [...document.querySelectorAll<HTMLElement>(s)];
const text = (s: string) => $(s)?.textContent?.replace(/\s+/g, " ").trim() ?? "";
const approver = () => (identity.identity = { name: "Ann", email: "", roles: ["planner", "approver"], id: "a" });

const ID = "pf-0123456789ab";
const CAUSE = "refusing to allow a Personal Access Token to create or update workflow `.github/workflows/ci.yml` without `workflow` scope";
const DETAIL = `To github.com:ortusnova/agentic-orchestration.git\n ! [remote rejected] sortie/ONS-49 -> sortie/ONS-49 (${CAUSE})`;
const ACTION = "Give taichi's GitHub token \"Workflows: Read and write\"";
const WHERE = "github.com → Settings → Developer settings → Fine-grained tokens";
const KEYS = ["apw/push-a", "apw/push-b", "apw/push-c"];
const AT = Date.parse("2026-11-03T12:00:00Z") / 1000;

function fault(extra: Partial<HomeFault> = {}): HomeFault {
  return {
    id: ID, sortie: "taichi", class: "token_scope", step: "export", cause: CAUSE, detail: DETAIL, action: ACTION, where: WHERE,
    done_at: null, done_by: null,
    stories: KEYS.map((k, i) => ({ identifier: `ONS-${49 + i}`, display: null, held_at: AT, key: k, label: k.split("/")[1], change: "apw", title: `Push ${i}` })),
    keys: KEYS, key: KEYS[0], recorded_at: AT, last_check_at: null, last_check_error: null, next_check_at: AT + 300, checking: false, rank: 2,
    ...extra,
  };
}

function question(): HomeQuestion {
  return { identifier: "ONS-25", statement: "Which repository?", parked_at: AT - 600, reason: "agent blocked", key: "apw/daemon",
    label: "daemon", change: "apw", title: "Daemon", url: null, rank: 1 };
}

function doc(faults: HomeFault[], questions: HomeQuestion[] = []): MergedGraph {
  const home: HomeDoc = {
    generated_at: 0, plan: { valid: true, errors: 0, has_graph: true }, milestones_declared: true, story_progress: "live",
    health: { verdict: "stuck", reasons: [], slots: 2, busy: 0, free: 2, running: [], held: [], runway: 0 },
    milestones: [], goal: null, next: null, critical_path: null, timeline: null, not_ready: [], questions, faults,
    needs_person: { total: faults.length + questions.length, questions: questions.length, faults: faults.length, approve: 0, approve_stories: 0,
      spec: 0, operator: 0, nearest: KEYS[0] },
  };
  return graph([synced("apw", "In Progress"), synced("apw/daemon", "In Progress", { question: questions.length ? questions[0] : null }),
    ...KEYS.map((k) => synced(k, "In Progress"))], [], { home });
}

describe("A platform fault on the home", () => {
  it("is under Needs a person beside the questions, with its cause, action, where, and stories", async () => {
    approver();
    app = mount(HomeHarness, { target: document.body, props: { initial: doc([fault()], [question()]) } });
    await settle();
    const items = $$("[data-column='answer'] li");
    expect(items.map((li) => li.dataset.rank)).toEqual(["1", "2"]);
    expect(items[1].dataset.platformFault).toBe(ID);
    expect(text("[data-column='answer'] .ch span")).toBe("2 · agents wait");
    expect(text(`[data-fault='${ID}'] [data-cause]`)).toBe(CAUSE);
    expect(text(`[data-fault='${ID}'] [data-action]`)).toBe(ACTION);
    expect(text(`[data-fault='${ID}'] [data-where]`)).toBe(WHERE);
    expect($$(`[data-fault='${ID}'] [data-held]`).map((a) => a.dataset.held)).toEqual(KEYS);
    expect($(`[data-fault='${ID}'] [data-detail]`)!.textContent).toBe(DETAIL);
    expect(text("#needs-h span")).toContain("numbers rank all 2 by urgency");
  });

  it("calls Sortie's re-check through Done", async () => {
    approver();
    const onfaultdone = vi.fn(async () => null);
    app = mount(HomeHarness, { target: document.body, props: { initial: doc([fault()]), onfaultdone } });
    await settle();
    $<HTMLButtonElement>(`[data-fault='${ID}'] [data-act='fault-done']`)!.click();
    await settle();
    expect(onfaultdone).toHaveBeenCalledWith(ID);
    expect(text(`[data-fault='${ID}'] .sent`)).toBe("Sortie is checking again; the stories run once it passes.");
  });

  it("says when the check still fails after Done, and shows an error", async () => {
    approver();
    const onfaultdone = vi.fn(async () => "Sortie no longer holds this fault.");
    const failed = fault({ done_at: AT + 600, done_by: "Ann", last_check_error: "push refused again" });
    app = mount(HomeHarness, { target: document.body, props: { initial: doc([failed]), onfaultdone } });
    await settle();
    expect(text(`[data-fault='${ID}'] [data-check-error]`)).toMatch(/^Marked done by Ann .*, but Sortie's check still fails: push refused again$/);
    $<HTMLButtonElement>(`[data-fault='${ID}'] [data-act='fault-done']`)!.click();
    await settle();
    expect(text(`[data-fault='${ID}'] [role='alert']`)).toBe("Sortie no longer holds this fault.");
  });

  it("offers no Done without the approver role", async () => {
    identity.identity = { name: "Pat", email: "", roles: ["planner"], id: "p" };
    app = mount(HomeHarness, { target: document.body, props: { initial: doc([fault()]) } });
    await settle();
    expect($("[data-act='fault-done']")).toBeNull();
    expect(text(`[data-fault='${ID}'] [data-note='approver']`)).toBe("Marking it done needs the approver role.");
  });

  it("counts as an open question in the bar", () => {
    expect(openQuestions(doc([fault()], [question()]))).toBe(2);
    expect(openQuestions(doc([]))).toBe(0);
  });
});

describe("A story's card", () => {
  const noSession: SessionClient = {
    status: async () => ({ active: false }), start: async () => ({ active: true }), stop: async () => ({ active: false }),
    command: async () => ({}), subscribe: () => () => {},
    config: async () => ({ options: [], ready: true }), setConfig: async () => ({ options: [], ready: true }),
  };
  const failure = (extra: Partial<Failure>): Failure => ({
    source: "run", sortie: "taichi", cause: "worker exited: make test: exit status 2", detail: null, step: null, class: null, status: "failed",
    at: AT, fault_id: null, action: null, where: null, done_at: null, last_check_error: null, next_check_at: null, ...extra,
  });
  async function card(n: GraphNode) {
    app = mount(ChatHarness, { target: document.body, props: { initial: graph([synced("apw"), n]), session: noSession, actions: {} as StoryActions,
      initialView: { epic: "apw", selected: n.plan_key, showCross: true } } });
    await settle();
  }

  it("shows the full cause of the platform fault that holds it, and its action", async () => {
    await card(synced(KEYS[0], "In Progress", { failure: failure({ source: "platform_fault", cause: CAUSE, detail: DETAIL, step: "export",
      class: "token_scope", status: null, fault_id: ID, action: ACTION, where: WHERE }) }));
    expect(text("[data-failure='platform_fault'] > b")).toBe("Held by a platform fault");
    expect(text("[data-failure] [data-cause]")).toBe(CAUSE);
    expect($("[data-failure] [data-detail]")!.textContent).toBe(DETAIL);
    expect(text("[data-failure] [data-fault-action]")).toBe(`A person clears it: ${ACTION} (${WHERE}). Mark it done under Needs a person on the home.`);
  });

  it("shows its failed run's whole output", async () => {
    const output = "FAILED tests/test_home.py::test_ranks\n1 failed, 425 passed\nmake: *** [Makefile:8: test] Error 1";
    await card(synced(KEYS[0], "In Progress", { failure: failure({ detail: output }) }));
    expect(text("[data-failure='run'] > b")).toBe("Its last run failed");
    expect(text("[data-failure] [data-cause]")).toBe("worker exited: make test: exit status 2");
    expect($("[data-failure] [data-detail]")!.textContent).toBe(output);
  });

  it("shows nothing when the story has no failure", async () => {
    await card(synced(KEYS[0], "In Progress", { failure: null }));
    expect($("[data-failure]")).toBeNull();
  });
});
