/** Read-only helpers over a merged graph: status, progress, badges, neighbours. */
import type { GraphNode, MergedGraph } from "./types";

export type StatusClass = "done" | "prog" | "todo" | "cancelled";

const ACTIVE_STATE = /progress|review|doing|active|started/i;

export interface Badge {
  kind: "run" | "retry" | "budget";
  label: string;
}

export class GraphModel {
  readonly byKey: Map<string, GraphNode>;
  readonly epics: GraphNode[];
  private readonly blockersOf = new Map<string, string[]>();
  private readonly blockedOf = new Map<string, string[]>();
  private readonly childrenOf = new Map<string, GraphNode[]>();

  constructor(readonly doc: MergedGraph) {
    this.byKey = new Map(doc.nodes.map((n) => [n.plan_key, n]));
    this.epics = doc.nodes.filter((n) => n.kind === "epic");
    for (const b of doc.blocks) {
      (this.blockersOf.get(b.blocked) ?? this.blockersOf.set(b.blocked, []).get(b.blocked)!).push(b.blocker);
      (this.blockedOf.get(b.blocker) ?? this.blockedOf.set(b.blocker, []).get(b.blocker)!).push(b.blocked);
    }
    const parentOf = new Map(doc.parents.map((p) => [p.child, p.parent]));
    for (const n of doc.nodes) {
      if (n.kind !== "story") continue;
      const parent = parentOf.get(n.plan_key) ?? n.change;
      (this.childrenOf.get(parent) ?? this.childrenOf.set(parent, []).get(parent)!).push(n);
    }
  }

  node(key: string | null | undefined): GraphNode | undefined {
    return key ? this.byKey.get(key) : undefined;
  }

  storiesOf(epicKey: string): GraphNode[] {
    return this.childrenOf.get(epicKey) ?? [];
  }

  blockers(key: string): string[] {
    return (this.blockersOf.get(key) ?? []).filter((k) => this.byKey.has(k));
  }

  unblocks(key: string): string[] {
    return (this.blockedOf.get(key) ?? []).filter((k) => this.byKey.has(k));
  }

  epicOf(n: GraphNode): GraphNode | undefined {
    return n.kind === "epic" ? n : this.byKey.get(this.doc.parents.find((p) => p.child === n.plan_key)?.parent ?? n.change);
  }

  /** A story Sortie cancelled: in the tracker's cancel state. It is closed, but not done. */
  isCancelled(n: GraphNode | undefined): boolean {
    const cancel = this.doc.plan_states?.cancel;
    return !!n && n.kind === "story" && !!n.tracker?.terminal && !!cancel && n.tracker.state.toLowerCase() === cancel.toLowerCase();
  }

  statusClass(n: GraphNode): StatusClass {
    if (n.kind === "epic") {
      // A cancelled story no longer counts toward its epic.
      const stories = this.storiesOf(n.plan_key).filter((s) => !this.isCancelled(s));
      if (n.tracker?.terminal || (stories.length > 0 && stories.every((s) => this.isDone(s)))) return "done";
      if (stories.some((s) => this.statusClass(s) !== "todo") || (n.tracker && ACTIVE_STATE.test(n.tracker.state))) return "prog";
      return "todo";
    }
    // Its pull requests delivered it: done, wherever the work was done.
    if (n.plan_status === "delivered") return "done";
    if (this.isCancelled(n)) return "cancelled";
    if (n.tracker?.terminal) return "done";
    if (n.runtime && n.runtime.status !== "budget_held") return "prog";
    if (n.tracker && ACTIVE_STATE.test(n.tracker.state)) return "prog";
    return "todo";
  }

  isDone(n: GraphNode | undefined): boolean {
    return !!n && this.statusClass(n) === "done";
  }

  statusLabel(n: GraphNode): string {
    if (n.plan_status === "delivered") return "Done";
    if (n.plan_status === "delivery_unconfirmed") return "Delivery not confirmed";
    if (n.tracker) return n.tracker.state;
    return n.status === "draft" ? "Not approved" : "To do";
  }

  progress(epic: GraphNode): { done: number; total: number } {
    const stories = this.storiesOf(epic.plan_key).filter((s) => !this.isCancelled(s));
    return { done: stories.filter((s) => this.isDone(s)).length, total: stories.length };
  }

  badge(n: GraphNode): Badge | null {
    if (n.kind === "story") return runtimeBadge(n);
    const badges = this.storiesOf(n.plan_key).map(runtimeBadge).filter((b): b is Badge => b !== null);
    const running = badges.filter((b) => b.kind === "run").length;
    if (running) return { kind: "run", label: running === 1 ? "1 agent running" : `${running} agents running` };
    if (badges.some((b) => b.kind === "retry")) return { kind: "retry", label: "agent retrying" };
    if (badges.length) return { kind: "budget", label: "budget held" };
    return null;
  }

  /** Not done, not started, and every blocker done. */
  isReady(n: GraphNode): boolean {
    // A story waiting for a person (not approved, held, or in re-review) is not ready, whatever its blockers.
    if (n.plan_status && !["ready", "released"].includes(n.plan_status)) return false;
    return this.statusClass(n) === "todo" && !n.runtime && this.blockers(n.plan_key).every((b) => this.isDone(this.byKey.get(b)));
  }

  identifier(n: GraphNode): string {
    return n.tracker?.identifier ?? n.plan_key;
  }
}

function runtimeBadge(n: GraphNode): Badge | null {
  switch (n.runtime?.status) {
    case "running":
      return { kind: "run", label: `running · turn ${n.runtime.turn_count ?? 0}` };
    case "retrying":
      return { kind: "retry", label: `retrying · attempt ${n.runtime.attempt ?? 1}` };
    case "budget_held":
      return { kind: "budget", label: "budget held" };
    default:
      return null;
  }
}
