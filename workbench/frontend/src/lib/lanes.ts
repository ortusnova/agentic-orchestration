/** The workers' slot lanes on the home: one lane per agent slot, in time
 * (agent-platform/run-supervision). The backend reads them from each Sortie;
 * this module places them on the axis. */

import type { Question } from "./types";

export type StuckCode = "held" | "stalled" | "retrying" | "budget" | "question";

export interface Estimate { seconds: number; basis: string }

export interface LaneJob {
  identifier: string;
  key: string | null;
  title: string;
  phase: string;
  rule: string | null;
  started_at?: number | null;
  estimate?: Estimate | null;
  last_event?: string | null;
  last_event_at?: number | null;
  tokens?: number | null;
  turns?: number | null;
  session_id?: string | null;
  watchable?: boolean;
  repository?: string | null;
  status?: string | null;
  /** A retry: when it runs again, and its attempt. */
  due_at?: number | null;
  attempt?: number | null;
  /** Stuck work: since when it waits. */
  since?: number | null;
  /** A parked story: its whole question, for the answer box its lane opens. */
  question?: Question | null;
  /** A story held at a budget: what it used of which. */
  budget?: { reason: string; used: number | null; sessions: number | null } | null;
}

export interface LaneNext { identifier: string; key: string | null; title: string; phase: string; rule: string | null; estimate: Estimate | null }

export interface Lane {
  slot: number;
  kind: "job" | "retry" | "stuck" | "free";
  job: LaneJob | null;
  stuck: { code: StuckCode; text: string } | null;
  /** A free lane: what runs next in it, or why nothing does. */
  reason: string | null;
  next: LaneNext[];
}

export interface LaterItem {
  kind: "blocked" | "waiting" | "ready" | "you" | "held";
  identifier: string;
  key: string | null;
  title: string;
  text: string;
  status?: string | null;
  pull?: { number: number | null; url: string | null; repository: string | null };
  since?: number | null;
  /** It makes its worker stuck. */
  stuck?: boolean | null;
  /** Stuck work that waits below the lanes: why, and what its action needs. */
  code?: StuckCode;
  question?: Question | null;
  budget?: LaneJob["budget"];
}

export type WorkerVerdict = "working" | "stuck" | "idle" | "paused" | "unreachable";

export interface Worker {
  name: string;
  /** The Sortie that also serves the plan. */
  main: boolean;
  /** False when the main Sortie runs agents for another project only. */
  dispatched: boolean;
  reachable?: boolean;
  error?: string | null;
  slots?: number | null;
  busy?: number;
  started_at?: number | null;
  poll_seconds?: number | null;
  paused?: string | null;
  verdict?: WorkerVerdict;
  reasons?: { code: string; text: string; identifier: string | null; key: string | null }[];
  lanes?: Lane[];
  later?: LaterItem[];
  /** Whether its Sortie grants sessions; null until asked. */
  can_grant?: boolean | null;
  /** Its workflow's agent.max_sessions. */
  max_sessions?: number | null;
}

/** The axis, in minutes from now: half an hour back to an hour ahead. */
export const FROM = -30;
export const TO = 60;
export const TICKS: [number, string][] = [[-30, "−30 min"], [-15, "−15"], [0, "now"], [15, "+15"], [30, "+30"], [45, "+45"], [60, "+1 h"]];

/** A minute's place on the axis, in percent, clamped to it. */
export const pct = (m: number) => ((Math.max(FROM, Math.min(TO, m)) - FROM) / (TO - FROM)) * 100;

export interface Box {
  left: number;
  width: number;
  /** Where now falls inside the box, in percent of it: run so far before, estimate after. */
  split: number;
  /** It started before the axis begins. */
  cut: boolean;
  /** Minutes left, when an estimate says. */
  left_min: number | null;
  overdue: boolean;
}

/** A running job's box: from its start to its estimated end, or to now
 * when no estimate can say. */
