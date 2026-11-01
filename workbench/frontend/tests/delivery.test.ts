import { flushSync, mount, tick, unmount } from "svelte";
import { afterEach, describe, expect, it } from "vitest";
import ChatHarness from "./ChatHarness.svelte";
import GraphHarness from "./GraphHarness.svelte";
import { graph, node, synced } from "./fixtures";
import { GraphModel } from "../src/lib/model";
import { session as identity } from "../src/lib/identity.svelte";
import type { SessionClient } from "../src/lib/session";
import type { StoryActions } from "../src/lib/sync";
import type { DeliveryPull, GraphNode, MergedGraph } from "../src/lib/types";

const C = "run-homelab-through-sortie";
const IMAGE = `${C}/homelab-image`;
const PR = "ortusnova/homelab#85";
const SHA = "0123456789abcdef0123456789abcdef01234567";

function pull(state: DeliveryPull["state"], extra: Partial<DeliveryPull> = {}): DeliveryPull {
  return {
    ref: PR, state, url: "https://github.com/ortusnova/homelab/pull/85",
    merge_commit: state === "merged" ? SHA : null,
    merge_commit_url: state === "merged" ? `https://github.com/ortusnova/homelab/commit/${SHA}` : null,
    merged_at: state === "merged" ? "2026-10-30T12:00:00Z" : null,
    reason: state === "merged" ? null : state === "open" ? "it is open" : "GitHub answered 401: Bad credentials",
    ...extra,
  };
}

function delivered(): Partial<GraphNode> {
  return { plan_status: "delivered", approval: "draft", delivery: { state: "delivered", pulls: [pull("merged")] } };
}

function unconfirmed(state: DeliveryPull["state"] = "open"): Partial<GraphNode> {
  return { plan_status: "delivery_unconfirmed", approval: "draft", delivery: { state: "unconfirmed", pulls: [pull(state)] } };
}

function homelab(image: Partial<GraphNode>): MergedGraph {
  return graph(
    [
      node(C, { approval: "approved" }),
      node(IMAGE, { approval: "draft", ...image }),
      node(`${C}/homelab-pilot`, { approval: "approved", plan_status: "ready" }),
    ],
    [[IMAGE, `${C}/homelab-pilot`]],
  );
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

describe("a delivered story", () => {
  it("reads Done, counts toward its epic, and unblocks its dependent", () => {
    const m = new GraphModel(homelab(delivered()));
    const image = m.node(IMAGE)!;
    expect(m.statusClass(image)).toBe("done");
    expect(m.statusLabel(image)).toBe("Done");
    expect(m.progress(m.node(C)!)).toEqual({ done: 1, total: 2 });
    expect(m.isReady(m.node(`${C}/homelab-pilot`)!)).toBe(true);
  });

  it("links to the pull request and its merge commit, and offers no Approve", async () => {
    select(homelab(delivered()), IMAGE);
    await settle();
    expect($(".chips .chip.done")!.textContent).toContain("Done");
    const link = $<HTMLAnchorElement>(`[data-pull='${PR}'] a.ext-link`)!;
    expect(link.textContent).toContain(PR);
    expect(link.href).toBe("https://github.com/ortusnova/homelab/pull/85");
    expect($<HTMLAnchorElement>(`[data-commit='${SHA}']`)!.href).toBe(`https://github.com/ortusnova/homelab/commit/${SHA}`);
    expect($("[data-act='approve']")).toBeNull();
    expect($("[data-act='pull-back']")).toBeNull();
  });
});

describe("a delivery not confirmed", () => {
  it("reads Delivery not confirmed, naming the pull request and why", async () => {
    select(homelab(unconfirmed("open")), IMAGE);
    await settle();
    expect($(".chips .chip.todo")!.textContent).toContain("Delivery not confirmed");
    expect($("[data-why='delivery']")!.textContent).toBe(`Delivery not confirmed: ${PR}: it is open.`);
    expect($(`[data-pull='${PR}']`)!.textContent).toContain("it is open");
    expect($("[data-act='approve']")).toBeNull();
  });

  it("counts as not done, and holds its dependent", () => {
    const m = new GraphModel(homelab(unconfirmed("unknown")));
    expect(m.isDone(m.node(IMAGE))).toBe(false);
    expect(m.progress(m.node(C)!)).toEqual({ done: 0, total: 2 });
    expect(m.isReady(m.node(`${C}/homelab-pilot`)!)).toBe(false);
  });

  it("turns Done on the card when the merge arrives, without a reload", async () => {
    const h = mount(GraphHarness, { target: document.body, props: { initial: homelab(unconfirmed("open")), initialView: { epic: C, selected: null, showCross: true } } }) as
      ReturnType<typeof mount> & { setDoc: (d: MergedGraph) => void };
    app = h;
    await settle();
    const card = () => $(`[data-key='${IMAGE}']`)!;
    expect(card().querySelector(".st")!.textContent).toBe("Delivery not confirmed");
    expect(card().querySelector(".meta")!.textContent).toBe(`${PR} open`);
    expect(card().classList.contains("done")).toBe(false);
    h.setDoc({ ...homelab(delivered()), version: 2 });
    await settle();
    expect(card().querySelector(".st")!.textContent).toBe("Done");
    expect(card().classList.contains("done")).toBe(true);
  });

  it("reads Delivery not confirmed even when the tracker holds an issue for it", () => {
    const m = new GraphModel(graph([node(C), synced(IMAGE, "Todo", unconfirmed("closed"))]));
    expect(m.statusLabel(m.node(IMAGE)!)).toBe("Delivery not confirmed");
    expect(m.statusClass(m.node(IMAGE)!)).toBe("todo");
  });
});
