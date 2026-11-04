import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, describe, expect, it } from "vitest";
import LastRunPane from "../src/components/LastRunPane.svelte";
import RunStateView from "../src/components/RunState.svelte";
import { session } from "../src/lib/identity.svelte";
import { runItems, type GrantResult, type RunClient, type RunEvent, type RunState } from "../src/lib/run";
import type { GraphNode } from "../src/lib/types";

let app: ReturnType<typeof mount> | undefined;
afterEach(() => {
  if (app) unmount(app);
  app = undefined;
  document.body.innerHTML = "";
  session.identity = null;
});
const settle = async () => { for (let i = 0; i < 6; i++) { await tick(); await Promise.resolve(); } flushSync(); };
const $ = <T extends Element = HTMLElement>(sel: string) => document.querySelector<T>(sel);
const text = (sel: string) => $(sel)?.textContent?.replace(/\s+/g, " ").trim() ?? "";
const approver = () => (session.identity = { name: "Ann", email: "", roles: ["planner", "approver"], id: "a" });

const NOW = 1_800_000_000;

function run(extra: Partial<RunState>): RunState {
  return { sortie: "p1", identifier: "ONS-68", status: "idle", headline: null, text: null, why: [], releases: null, action: null, question: null,
    budget: null, blockers: [], retry: null, running: null, pulls: [], next: [], runs: [], can_grant: null, max_sessions: 16, ...extra };
}

const budget = run({ status: "budget", headline: "Held: session budget reached", action: "grant",
  why: ["It used 16 of its 16 sessions, so Sortie starts no more."], releases: "More sessions release it: Sortie runs it again at its next poll.",
  budget: { reason: "session_budget", used: 16, sessions: 16, since: NOW - 3600 }, can_grant: true,
  runs: [{ dispatch_id: "D2", status: "cancelled", rule: "review", started_at: NOW - 7200, completed_at: NOW - 6000, error: null },
    { dispatch_id: "D1", status: "succeeded", rule: "build", started_at: NOW - 9000, completed_at: NOW - 7300, error: null }] });

const events: Record<string, RunEvent[]> = {
  D2: [
    { seq: 1, at: "2026-11-02T17:05:08Z", event: "session_started", message: "session started", text: "Session started." },
    { seq: 2, at: "2026-11-02T17:05:20Z", event: "tool_result", message: "", text: "Ran go test ./...", tool_line: "go test ./...", tool_error: false },
    { seq: 3, at: "2026-11-02T17:06:00Z", event: "notification", message: "The review still passes. The branch has not changed since the last turn, and CI is green on it.", text: "x" },
  ],
  D1: [{ seq: 1, at: "2026-11-02T16:05:08Z", event: "notification", message: "built it", text: "built it" }],
};

function fake(state: RunState, result: GrantResult = { ok: true }) {
  const calls = { grants: [] as [string, number][], events: [] as string[], states: 0 };
  const client: RunClient = {
    state: async () => { calls.states++; return state; },
    events: async (_id, d) => { calls.events.push(d); return events[d] ?? []; },
    grant: async (id, n) => { calls.grants.push([id, n]); return result; },
  };
  return { client, calls };
}

const node = { plan_key: "sortie-supervises-runs/failure-routing", kind: "story", title: "failure routing", sortie: "p1",
  tracker: { identifier: "ONS-68" }, question: null } as unknown as GraphNode;

