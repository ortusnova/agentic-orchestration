/** What each worker host does and what waits on it, as the backend computes it
 * from every Sortie's state (agent-platform/run-supervision). */

export type Phase = "build" | "review" | "fix" | "live_proof" | "deploy";
export type WorkerVerdict = "stuck" | "moving" | "idle" | "draining";
export type HoldKind = "question" | "budget" | "permission" | "hold_expired";
export type WaitKind = "slot" | "capability" | "live_proof" | "supervisor" | "drain";
export type PullWait = "review" | "ci" | "conflict" | "live_proof" | "person" | "none";
export type Action = "answer" | "sessions" | "open" | "pull" | "story" | null;

export interface Size { cpu?: number; mem_gib?: number; disk_gib?: number }

/** A lease the supervisor holds on a story in Sortie: the story waits on the
 * supervisor until it releases it, as long as it renews the lease with new
 * evidence before it runs out. Times in Unix seconds. */
export interface Lease {
  identifier: string;
  sortie: string;
  by: string | null;
  reason: string | null;
  evidence: string | null;
  held_since: number | null;
  renewed_at: number | null;
  until: number | null;
  expired: boolean;
  expired_at: number | null;
}

export interface StoryRef {
  identifier: string | null;
  key: string | null;
  label: string | null;
  title: string | null;
}

export interface PullRef { number: number; repository: string | null; url: string | null; waits_on: PullWait }

export interface Job extends StoryRef {
  sortie: string;
  worker: string;
  phase: Phase;
  phase_text: string;
  stage: string | null;
  /** The step the job reports, and when, in Unix seconds. */
  step: string | null;
  step_at: number | null;
  started_at: number;
  elapsed: number;
  /** When it is expected to end; estimated from its phase when Sortie gives no estimate. */
  ends_at: number;
  estimated: boolean;
  session_id: string | null;
  watchable: boolean;
  hold: { kind: HoldKind; reason: string; since: number | null; action: Action } | null;
  pull: { number: number; repository: string | null; url: string | null } | null;
  slot?: number;
  status_text?: string | null;
}

export interface Stuck extends StoryRef {
  sortie: string;
  worker: string;
  kind: HoldKind;
  reason: string;
  since: number | null;
  action: Action;
  question?: string | null;
  slot?: number | null;
  phase?: Phase | null;
  /** The supervisor's lease that ran out, for a hold_expired story. */
  lease?: Lease | null;
}

/** A run a slot ran before now, from its Sortie's run history. Times in Unix seconds. */
export interface PastRun extends StoryRef {
  started_at: number;
  ended_at: number;
  status: string | null;
  rule: string | null;
  phase: Phase;
  phase_text: string;
  error: string | null;
}

/** A job that waits for a slot, placed where the forecast expects it to run:
 * in the slot that frees first on a worker that can take it. */
export interface Projected extends StoryRef {
  starts_at: number;
  ends_at: number;
  /** Its expected length, in seconds, and what that comes from. */
  estimate: number;
  basis: string;
  rule: string | null;
  since: number | null;
}

/** A job that waits for a slot that no slot can take: no worker can run it, or its workers drain. */
export interface Waiting extends StoryRef {
  sortie: string;
  kind: string;
  reason: string;
  release: string | null;
  since: number | null;
}

export interface Lane {
  slot: number; job: Job | null; stuck: Stuck | null; extra: boolean;
  /** Absent from an older backend. */
  past?: PastRun[];
  next?: Projected[];
}

export interface Wait extends StoryRef {
  sortie: string;
  kind: WaitKind;
  reason: string;
  release: string | null;
  since: number | null;
  pull?: PullRef | null;
  /** The supervisor's lease, for a story that waits on the supervisor. */
  lease?: Lease | null;
}

export interface Reason {
  code: string;
  text: string;
  identifier?: string | null;
  key?: string | null;
  action?: Action;
  url?: string | null;
  worker?: string | null;
}

export interface Unmanaged { name: string; since: number | null; age: number | null; age_text: string; state: string | null }