export function jobBox(job: LaneJob, now: number): Box {
  const start = job.started_at != null ? (job.started_at - now) / 60 : 0;
  const est = job.estimate?.seconds;
  let end = est != null ? start + est / 60 : 0;
  const overdue = est != null && end < 0;
  if (end < 0) end = 0;
  const l = pct(start), r = pct(end), at = pct(0);
  return {
    left: l, width: Math.max(r - l, 0), split: r > l ? Math.round(((at - l) / (r - l)) * 100) : 100,
    cut: start < FROM, left_min: est != null && !overdue ? Math.max(0, Math.round(end)) : null, overdue,
  };
}

/** A retry's box: from now to when it runs again, and on for as long as a
 * run of its rule takes, when that is known. */
export function retryBox(job: LaneJob, now: number): Box {
  const due = job.due_at != null ? Math.max(0, (job.due_at - now) / 60) : 0;
  const end = due + (job.estimate ? job.estimate.seconds / 60 : 5);
  const l = pct(0), r = pct(end);
  return { left: l, width: r - l, split: 0, cut: false, left_min: Math.round(due), overdue: false };
}

/** A next job's box, starting now, as long as a run of its rule takes. */
export function nextBox(n: LaneNext): Box & { known: boolean } {
  const len = n.estimate ? n.estimate.seconds / 60 : 15;
  const l = pct(0), r = pct(len);
  return { left: l, width: r - l, split: 0, cut: false, left_min: n.estimate ? Math.round(len) : null, overdue: false, known: !!n.estimate };
}

/** "23 min", "1 h 05". */
export function duration(seconds: number): string {
  const m = Math.max(0, Math.round(seconds / 60));
  return m >= 60 ? `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, "0")}` : `${m} min`;
}

/** "20 s", "4 min", "2 h 10": how long ago. */
export function ago(seconds: number): string {
  if (seconds < 60) return `${Math.max(0, Math.round(seconds))} s`;
  return duration(seconds);
}

/** "1.8 M", "410 k". */
export function tokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)} M`;
  if (n >= 1000) return `${Math.round(n / 1000)} k`;
  return String(n);
}

/** The lane label's state words, as the prototype reads them. */
export function laneState(lane: Lane): string {
  if (lane.kind === "free") return "Free";
  const phase = lane.job?.phase ?? "";
  switch (lane.stuck?.code) {
    case "held": return `${phase} · held`;
    case "stalled": return `${phase} · stalled`;
    case "retrying": return `${phase} · retrying${lane.job?.attempt ? `, attempt ${lane.job.attempt}` : ""}`;
    case "budget": return "Stuck · session budget";
    case "question": return "Stuck · asks you";
    default: return `${phase} · working`;
  }
}

/** What a person does about stuck work in a lane. */
export function stuckAction(code: StuckCode | undefined): string {
  switch (code) {
    case "question": return "answer its question";
    case "budget": return "give it more sessions, or take the story over";
    case "held": return "decide on what holds it";
    case "stalled": return "look at its session: the agent stopped reporting";
    case "retrying": return "nothing yet: Sortie retries it";
    default: return "";
  }
}

export const VERDICT_WORD: Record<WorkerVerdict, string> = {
  working: "moving", stuck: "stuck", idle: "idle", paused: "paused", unreachable: "unreachable",
};

/** The worker's line of host facts. */
export function hostLine(w: Worker, now: number): string {
  const parts = [w.main ? "serves the plan and runs agents" : "runs agents"];
  if (w.started_at) parts.push(`Sortie up ${duration(now - w.started_at)}`);
  if (w.poll_seconds) parts.push(`polls every ${Math.round(w.poll_seconds)} s`);
  return parts.join(" · ");
}

/** Blocked stories that wait on the same work share one entry. */
export function groupLater(items: LaterItem[]): (LaterItem & { identifiers: string[] })[] {
  const out: (LaterItem & { identifiers: string[] })[] = [];
  for (const i of items) {
    const same = i.kind === "blocked" ? out.find((o) => o.kind === "blocked" && o.text === i.text) : undefined;
    if (same) same.identifiers.push(i.identifier);
    else out.push({ ...i, identifiers: [i.identifier] });
  }
  return out;
}
