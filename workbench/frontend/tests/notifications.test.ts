// A person is alerted, wherever the question is held (agent-platform/run-supervision):
// what each Sortie's notifications sent the workbench shows under "Needs a person",
// unread until a person has seen it. Each notification is built from a body
// Sortie's webhook backend posted (workbench/tests/fixtures/sortie-webhook), as
// the backend keeps it.
import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, describe, expect, it, vi } from "vitest";
import HomeHarness from "./HomeHarness.svelte";
import { graph, synced } from "./fixtures";
import ProjectView from "../src/components/ProjectView.svelte";
import { eventText, type HomeDoc, type HomeNotification, type HomeQuestion } from "../src/lib/home";
import { session as identity } from "../src/lib/identity.svelte";
import type { MergedGraph, ViewState } from "../src/lib/types";
import stopped from "../../tests/fixtures/sortie-webhook/session_stopped.json";
import escalated from "../../tests/fixtures/sortie-webhook/session_escalated.json";
import ciFailure from "../../tests/fixtures/sortie-webhook/escalation_ci_failure.json";

globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;

const KEY = "apw/worker-daemon";
const QUESTION = "Which cadence did the operator choose for the Authentik upgrade gate?";
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
const text = (el: Element | null) => el?.textContent?.replace(/\s+/g, " ").trim() ?? "";

type SortieBody = { event_type: string; title: string; body: string; severity: string; agent_text?: string;
  escalation?: { kind: string; rule: string; url?: string } };

/** A notification as the backend shows it: Sortie's body, linked to the story and its question. */
function fromSortie(body: SortieBody, id: number, extra: Partial<HomeNotification> = {}): HomeNotification {
  return {
    id, host: "taichi", event_type: body.event_type, severity: body.severity, category: "", title: body.title, body: body.body,
    agent_text: body.agent_text ?? "", identifier: "ONS-25",
    escalation: body.escalation ? { url: "", ...body.escalation } : null,
    sent_at: PARKED_AT + id, received_at: PARKED_AT + id, seen_at: null, seen_by: null,
    story: { key: KEY, label: "worker-daemon", change: "apw", title: "The worker daemon", url: null },
    question: { identifier: "ONS-25", sortie: "taichi", parked_at: PARKED_AT }, unread: true, again: false, ...extra,
  };
}

function homeQuestion(): HomeQuestion {
  return { identifier: "ONS-25", statement: QUESTION, parked_at: PARKED_AT, reason: "agent blocked", sortie: "taichi",
    key: KEY, label: "worker-daemon", change: "apw", title: "The worker daemon", url: null, rank: 1 };
}

function homeDoc(notifications: HomeNotification[]): HomeDoc {
  return {
    generated_at: 0, plan: { valid: true, errors: 0, has_graph: true }, milestones_declared: true, story_progress: "live",
    health: { verdict: "stuck", reasons: [], slots: 3, busy: 0, free: 3, running: [], held: [], runway: 0 },
    milestones: [], goal: null, next: null, critical_path: null, timeline: null, not_ready: [],
    questions: [homeQuestion()], notifications,
    needs_person: { total: 1, questions: 1, unread: notifications.filter((n) => n.unread).length, approve: 0, approve_stories: 0,
      spec: 0, operator: 0, nearest: KEY },
  };
}

const doc = (notifications: HomeNotification[]): MergedGraph =>
  graph([synced("apw", "In Progress"), synced(KEY, "In Progress")], [], { home: homeDoc(notifications) });

async function home(d: MergedGraph, onseen?: (ids: number[]) => Promise<string | null>) {
  app = mount(HomeHarness, { target: document.body, props: { initial: d, onseen } });
  await settle();
  return app as ReturnType<typeof mount> & { setDoc: (d: MergedGraph) => void };
}