export interface Worker {
  name: string;
  sortie: string;
  remote: boolean;
  last_seen: number | null;
  slots: number;
  slots_known: boolean;
  busy: number;
  capacity: Size | null;
  used: Size | null;
  verdict: WorkerVerdict;
  reasons: Reason[];
  lanes: Lane[];
  jobs: Job[];
  stuck: Stuck[];
  waits: Wait[];
  /** What it runs next: the first that wait on it for a slot or a live proof. */
  next: Wait[];
  unmanaged: Unmanaged[];
  draining: { reason: string | null; since: number | null } | null;
  /** Whether its Sortie serves its use, its unmanaged machines and its jobs' steps; absent from an older backend. */
  known?: { use: boolean; unmanaged: boolean; steps: boolean };
  /** Whether stories wait for a slot while its lanes are free because its Sortie's cap is reached; absent from an older backend. */
  capped?: boolean;
  /** Jobs the forecast places beyond its horizon, and past runs more than its slots held at once; absent from an older backend. */
  later?: number;
  past_hidden?: number;
  gone?: boolean;
}

export interface Pull extends StoryRef {
  number: number;
  repository: string | null;
  url: string | null;
  waits_on: PullWait;
  waits_text: string;
  reason: string | null;
  since: number | null;
  sortie: string;
  worker: string;
  stuck: boolean;
}

/** A field of Sortie's state the view reads that this Sortie does not serve, and what it cannot show without it. */
export interface Missing { field: string; text: string; sortie?: string }

export interface Fleet {
  generated_at?: number;
  sorties: { name: string; reachable: boolean; error: string | null; missing?: Missing[]; cap?: { limit: number; running: number } | null }[];
  workers: Worker[];
  pulls: Pull[];
  error?: string;
}

/** What the home's slot lanes say above the lanes. */
export interface Lanes {
  verdict: string | null;
  reasons: Reason[];
  busy: number;
  slots: number | null;
  /** Sortie's cap on agents at once, every worker's together; absent from an older backend. */
  cap?: number | null;
  runway: number | null;
  missing?: Missing[];
  generated_at?: number;
  workers: Worker[];
  /** The jobs no slot can take, and the runs that ended on no worker the workbench knows; absent from an older backend. */
  waiting?: Waiting[];
  unattributed?: number;
}

/** A story's run state, in its panel. */
export interface RunState {
  status: "running" | "stuck" | "waiting" | "pull";
  worker: string | null;
  sortie: string | null;
  slot: number | null;
  why: string;
  release: string | null;
  kind: string | null;
  action: Action;
  phase: Phase | null;
  step: string | null;
  started_at?: number;
  pull: (PullRef & { waits_text: string; reason: string | null; stuck: boolean }) | null;
  /** The supervisor's lease on it, live or expired; absent from an older backend. */
  lease?: Lease | null;
}

export const WORKER_VERDICT_TEXT: Record<WorkerVerdict, string> = {
  stuck: "stuck", moving: "moving", idle: "idle", draining: "draining",
};

export const WAIT_TEXT: Record<WaitKind, string> = {
  slot: "a slot", capability: "a capability", live_proof: "a live proof", supervisor: "the supervisor", drain: "the host to drain",
};

/** A time in UTC, to the minute: "14:00 UTC", with the date when it is not today's. */
export function clockText(t: number, now: number): string {
  const iso = new Date(t * 1000).toISOString();
  const day = iso.slice(0, 10) === new Date(now * 1000).toISOString().slice(0, 10) ? "" : `${iso.slice(0, 10)} `;
  return `${day}${iso.slice(11, 16)} UTC`;
}

/** What a live lease says beside its reason: the evidence, when it was last
 * renewed and when it runs out. */
export function leaseText(lease: Lease, now: number): string {
  const parts = [`evidence: ${lease.evidence ?? "none given"}`];
  if (lease.renewed_at != null) parts.push(`renewed ${duration(now - lease.renewed_at)} ago`);
  if (lease.until != null) {
    parts.push(lease.until > now ? `runs out at ${clockText(lease.until, now)}, in ${duration(lease.until - now)}`
      : `ran out at ${clockText(lease.until, now)}`);
  }
  return parts.join(" · ");
}

/** A duration in words: "40 s", "25 min", "2.0 h". */
export function duration(seconds: number | null | undefined): string {
  if (seconds == null || !Number.isFinite(seconds)) return "—";
  const s = Math.max(0, Math.round(seconds));
  if (s < 90) return `${s} s`;
  if (s < 5400) return `${Math.round(s / 60)} min`;
  if (s < 172800) return `${(s / 3600).toFixed(1)} h`;
  return `${Math.round(s / 86400)} d`;
}

const gib = (n: number | undefined) => (n == null ? "?" : Number.isInteger(n) ? `${n}` : n.toFixed(1));

