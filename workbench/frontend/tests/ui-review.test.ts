import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, describe, expect, it } from "vitest";
import ChatHarness from "./ChatHarness.svelte";
import GraphHarness from "./GraphHarness.svelte";
import { graph, node, synced } from "./fixtures";
import { session as identity } from "../src/lib/identity.svelte";
import type { SessionClient } from "../src/lib/session";
import type { StoryActions } from "../src/lib/sync";
import type { GraphNode, MergedGraph, PlanStatus } from "../src/lib/types";

// A review: ui story (agent-platform/run-supervision#A UI is approved as a
// prototype, and the built story merges after its review).
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
  it("offers no Approve, and says how it is reviewed", async () => {
    select(ui({ plan_status: "ready" }), UI);
    await settle();
    expect($("[data-act='approve']")).toBeNull();
    expect($("[data-plan-status='ready']")!.textContent).toBe("Ready");
    expect(document.body.textContent).toContain("review: acceptance criteria and click-proofs on a preview");
  });

  it("offers no Approve even when it needs re-review", async () => {
    select(ui({ plan_status: "needs_rereview", rereview: { reason: "blocker_canceled", ref: `${C}/a` } }), UI);
    await settle();
    expect($("[data-act='approve']")).toBeNull();
  });

  // Scenario: A built UI story. From its build to Sortie's merge it shows
  // its state, and nothing in it asks the operator to approve or merge.
  const states: [string, string, string, string, string, boolean][] = [
    ["building", "In Progress", "Building", "building", "Agents are building it", false],
    ["in_review", "In Progress", "In review", "in review", "Its review runs", false],
    ["click_proofs", "In Review", "Waiting for click-proofs", "waiting for click-proofs", "Sortie merges it once the click-proofs on its preview are recorded", true],
    ["in_review", "In Review", "In review", "in review", "clicks the actions they name on its preview", true],
    ["merging", "In Review", "Merging", "merging", "Its click-proofs passed on its preview", true],
  ];
  for (const [status, state, chip, meta, why, deployed] of states) {
    it(`reads ${chip.toLowerCase()}${deployed ? " with its preview" : ""}, with nothing to approve`, async () => {
      const fields = { plan_status: status as PlanStatus, frozen: true, preview: deployed ? preview : null };
      select(ui(fields, state), UI);
      await settle();
      expect($(`[data-plan-status='${status}']`)!.textContent).toBe(chip);
      expect($(`[data-why='${status}']`)!.textContent).toContain(why);
      expect($("[data-act='approve']")).toBeNull();
      const text = document.querySelector(".sel")!.textContent!;
      expect(text).not.toMatch(/approve the story|approves it|to look|merge it to/i);
      if (deployed) {
        const [address, pull] = Array.from(document.querySelectorAll<HTMLAnchorElement>("[data-why='preview'] a"));
        expect(address.href).toBe(ADDRESS);
        expect(address.textContent).toBe(ADDRESS);
        expect(pull.textContent).toBe(PR);
        expect(pull.href).toBe(preview.pull_url);
        expect($("[data-why='preview'] code")!.textContent).toBe("1".repeat(12));
      } else {
        expect($("[data-why='preview']")).toBeNull();
      }

      unmount(app!);
      document.body.innerHTML = "";
      app = mount(GraphHarness, { target: document.body, props: { initial: ui(fields, state), initialView: { epic: C, selected: null, showCross: true } } });
      await settle();
      expect($(`[data-key='${UI}'] .meta`)!.textContent).toBe(meta);
    });
  }

  it("says its preview comes once CI passes while it waits for the click-proofs without one", async () => {
    select(ui({ plan_status: "click_proofs", frozen: true, preview: null }, "In Review"), UI);
    await settle();
    expect($("[data-why='click_proofs']")!.textContent).toContain("Its preview is deployed once CI passes on its head.");
  });
});
