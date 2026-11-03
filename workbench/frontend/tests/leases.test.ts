/** A story the supervisor holds under a lease waits on the supervisor, not on
 * the operator: the home's lanes and the story's panel say so with the lease's
 * evidence and expiry, and offer no Answer. Once the lease runs out, the
 * story is stuck for the operator, with its own entry. */
import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import ChatHarness from "./ChatHarness.svelte";
import HomeHarness from "./HomeHarness.svelte";
import { graph, synced } from "./fixtures";
import { leaseText, type Fleet, type Lease, type Stuck, type Wait, type Worker } from "../src/lib/fleet";
import type { ExpiredHold, HomeDoc } from "../src/lib/home";
import { session as identity } from "../src/lib/identity.svelte";
import type { SessionClient } from "../src/lib/session";
import type { StoryActions } from "../src/lib/sync";

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
const text = (s: string) => $(s)?.textContent?.replace(/\s+/g, " ").trim() ?? "";

const NOW = 1_793_700_000;
const REASON = "full vm chain proof running";
const EVIDENCE = "sandbox hl-ons-54-proof-1 on taichi";

function lease(extra: Partial<Lease> = {}): Lease {
  return { identifier: "ONS-54", sortie: "taichi", by: "supervisor", reason: REASON, evidence: EVIDENCE, held_since: NOW - 1200,
    renewed_at: NOW - 600, until: NOW + 3000, expired: false, expired_at: null, ...extra };
}

function worker(extra: Partial<Worker>): Worker {
  return {
    name: "taichi", sortie: "taichi", remote: false, last_seen: NOW, slots: 2, slots_known: true, busy: 0, capacity: null, used: null,
    verdict: "idle", reasons: [], lanes: [{ slot: 1, job: null, stuck: null, extra: false }, { slot: 2, job: null, stuck: null, extra: false }],
    jobs: [], stuck: [], waits: [], next: [], unmanaged: [], draining: null, ...extra,
  };
}

function homeGraph(w: Worker, verdict: string, reasons: { code: string; text: string }[], expired: ExpiredHold[] = []) {
  const f: Fleet = { generated_at: NOW, sorties: [{ name: "taichi", reachable: true, error: null }], workers: [w], pulls: [] };
  const home: HomeDoc = {
    generated_at: NOW, plan: { valid: true, errors: 0, has_graph: true }, milestones_declared: true, story_progress: "live",
    health: { verdict: verdict as HomeDoc["health"]["verdict"], reasons, slots: 2, busy: 0, free: 2, running: [], held: [], runway: 0 },
    milestones: [], goal: null, next: null, critical_path: null, not_ready: [], questions: [], expired_holds: expired,
    needs_person: { total: expired.length, questions: 0, expired_holds: expired.length, approve: 0, approve_stories: 0, spec: 0, operator: 0,
      nearest: expired[0]?.key ?? null },
    timeline: null,
    lanes: { verdict, reasons: reasons.map((r) => ({ ...r, worker: null })), busy: 0, slots: 2, runway: 0, generated_at: NOW, workers: [w] },
  };
  return graph([synced("homelab"), synced("homelab/vm-acceptance", "In Progress")], [], { home, fleet: f });
}

