/** Sync and approval as the UI uses them; tests substitute a fake. */
import { projectApi } from "./api";
import type { Delivery, SyncStatus } from "./types";

/** The plan syncs on its own; a planner can only retry a failed sync. */
export interface SyncClient extends StoryActions {
  retry(): Promise<SyncStatus>;
}

/** What an approver does to stories. The backend checks the role. */
export interface StoryActions {
  approve(target: { keys?: string[]; epic?: string }): Promise<unknown>;
  pullBack(key: string): Promise<unknown>;
  cancel(key: string): Promise<unknown>;
  supersede(key: string): Promise<unknown>;
}

/** Sync and story actions of one project. */
export function httpSync(slug: string): SyncClient {
  const api = projectApi(slug);
  return {
    retry: () => api.retrySync(), approve: (t) => api.approve(t),
    pullBack: (k) => api.pullBack(k), cancel: (k) => api.cancel(k), supersede: (k) => api.supersede(k),
  };
}

const STATUS_LABELS: Record<string, string> = {
  draft: "Draft", held: "Held", ready: "Ready", released: "Released", frozen: "Started", needs_rereview: "Re-review",
  delivered: "Delivered", delivery_unconfirmed: "Not confirmed",
};

/** The short label of a plan status. */
export function planStatusLabel(s: string | null | undefined): string {
  return s ? (STATUS_LABELS[s] ?? s) : "";
}

/** Why a story needs re-review, in a sentence. */
export function rereviewText(r: { reason: string; ref: string } | null | undefined): string {
  if (!r) return "";
  if (r.reason === "blocker_canceled") return `Its blocker ${r.ref} was cancelled. Approve it again if it still makes sense, or cancel it.`;
  if (r.reason === "unverified_no_change") return `The agent said the work was already done, but its evidence could not be confirmed (${r.ref || "no citation"}). Supersede or cancel it.`;
  return r.ref ? `${r.reason}: ${r.ref}` : r.reason;
}

/** Why a story's pull requests do not confirm its delivery, in a sentence. */
export function deliveryText(d: Delivery | null | undefined): string {
  if (!d || d.state === "delivered") return "";
  const open = d.pulls.filter((p) => p.state !== "merged");
  return "Delivery not confirmed: " + open.map((p) => `${p.ref}: ${p.reason ?? p.state}`).join("; ") + ".";
}

/** The plan key an error points at, when the graph holds it. */
export function errorTarget(e: { change: string | null; deliverable: string | null; members: string[] }, has: (key: string) => boolean): string | null {
  const candidates = [
    e.change && e.deliverable ? `${e.change}/${e.deliverable}` : null,
    ...e.members,
    e.change,
  ].filter((k): k is string => !!k);
  return candidates.find(has) ?? null;
}
