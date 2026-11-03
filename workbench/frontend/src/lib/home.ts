/** The project home the backend computes with the graph (planning/project-home). */
import type { Lanes } from "./fleet";
import type { GraphNode, Question } from "./types";

export interface Count { done: number; total: number }

export interface MilestoneProgress {
  stories: Count;
  agent_tasks: Count;
  person_tasks: Count;
  metal_tasks: Count;
  operator_tasks: Count;
}

export interface HomeMilestone {
  id: string;
  title: string;
  /** "M1" from "M1: vm-complete". */
  short: string;
  goal: boolean;
  next: boolean;
  done: boolean;
  implicit: boolean;
  depends_on: string[];
  gate: string | null;
  changes: string[];
  /** The stories it waits on that no milestone, or only a later one, includes; absent from an older backend. */
  waits_on?: string[];
  progress: MilestoneProgress;
}

export type Verdict = "stuck" | "done" | "idle_now" | "will_idle" | "healthy";

export interface Health {
  /** Null when the sources cannot support a verdict. */
  verdict: Verdict | null;
  reasons: { code: string; text: string; key?: string | null }[];
  slots: number | null;
  busy: number | null;
  free: number | null;
  running: { identifier: string; key: string | null }[];
  held: { identifier: string; key: string | null; reason: string; holder: string | null }[];
  runway: number | null;
  history?: { end: number; busy: number | null; runway: number | null }[];
  /** What the released stories wait on, and who acts on each; absent from an older backend. */
  waits?: Wait[];
}

export type WaitKind = "review" | "merge" | "deploy" | "question" | "budget" | "hold";

/** One thing released stories wait on while no agent works on them (agent-platform/run-supervision). */
export interface Wait {
  kind: WaitKind;
  /** Review, merge, deploy: the pull request, which stories may share. */
  pr: { ref: string; url: string | null } | null;
  /** Budget, hold: why. */
  reason: string | null;
  stories: { key: string; identifier: string | null }[];
  /** Who acts on it, and what they do: "a reviewer", "reviews it". */
  who: string;
  does: string;
  /** All of it in words, as the backend says it. */
  text: string;
}

/** What a wait is, in words, after its pull request when it has one: "in review", "a held budget (token budget)". */
export function waitWhat(w: Wait): string {
  if (w.kind === "review") return w.pr ? "in review" : "a review";
  if (w.kind === "merge") return w.pr ? "to merge" : "a merge";
  if (w.kind === "deploy") return w.pr ? "to deploy" : "a deploy";
  if (w.kind === "question") return "a question";
  const what = w.kind === "budget" ? "a held budget" : "a held run";
  return w.reason ? `${what} (${w.reason})` : what;
}

export type Reason =
  | "no_breakdown" | "no_spec" | "needs_rereview" | "not_approved" | "held_for_approval"
  | "waits_on_person" | "waits_on_work" | "operator_task";

export type Column = "approve" | "spec" | "operator";

export interface TaskRef { id: string; change: string; number: string | null; text: string; kind: string; line: number }

export interface NotReadyItem {
  kind: "story" | "change" | "task";
  key: string;
  label: string;
  title: string;
  change?: string;
  milestone: string;
  reason: Reason;
  column: Column | null;
  slack: number;
  urgency: "now" | "soon" | "later";
  critical: boolean;
  dependents: number;
  order: number;
  rank: number | null;
  after?: string[];
  after_milestones?: string[];
  stories?: string[];
  task?: TaskRef;
  action: { kind: "approve" | "review" | "plan" | "task"; keys?: string[]; epic?: string; story?: string; task?: string };
}

/** An open question on the home, ranked before everything else that needs a person. */
export interface HomeQuestion extends Question {
  key: string;
  label: string;
  change: string;
  title: string;
  url: string | null;
  rank: number;
  /** The story the plan replaces this one with; closing it as replaced, not an answer, resolves the question. */
  replaced_by?: { key: string; label: string; title: string } | null;
}

