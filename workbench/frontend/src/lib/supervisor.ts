/** Sortie's supervisor in words (agent-platform/run-supervision). */
import type { SupervisorAction, SupervisorStatus } from "./types";

/** What each action the supervisor's token allows reads as, done, by Sortie's name for it. */
const ACTION_TEXT: Record<string, string> = {
  answer: "Answered a question",
  release: "Released a parked story",
  grant_sessions: "Granted sessions",
  record_merge: "Recorded a merge",
  fix_run: "Started a fix run",
  mark_draft: "Marked the pull request a draft",
  merge: "Merged the pull request",
  file_issue: "Filed an issue",
  live_proof: "Requested a live proof",
  hold: "Held a story",
  release_hold: "Released a hold",
  ask_person: "Asked a person",
};

/** The same actions, tried and not done. */
const TRIED_TEXT: Record<string, string> = {
  answer: "answer a question",
  release: "release a parked story",
  grant_sessions: "grant sessions",
  record_merge: "record a merge",
  fix_run: "start a fix run",
  mark_draft: "mark the pull request a draft",
  merge: "merge the pull request",
  file_issue: "file an issue",
  live_proof: "request a live proof",
  hold: "hold a story",
  release_hold: "release a hold",
  ask_person: "ask a person",
};

/** A refused route Sortie names by its method and pattern, such as "POST /api/v1/plan/sync". */
const ROUTE = /^(GET|POST|PUT|PATCH|DELETE) \//;

const words = (code: string) => code.replace(/[_-]+/g, " ").trim();

/** The action in a few words: what it did, or what it tried and could not do. */
export function actionText(a: SupervisorAction): string {
  if (a.result === "done" || a.result === "ok") return ACTION_TEXT[a.action] ?? capital(words(a.action));
  if (ROUTE.test(a.action)) return `Tried ${a.action}`;
  return `Tried to ${TRIED_TEXT[a.action] ?? words(a.action)}`;
}

const RESULT_TEXT: Record<string, string> = { done: "done", ok: "done", refused: "refused", rejected: "rejected", failed: "failed" };

export function resultText(result: string): string {
  return RESULT_TEXT[result] ?? words(result);
}

/** "ok", "refused" or "failed": the tone its result shows in. A rejected
 * action was the supervisor's to take, but Sortie found it did not hold. */
export function resultTone(result: string): "ok" | "refused" | "failed" {
  if (result === "done" || result === "ok") return "ok";
  return result === "refused" || result === "rejected" ? "refused" : "failed";
}

/** Sortie's supervisor-session statuses, an older Sortie's "active", and the workbench's "off" for a Sortie that serves none. */
const STATUS_TEXT: Record<string, string> = {
  idle: "Idle", running: "Running", paused: "Paused", budget_exhausted: "Out of budget", unavailable: "Unavailable",
  active: "Acting", off: "Off",
};

export function statusText(s: SupervisorStatus): string {
  return STATUS_TEXT[s.status] ?? capital(words(s.status));
}

/** Whether a person may pause it now, or resume it; neither when it is off. */
export const canPause = (s: SupervisorStatus) => s.status !== "paused" && s.status !== "off";
export const canResume = (s: SupervisorStatus) => s.status === "paused";

export function when(at: number | null): string {
  return at == null ? "" : new Date(at * 1000).toLocaleString();
}

export const iso = (at: number | null) => (at == null ? undefined : new Date(at * 1000).toISOString());

/** The time of day, "18:06"; with the day too, "Nov 2", when it was not today. */
export function clock(at: number, now = Date.now()): { time: string; day: string | null } {
  const d = new Date(at * 1000);
  const time = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  const today = new Date(now).toDateString() === d.toDateString();
  return { time, day: today ? null : d.toLocaleDateString([], { month: "short", day: "numeric" }) };
}

/** Sortie's "code: message" for an action it declined, split; a detail in other words has no code. */
export function detailOf(detail: string): { code: string | null; message: string } {
  const m = /^([a-z][a-z0-9_]*): ([\s\S]*)$/.exec(detail);
  return m ? { code: m[1], message: m[2] } : { code: null, message: detail };
}

const URL_IN_TEXT = /https?:\/\/[^\s<>"')\]]+/g;

/** Evidence text in parts, each address a link; a GitHub address reads by its path in the owner's repositories. */
export function linkParts(text: string): { text: string; url: string | null }[] {
  const out: { text: string; url: string | null }[] = [];
  let last = 0;
  for (const m of text.matchAll(URL_IN_TEXT)) {
    const url = m[0].replace(/[.,;:]+$/, "");
    if (m.index > last) out.push({ text: text.slice(last, m.index), url: null });
    out.push({ text: url.replace(/^https:\/\/github\.com\/[^/]+\//, ""), url });
    last = m.index + url.length;
  }
  if (last < text.length) out.push({ text: text.slice(last), url: null });
  return out;
}

function capital(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}