describe("A story the supervisor holds", () => {
  it("waits on the supervisor in the lanes, with the evidence and the expiry, and no Answer", async () => {
    const x: Wait = { identifier: "ONS-54", key: "homelab/vm-acceptance", label: "vm-acceptance", title: "VM acceptance", sortie: "taichi",
      kind: "supervisor", reason: `waiting on the supervisor: ${REASON}`, release: "the supervisor releases it", since: NOW - 1200, lease: lease() };
    const w = worker({ waits: [x], reasons: [{ code: "idle", text: "nothing runs; 1 item waiting" }] });
    app = mount(HomeHarness, { target: document.body, props: { initial: homeGraph(w, "waiting",
      [{ code: "supervisor", text: "1 story waits on 1 supervisor hold: the supervisor proves them" }]) } });
    await settle();
    expect($("[data-verdict]")!.dataset.verdict).toBe("waiting");
    expect(text("[data-verdict] .big")).toBe("Waiting on the supervisor");
    expect($("[data-verdict]")!.classList.contains("alert")).toBe(false);
    const sup = $("[data-wait-supervisor='ONS-54']")!;
    expect(sup.textContent).toContain(`vm-acceptance waiting on the supervisor: ${REASON}`);
    expect(text("[data-wait-supervisor='ONS-54'] [data-lease]")).toBe(`evidence: ${EVIDENCE} · renewed 10 min ago · runs out at 10:50 UTC, in 50 min`);
    expect(document.querySelector("[data-act='answer']")).toBeNull();
    expect(document.querySelector("[data-stuck='true']")).toBeNull();
    expect(text("[data-column='answer'] .none")).toBe("No agent waits on an answer.");
  });

  it("is stuck for the operator once its lease expired, with its own entry", async () => {
    const reason = `the supervisor's hold expired at 2026-11-03 09:59 UTC; last evidence: ${EVIDENCE}`;
    const s: Stuck = { identifier: "ONS-54", key: "homelab/vm-acceptance", label: "vm-acceptance", title: "VM acceptance", sortie: "taichi",
      worker: "taichi", kind: "hold_expired", reason, since: NOW - 60, action: "story", slot: 1, lease: lease({ expired: true, expired_at: NOW - 60 }) };
    const w = worker({ verdict: "stuck", stuck: [s], lanes: [{ slot: 1, job: null, stuck: s, extra: false }, { slot: 2, job: null, stuck: null, extra: false }],
      reasons: [{ code: "hold_expired", text: `slot 1: vm-acceptance ${reason}`, identifier: "ONS-54", key: "homelab/vm-acceptance", action: "story" }] });
    const expired: ExpiredHold = { key: "homelab/vm-acceptance", label: "vm-acceptance", change: "homelab", identifier: "ONS-54", title: "VM acceptance",
      text: reason, rank: 1, lease: lease({ expired: true, expired_at: NOW - 60 }) };
    app = mount(HomeHarness, { target: document.body, props: { initial: homeGraph(w, "stuck",
      [{ code: "hold_expired", text: `taichi slot 1: vm-acceptance ${reason}` }], [expired]) } });
    await settle();
    expect($("[data-verdict]")!.dataset.verdict).toBe("stuck");
    expect(text("[data-why='hold_expired'] p")).toBe(`Stuck: vm-acceptance ${reason}`);
    expect($("[data-why='hold_expired'] [data-act='story']")).not.toBeNull();
    expect(text("[data-expired-hold='ONS-54'] [data-expired-text]")).toBe(
      `The supervisor's hold expired at 2026-11-03 09:59 UTC; last evidence: ${EVIDENCE}. Release the hold in Sortie, or ask the supervisor to renew it.`);
    expect(document.querySelector("[data-act='answer']")).toBeNull();
  });

  it("says in its panel that it waits on the supervisor, with the lease", async () => {
    const noSession: SessionClient = {
      status: async () => ({ active: false }), start: async () => ({ active: true }), stop: async () => ({ active: false }),
      command: async () => ({}), subscribe: () => () => {},
      config: async () => ({ options: [], ready: true }), setConfig: async () => ({ options: [], ready: true }),
    };
    const now = Date.now() / 1000;
    const n = synced("homelab/vm-acceptance", "In Progress", { question: null, run_state: { status: "waiting", worker: "taichi", sortie: "taichi", slot: null,
      why: `waiting on the supervisor: ${REASON}`, release: "the supervisor releases it", kind: "supervisor", action: null, phase: null, step: null, pull: null,
      lease: lease({ renewed_at: now - 600, until: now + 3000 }) } });
    app = mount(ChatHarness, { target: document.body, props: { initial: graph([synced("homelab"), n]), session: noSession, actions: {} as StoryActions,
      initialView: { epic: "homelab", selected: n.plan_key, showCross: true } } });
    await settle();
    expect(text("[data-run-state='waiting'] b")).toBe("Waiting on the supervisor");
    expect(text("[data-run-state] [data-why]")).toBe(`waiting on the supervisor: ${REASON}`);
    expect(text("[data-run-state] [data-lease]")).toContain(`evidence: ${EVIDENCE} · renewed 10 min ago · runs out at`);
    expect(document.querySelector("[data-act='answer']")).toBeNull();
  });

  it("words a lease's evidence, renewal and expiry", () => {
    expect(leaseText(lease(), NOW)).toBe(`evidence: ${EVIDENCE} · renewed 10 min ago · runs out at 10:50 UTC, in 50 min`);
    expect(leaseText(lease({ evidence: null, renewed_at: null, until: NOW - 60 }), NOW)).toBe("evidence: none given · ran out at 09:59 UTC");
  });
});