describe("Scenario: A question on taichi's Sortie", () => {
  it("shows Sortie's notification under Needs a person, unread, with the story and the question", async () => {
    await home(doc([fromSortie(stopped, 1)]));
    const needs = $("section.needs")!;
    const item = needs.querySelector<HTMLElement>("[data-notifications] [data-notification='1']")!;
    expect(item.dataset.unread).toBe("true");
    expect(item.dataset.event).toBe("session.stopped");
    expect(text(item)).toContain("ONS-25 stopped for a person on taichi's Sortie");
    expect(text(item.querySelector(".said"))).toBe(QUESTION);
    expect(text(item)).toContain("unread");
    expect(text(item)).toContain("answer it under Answer");
    expect(item.querySelector<HTMLAnchorElement>("a.name")!.getAttribute("href")).toContain(encodeURIComponent(KEY));
    // The question itself is answered in the Answer column beside it.
    expect($("[data-column='answer'] [data-key='" + KEY + "']")).not.toBeNull();
    expect(text($("[data-notifications] .ch"))).toContain("1 unread");
  });

  it("is seen with one click, and then no longer unread", async () => {
    const seen: number[][] = [];
    const h = await home(doc([fromSortie(stopped, 1), fromSortie(ciFailure, 2, { question: null })]), async (ids) => { seen.push(ids); return null; });
    $$("[data-notification='2'] button")[0].click();
    await settle();
    expect(seen).toEqual([[2]]);
    $$("[data-notifications] .ch button")[0].click();
    await settle();
    expect(seen).toEqual([[2], [1, 2]]);
    // The backend's next home says they were seen.
    h.setDoc(doc([fromSortie(stopped, 1, { unread: false, seen_at: PARKED_AT + 60, seen_by: "Ann Approver" })]));
    await settle();
    const item = $("[data-notification='1']")!;
    expect(item.dataset.unread).toBe("false");
    expect(item.querySelector("button")).toBeNull();
    expect(text(item)).toContain("seen by Ann Approver");
    expect(text($("[data-notifications] .ch"))).toContain("0 unread");
  });

  it("says what each of Sortie's events is", async () => {
    await home(doc([fromSortie(escalated, 3), fromSortie(ciFailure, 4, { question: null })]));
    expect(text($("[data-notification='3'] .what"))).toContain("escalated to a person");
    expect(text($("[data-notification='3'] .said"))).toBe("(the check left no summary)");
    expect(text($("[data-notification='4'] .what"))).toContain("CI still fails");
    expect(text($("[data-notification='4'] .said"))).toContain("CI fix retries exhausted");
    expect(text($("[data-notification='4']"))).not.toContain("answer it under Answer");
    expect(eventText({ event_type: "budget.held", escalation: null })).toBe("held by its budget");
    expect(eventText({ event_type: "session.escalated", escalation: { kind: "question", rule: "", url: "" } })).toBe("asks a person");
  });

  it("shows nothing from Sortie when nothing was sent", async () => {
    await home(doc([]));
    expect($("[data-notifications]")).toBeNull();
  });
});

describe("Scenario: Six notifications for one story", () => {
  it("shows one entry for the story, the latest, with the count of the earlier ones, seen together", async () => {
    const seen: number[][] = [];
    const latest = fromSortie(stopped, 6, { earlier: 5, ids: [1, 2, 3, 4, 5, 6] });
    const other = fromSortie(ciFailure, 7, { identifier: "ONS-26", question: null, earlier: 0, ids: [7],
      story: { key: "apw/other", label: "other", change: "apw", title: "Another", url: null } });
    await home(doc([other, latest]), async (ids) => { seen.push(ids); return null; });
    expect($$("[data-notifications] li.note-item").length).toBe(2);
    const item = $("[data-notification='6']")!;
    expect(item.dataset.earlier).toBe("5");
    expect(text(item.querySelector(".earlier"))).toBe("5 earlier notifications");
    expect($("[data-notification='7'] .earlier")).toBeNull();
    // The count is of stories, not of webhooks.
    expect(text($("[data-notifications] .ch"))).toContain("2 unread · 2 stories");
    item.querySelector("button")!.click();
    await settle();
    expect(seen).toEqual([[1, 2, 3, 4, 5, 6]]);
    $$("[data-notifications] .ch button")[0].click();
    await settle();
    expect(seen[1]).toEqual([7, 1, 2, 3, 4, 5, 6]);
  });
});

describe("Scenario: Still open", () => {
  it("marks a notification whose question is still open after 4 hours", async () => {
    await home(doc([fromSortie(stopped, 1, { again: true, seen_at: PARKED_AT + 60, seen_by: "Ann Approver" })]));
    const item = $("[data-notification='1']")!;
    expect(item.dataset.unread).toBe("true");
    expect(text(item)).toContain("still open after 4 hours");
  });
});

describe("Seen, through the backend", () => {
  it("posts the ids to the project's notifications", async () => {
    identity.identity = { name: "Ann Approver", email: "", roles: ["planner", "approver"], id: "a" };
    const calls: [string, string | undefined][] = [];
    const first = doc([fromSortie(stopped, 7)]);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push([url, init?.body as string | undefined]);
      const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
      if (url.endsWith("/api/projects/homelab")) return json({ project: { id: 1, slug: "homelab", name: "Homelab" }, tracker: null, tracker_error: null });
      if (url.endsWith("/graph")) return json(first);
      if (url.endsWith("/notifications/seen")) return json({ seen: 1 });
      return json({});
    }));
    vi.stubGlobal("EventSource", class { addEventListener() {} close() {} });
    const view: ViewState = { epic: null, selected: null, showCross: true };
    app = mount(ProjectView, { target: document.body, props: { slug: "homelab", page: "home", view, onmissing: () => {} } });
    await settle();
    $$("[data-notification='7'] button")[0].click();
    await settle();
    expect(calls).toContainEqual(["/api/projects/homelab/notifications/seen", JSON.stringify({ ids: [7] })]);
  });
});