/** What a Sortie's notifications sent the workbench (agent-platform/run-supervision), linked to its story and question. */
export interface HomeNotification {
  id: number;
  /** The host whose Sortie sent it. */
  host: string;
  /** Sortie's event type, such as session.stopped or escalation.ci_failure. */
  event_type: string;
  severity: string;
  category: string;
  title: string;
  body: string;
  /** What the agent said, such as the question it stopped on. */
  agent_text: string;
  identifier: string;
  escalation: { kind: string; rule: string; url: string } | null;
  /** When Sortie sent it, in Unix seconds. */
  sent_at: number;
  received_at: number;
  seen_at: number | null;
  seen_by: string | null;
  story: { key: string; label: string; change: string; title: string; url: string | null };
  /** The open question it is of, which is answered under Answer; null when it is of none. */
  question: { identifier: string; sortie: string | null; parked_at: number | null } | null;
  unread: boolean;
  /** Its question is still open four hours or more after it was sent. */
  again: boolean;
}

/** A notification's event in words: "stopped for a person", "CI still fails". */
export function eventText(n: Pick<HomeNotification, "event_type" | "escalation">): string {
  const words: Record<string, string> = {
    "session.stopped": "stopped for a person",
    "session.failed": "failed",
    "session.escalated": n.escalation?.kind === "question" ? "asks a person"
      : n.escalation?.kind === "permission" ? "asks permission" : "escalated to a person",
    "escalation.ci_failure": "CI still fails",
    "escalation.review_comments": "review comments need a person",
    "escalation.bot_review": "a bot review needs a person",
    "escalation.merge_conflicts": "still conflicts with its base",
    "escalation.auto_merge": "could not be merged",
    "escalation.merge_completion": "merged, but not finished",
    "budget.held": "held by its budget",
  };
  return words[n.event_type] ?? n.event_type.replace(/[._]/g, " ");
}

export interface Span {
  id: string;
  start: number;
  end: number;
  size: number;
  done: number;
  blockers: { key: string; label: string; reason: Reason; rank: number | null }[];
}

export interface HomeDoc {
  version?: number;
  generated_at: number;
  error?: string;
  plan: { valid: boolean; errors: number; has_graph: boolean };
  milestones_declared: boolean;
  story_progress: "live" | "last_known" | "unknown";
  health: Health;
  milestones: HomeMilestone[];
  goal: string | null;
  next: string | null;
  critical_path: {
    milestone: string; gate: string | null;
    stories: { key: string; label: string; title: string; change: string; needs: Reason | null; running: boolean; estimate: number | null;
      /** The milestone waits on it without including its change. */
      waited_on?: boolean }[];
  } | null;
  not_ready: NotReadyItem[];
  /** Absent from an older backend. */
  questions?: HomeQuestion[];
  /** What the Sorties' notifications sent: every unread one, and every one of an open question. Absent from an older backend. */
  notifications?: HomeNotification[];
  needs_person: { total: number; questions?: number; unread?: number; approve: number; approve_stories: number; spec: number; operator: number; nearest: string | null };
  timeline: { spans: Span[]; here: number; to_goal: number; axis: string } | null;
  /** The workers as slot lanes, and what they say of the whole. Absent from an older backend. */
  lanes?: Lanes | null;
}

export const REASON_TEXT: Record<Reason, string> = {
  no_breakdown: "no breakdown",
  no_spec: "no spec",
  needs_rereview: "needs re-review",
  not_approved: "not approved",
  held_for_approval: "held for approval",
  waits_on_person: "waits on a person",
  waits_on_work: "waits on work",
  operator_task: "operator task",
};

export const VERDICT_TEXT: Record<Verdict, string> = {
  stuck: "Stuck",
  done: "Done",
  idle_now: "Idle now",
  will_idle: "Will idle",
  healthy: "Healthy",
};