/** A worker's capacity and use in words, each resource apart. */
export function capacityText(w: Pick<Worker, "slots" | "busy" | "capacity" | "used" | "known" | "sortie">): string[] {
  const out = [`${w.busy} / ${w.slots} slots`];
  if (w.known && !w.known.use && !w.capacity) {
    out.push(`CPU, memory and disk: ${w.sortie}'s Sortie does not report them`);
  } else if (w.capacity && w.known && !w.known.use) {
    out.push(`${gib(w.capacity.cpu)} CPU`, `${gib(w.capacity.mem_gib)} GiB memory`, `${gib(w.capacity.disk_gib)} GiB disk`,
      `use: ${w.sortie}'s Sortie does not report it`);
  } else if (w.capacity) {
    const u = w.used ?? {};
    out.push(`${gib(u.cpu)} / ${gib(w.capacity.cpu)} CPU`, `${gib(u.mem_gib)} / ${gib(w.capacity.mem_gib)} GiB memory`,
      `${gib(u.disk_gib)} / ${gib(w.capacity.disk_gib)} GiB disk`);
  }
  return out;
}

/** What a Sortie does not serve, in one sentence: what the view cannot show, and the field it needs. */
export function missingText(sortie: string, missing: Missing[]): string {
  const what = missing.map((m) => `${m.text} (${m.field})`).join("; ");
  return `${sortie}'s Sortie does not serve ${missing.length === 1 ? "this field" : "these fields"} yet, so the view cannot show ${what}.`;
}

/** The lanes' axis, in seconds around now: 30 minutes back to an hour ahead, now at one third. */
export const LANE_FROM = -1800;
export const LANE_TO = 3600;

/** The time window of the lanes around now. */
export function laneWindow(now: number): { start: number; end: number } {
  return { start: now + LANE_FROM, end: now + LANE_TO };
}

/** Where a time sits on the lanes' axis, in percent, clipped to it. */
export function lanePct(t: number, now: number): number {
  const m = Math.max(LANE_FROM, Math.min(LANE_TO, t - now));
  return ((m - LANE_FROM) / (LANE_TO - LANE_FROM)) * 100;
}

/** The axis's ticks: every quarter hour, now among them. */
export const LANE_TICKS: { at: number; label: string }[] = [
  { at: -1800, label: "−30 min" }, { at: -900, label: "−15" }, { at: 0, label: "NOW" }, { at: 900, label: "+15" },
  { at: 1800, label: "+30" }, { at: 2700, label: "+45" }, { at: 3600, label: "+1 h" },
];

/** Minutes, rounded, at least one: "~8 min". */
export function minutes(seconds: number): string {
  return `${Math.max(1, Math.round(seconds / 60))} min`;
}

/** A box's left edge and width in the window, as percentages, clipped to it. */
export function boxGeometry(from: number, to: number, win: { start: number; end: number }): { left: number; width: number } {
  const span = Math.max(1, win.end - win.start);
  const a = Math.min(Math.max(from, win.start), win.end);
  const b = Math.min(Math.max(to, a), win.end);
  return { left: ((a - win.start) / span) * 100, width: Math.max(1.5, ((b - a) / span) * 100) };
}

/** What a lane says in words, for a screen reader and a tooltip. */
export function laneText(lane: Lane): string {
  if (lane.stuck) return `slot ${lane.slot}: stuck, ${lane.stuck.label} ${lane.stuck.reason}`;
  if (lane.job) {
    const j = lane.job;
    return `slot ${lane.slot}: ${j.label}, ${j.phase_text}${j.step ? ` at ${j.step}` : ""}, for ${duration(j.elapsed)}`;
  }
  return `slot ${lane.slot}: free`;
}

/** What releases a wait, as one sentence: Sortie serves a whole sentence
 * ("Released when a running session ends."), the workbench a phrase ("your answer"). */
export function releaseText(release: string, lead: "by" | "when" = "by"): string {
  const r = release.trim();
  if (/^released\b/i.test(r)) return /[.!?]$/.test(r) ? r : `${r}.`;
  return `Released ${lead} ${r.replace(/[.]$/, "")}.`;
}

/** The button a stuck box or a reason offers, in words. */
export const ACTION_TEXT: Record<Exclude<Action, null>, string> = {
  answer: "Answer", sessions: "More sessions", open: "Open", pull: "Open pull request", story: "Open story",
};
