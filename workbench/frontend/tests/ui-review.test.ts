import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, describe, expect, it } from "vitest";
import ChatHarness from "./ChatHarness.svelte";
import GraphHarness from "./GraphHarness.svelte";
import { graph, node, synced } from "./fixtures";
import { session as identity } from "../src/lib/identity.svelte";
import type { SessionClient } from "../src/lib/session";
import type { StoryActions } from "../src/lib/sync";
import type { GraphNode, MergedGraph } from "../src/lib/types";

// A review: ui story (agent-platform/run-supervision#A UI story is built
// before it is approved, and its merge is the approval).
const C = "sortie-supervises-runs";
const UI = `${C}/worker-activity-view`;
const ADDRESS = "https://workbench-ons-97.ortus.example/";
const PR = "ortusnova/agentic-orchestration#80";

const preview = { url: ADDRESS, pull: PR, pull_url: "https://github.com/ortusnova/agentic-orchestration/pull/80", sha: "1".repeat(40) };

function ui(fields: Partial<GraphNode>, state?: string): MergedGraph {
  const story = { approval: "draft" as const, review: "ui" as const, covers: ["run-supervision#Show worker activity"], ...fields };
  return graph([node(C), state ? synced(UI, state, story) : node(UI, story)]);
}

class FakeActions implements StoryActions {
  approved: unknown[] = [];
  async approve(t: { keys?: string[]; epic?: string }) { this.approved.push(t); return {}; }
  async pullBack() { return {}; }
  async cancel() { return {}; }
  async supersede() { return {}; }
  async answer() { return {}; }
}

const noSession: SessionClient = {
  status: async () => ({ active: false }), start: async () => ({ active: true }), stop: async () => ({ active: false }),
  command: async () => ({}), subscribe: () => () => {},
  config: async () => ({ options: [], ready: true }), setConfig: async () => ({ options: [], ready: true }),
};

let app: ReturnType<typeof mount> | undefined;
afterEach(() => {
  if (app) unmount(app);
  app = undefined;
  document.body.innerHTML = "";
  identity.identity = null;
});
async function settle() { for (let i = 0; i < 6; i++) { await tick(); await Promise.resolve(); } flushSync(); }
const $ = <T extends Element = HTMLElement>(s: string) => document.querySelector<T>(s);

function select(doc: MergedGraph, key: string) {
  identity.identity = { name: "Ann", email: "", roles: ["planner", "approver"], id: "a" };
  const h = mount(ChatHarness, { target: document.body, props: { initial: doc, session: noSession, actions: new FakeActions() } }) as
    ReturnType<typeof mount> & { select: (k: string | null) => void };
  app = h;
  h.select(key);
}

describe("a ui story", () => {
  it("offers no Approve, and says who reviews it", async () => {
    select(ui({ plan_status: "ready" }), UI);
    await settle();
    expect($("[data-act='approve']")).toBeNull();
    expect($("[data-plan-status='ready']")!.textContent).toBe("Ready");
    expect(document.body.textContent).toContain("review: you, on a preview");
  });

  it("offers no Approve even when it needs re-review", async () => {
    select(ui({ plan_status: "needs_rereview", rereview: { reason: "blocker_canceled", ref: `${C}/a` } }), UI);
    await settle();
    expect($("[data-act='approve']")).toBeNull();
  });

  it("shows as building while agents build it", async () => {
    select(ui({ plan_status: "building", frozen: true }, "In Progress"), UI);
    await settle();
    expect($("[data-plan-status='building']")!.textContent).toBe("Building");
    expect($("[data-why='building']")!.textContent).toContain("Merging the pull request approves it");
    expect($("[data-why='preview']")).toBeNull();
  });

  it("then reads ready for you to look, with the preview's address and its pull request", async () => {
    select(ui({ plan_status: "preview", frozen: true, preview }, "In Review"), UI);
    await settle();
    expect($("[data-plan-status='preview']")!.textContent).toBe("Ready for you to look");
    const [address, pull] = Array.from(document.querySelectorAll<HTMLAnchorElement>("[data-why='preview'] a"));
    expect(address.href).toBe(ADDRESS);
    expect(address.textContent).toBe(ADDRESS);
    expect(pull.textContent).toBe(PR);
    expect(pull.href).toBe(preview.pull_url);
    expect($("[data-act='approve']")).toBeNull();
  });

  it("reads ready for you to look on its card", async () => {
    const h = mount(GraphHarness, { target: document.body, props: { initial: ui({ plan_status: "preview", frozen: true, preview }, "In Review"), initialView: { epic: C, selected: null, showCross: true } } });
    app = h;
    await settle();
    expect($(`[data-key='${UI}'] .meta`)!.textContent).toBe("ready for you to look");
  });
});