describe("the run state at the top of a story's panel", () => {
  it("says why a story held at its budget waits, what releases it, and grants more sessions", async () => {
    approver();
    const { client, calls } = fake(budget);
    let opened = 0;
    app = mount(RunStateView, { target: document.body, props: { identifier: "ONS-68", story: node.plan_key, runs: client, canAct: true,
      onanswer: async () => null, onlastrun: () => opened++ } });
    await settle();
    expect($("[data-run-state]")!.dataset.runState).toBe("budget");
    expect(text(".head")).toBe("Held at its session budget on p1");
    expect(text(".why")).toBe("It used 16 of its 16 sessions, so Sortie starts no more.");
    expect(text("[data-releases]")).toBe("Releases it More sessions release it: Sortie runs it again at its next poll.");
    $<HTMLFormElement>("[data-grant] form")!.requestSubmit();
    await settle();
    expect(calls.grants).toEqual([["ONS-68", 4]]);
    $<HTMLButtonElement>("[data-act='last-run']")!.click();
    expect(opened).toBe(1);
  });

  it("answers a parked story's question in place", async () => {
    approver();
    const parked = run({ sortie: "taichi", identifier: "ONS-63", status: "parked", action: "answer",
      question: { identifier: "ONS-63", statement: "Per check or per phase?", parked_at: NOW - 600, reason: "agent blocked" },
      releases: "Your answer releases it." });
    const sent: string[] = [];
    app = mount(RunStateView, { target: document.body, props: { identifier: "ONS-63", story: "a/b", runs: fake(parked).client, canAct: true,
      onanswer: async (t: string) => { sent.push(t); return null; } } });
    await settle();
    expect(text(".head")).toBe("Parked: the agent asks you on taichi");
    expect(text(".statement")).toBe("Per check or per phase?");
    const area = $<HTMLTextAreaElement>("textarea")!;
    area.value = "Per check.";
    area.dispatchEvent(new Event("input"));
    await settle();
    $<HTMLFormElement>(".question form")!.requestSubmit();
    await settle();
    expect(sent).toEqual(["Per check."]);
  });

  it("names a pull request with what it waits on, and opens a running story's session", async () => {
    const reaction = run({ status: "reaction", action: "open_pr", pulls: [{ number: 31, url: "https://github.com/o/r/pull/31", repository: "o/r", waits: "#31 waits on CI", ready: false }] });
    app = mount(RunStateView, { target: document.body, props: { identifier: "ONS-78", story: "a/b", runs: fake(reaction).client, canAct: false, onanswer: async () => null } });
    await settle();
    expect(text("[data-pull='31']")).toBe("o/r#31 waits on CI");
    expect($("[data-act='open-pr']")!.getAttribute("href")).toBe("https://github.com/o/r/pull/31");
    unmount(app);
    let watched = 0;
    const running = run({ status: "running", action: "watch", running: { session_id: "s", dispatch_id: "D", rule: "review", started_at: NOW, watchable: true, held: false, holder: null } });
    app = mount(RunStateView, { target: document.body, props: { identifier: "ONS-50", story: "a/b", runs: fake(running).client, canAct: true,
      onanswer: async () => null, onwatch: () => watched++ } });
    await settle();
    $<HTMLButtonElement>("[data-act='open-session']")!.click();
    expect(watched).toBe(1);
  });
});

describe("a held story's last run in the chat pane", () => {
  it("shows its newest run's transcript, lets a person pick another, and holds the unblock action", async () => {
    approver();
    const { client, calls } = fake(budget);
    let closed = 0;
    app = mount(LastRunPane, { target: document.body, props: { node, runs: client, canAct: true, onanswer: async () => null, onclose: () => closed++, now: NOW } });
    await settle();
    expect(calls.events).toEqual(["D2"]);
    expect(text(".session-bar .state")).toBe("Last run of ONS-68 · held at its session budget on p1");
    expect(text(".log")).toContain("The review still passes.");
    expect(text(".log")).toContain("go test ./...");
    const pick = $<HTMLSelectElement>(".session-bar select")!;
    pick.value = "D1";
    pick.dispatchEvent(new Event("change", { bubbles: true }));
    await settle();
    expect(calls.events).toEqual(["D2", "D1"]);
    expect(text(".log")).toContain("built it");
    $<HTMLFormElement>("[data-unblock] form")!.requestSubmit();
    await settle();
    expect(calls.grants).toEqual([["ONS-68", 4]]);
    $<HTMLButtonElement>("[data-act='back-to-planning']")!.click();
    expect(closed).toBe(1);
  });

  it("turns Sortie's kept events into a transcript", () => {
    const items = runItems(events.D2);
    expect(items.map((i) => i.kind)).toEqual(["notice", "tool", "agent"]);
    expect(items[1]).toMatchObject({ title: "go test ./...", status: "completed" });
  });
});
