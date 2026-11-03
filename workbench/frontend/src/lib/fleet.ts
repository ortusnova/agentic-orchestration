/** What each worker host does and what waits on it, as the backend computes it
 * from every Sortie's state (agent-platform/run-supervision). */

export type Phase = "build" | "review" | "fix" | "live_proof" | "deploy";
export type WorkerVerdict = "stuck" | "moving" | "idle" | "draining";
export type HoldKind = "question" | "budget" | "permission";
export type WaitKind = "slot" | "capability" | "live_proof" | "drain";
export type PullWait = "review" | "ci" | "conflict" | "live_proof" | "person" | "none";
export type Action = "answer" | "sessions" | "open" | "pull" | "story" | null;

export interface Size { cpu?: number; mem_gib?: number; disk_gib?: number }

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
  phase?: Phase;
}

export interface Lane { slot: number; job: Job | null; stuck: Stuck | null; extra: boolean }

export interface Wait extends StoryRef {
  sortie: string;
  kind: WaitKind;
  reason: string;
  release: string | null;
  since: number | null;
  pull?: PullRef | null;
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
  sorties: { name: string; reachable: boolean; error: string | null; missing?: Missing[] }[];
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
  runway: number | null;
  missing?: Missing[];
  generated_at?: number;
  workers: Worker[];
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
}

export const WORKER_VERDICT_TEXT: Record<WorkerVerdict, string> = {
  stuck: "stuck", moving: "moving", idle: "idle", draining: "draining",
};

export const WAIT_TEXT: Record<WaitKind, string> = {
  slot: "a slot", capability: "a capability", live_proof: "a live proof", drain: "the host to drain",
};

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

/** The time window of the lanes: the last hours and the next one, around now. */
export function laneWindow(workers: Worker[], now: number): { start: number; end: number } {
  const HOUR = 3600;
  const starts = workers.flatMap((w) => w.jobs.map((j) => j.started_at));
  const ends = workers.flatMap((w) => w.jobs.map((j) => j.ends_at));
  const start = Math.min(now - 2 * HOUR, ...starts.map((s) => Math.max(s, now - 8 * HOUR)));
  const end = Math.max(now + HOUR, ...ends.map((e) => Math.min(e, now + 8 * HOUR)));
  return { start, end };
}

/** A box's left edge and width in the window, as percentages, clipped to it. */
export function boxGeometry(from: number, to: number, win: { start: number; end: number }): { left: number; width: number } {
  const span = Math.max(1, win.end - win.start);
  const a = Math.min(Math.max(from, win.start), win.end);
  const b = Math.min(Math.max(to, a), win.end);
  return { left: ((a - win.start) / span) * 100, width: Math.max(1.5, ((b - a) / span) * 100) };
}

/** The ticks of the window, one per hour. */
export function hourTicks(win: { start: number; end: number }, now: number): { at: number; left: number; label: string }[] {
  const out = [];
  const span = Math.max(1, win.end - win.start);
  for (let h = Math.ceil((win.start - now) / 3600); now + h * 3600 <= win.end; h++) {
    const at = now + h * 3600;
    out.push({ at, left: ((at - win.start) / span) * 100, label: h === 0 ? "now" : `${h > 0 ? "+" : ""}${h} h` });
  }
  return out;
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

/** The button a stuck box or a reason offers, in words. */
export const ACTION_TEXT: Record<Exclude<Action, null>, string> = {
  answer: "Answer", sessions: "More sessions", open: "Open", pull: "Open pull request", story: "Open story",
};