/** A tasks.md line as plain words: no emphasis marks, no code ticks. */
export function plainTask(text: string): string {
  return text.replace(/\*\*|__|`/g, "").replace(/\s+/g, " ").trim();
}

const n = (k: number, one: string, many: string) => `${k} ${k === 1 ? one : many}`;
const fmt = (k: number) => (Number.isInteger(k) ? String(k) : k.toFixed(1));

/** The slack in words: shorter is more urgent. */
export function slackText(i: NotReadyItem): string {
  if (i.slack <= 0) return "blocking now";
  const stories = i.slack === 1 ? "1 story" : `${fmt(i.slack)} stories`;
  if (i.kind === "task" && i.after_milestones?.length) return `blocks after ${i.after_milestones.join(", ")}, ~${stories}`;
  return `blocks in ${stories}`;
}

/** The slack bar's length, in percent: 4% when blocking now, full at 8 stories or more. */
export function slackWidth(slack: number): number {
  return Math.max(4, Math.min(100, slack * 12.5));
}

export const share = (c: Count) => (c.total ? c.done / c.total : 0);

/** A milestone's measures in words, never summed (D3). */
export function progressLines(m: HomeMilestone, storiesKnown = true): string[] {
  const p = m.progress;
  const out: string[] = [];
  if (p.stories.total) out.push(`${storiesKnown ? p.stories.done : "?"} / ${n(p.stories.total, "story", "stories")}`);
  if (p.agent_tasks.total) out.push(`${p.agent_tasks.done} / ${n(p.agent_tasks.total, "agent task", "agent tasks")}`);
  if (p.person_tasks.total) out.push(`${p.person_tasks.done} / ${n(p.person_tasks.total, "person task", "person tasks")}`);
  if (!out.length) out.push("nothing counted yet");
  return out;
}

/**
 * The timeline's visual scale. Each milestone's span is proportional to the
 * work it counts, with a floor so that every tick's label fits; a work share
 * maps to x through the same spans.
 */
export function visualSpans(spans: Span[], floor = 0.1): { id: string; x0: number; x1: number }[] {
  if (!spans.length) return [];
  const f = Math.min(floor, 1 / spans.length);
  const widths = spans.map((s) => s.end - s.start);
  // Raise the narrow spans to the floor, and take the difference from the wide ones in proportion.
  const small = widths.map((w) => w < f);
  const need = small.reduce((acc, isSmall, i) => acc + (isSmall ? f - widths[i] : 0), 0);
  const wide = widths.reduce((acc, w, i) => acc + (small[i] ? 0 : w), 0);
  const scaled = widths.map((w, i) => (small[i] ? f : wide ? w - (need * w) / wide : 1 / spans.length));
  const out = [];
  let at = 0;
  for (let i = 0; i < spans.length; i++) {
    out.push({ id: spans[i].id, x0: at, x1: at + scaled[i] });
    at += scaled[i];
  }
  return out;
}

/** Where a share of all the work sits on the visual scale. */
export function mapShare(share: number, spans: Span[], visual: { x0: number; x1: number }[]): number {
  for (let i = 0; i < spans.length; i++) {
    const s = spans[i];
    if (share <= s.end || i === spans.length - 1) {
      const t = s.end > s.start ? Math.min(1, Math.max(0, (share - s.start) / (s.end - s.start))) : 0;
      return visual[i].x0 + t * (visual[i].x1 - visual[i].x0);
    }
  }
  return 0;
}

/** What about a story the home shows: a change in it marks the story briefly (D9). */
export function storySignature(n: GraphNode): string {
  return [n.tracker?.state ?? "", n.plan_status ?? "", n.runtime?.status ?? "", n.approval ?? "", n.question ? "asks" : ""].join("|");
}

/** The stories whose state changed between two graphs. */
export function changedStories(before: Map<string, string>, nodes: GraphNode[]): { changed: string[]; now: Map<string, string> } {
  const now = new Map<string, string>();
  const changed: string[] = [];
  for (const node of nodes) {
    if (node.kind !== "story") continue;
    const sig = storySignature(node);
    now.set(node.plan_key, sig);
    const old = before.get(node.plan_key);
    if (before.size && old !== undefined && old !== sig) changed.push(node.plan_key);
  }
  return { changed, now };
}
