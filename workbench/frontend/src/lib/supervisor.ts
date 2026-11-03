/** Sortie's supervisor in words (agent-platform/run-supervision). */
import type { Supervisor, SupervisorAction } from "./types";

/** What each action the supervisor's token allows reads as, done. */
const ACTION_TEXT: Record<string, string> = {
  answer_question: "Answered a question",
  release_park: "Released a parked story",
  grant_sessions: "Granted sessions",
  record_merge: "Recorded a merge",
  start_fix_run: "Started a fix run",
  mark_draft: "Marked the pull request a draft",
  merge_pull: "Merged the pull request",
  file_issue: "Filed an issue",
  request_proof: "Requested a live proof",
};

const words = (code: string) => code.replace(/[_-]+/g, " ").trim();

/** The action in a few words: what it did, or what it tried and could not do. */
export function actionText(a: SupervisorAction): string {
  if (a.result === "done" || a.result === "ok") return ACTION_TEXT[a.action] ?? capital(words(a.action));
  return `Tried to ${words(a.action)}`;
}

const RESULT_TEXT: Record<string, string> = { done: "done", ok: "done", refused: "refused", failed: "failed" };

export function resultText(result: string): string {
  return RESULT_TEXT[result] ?? words(result);
}

/** "ok", "refused" or "failed": the tone its result shows in. */
export function resultTone(result: string): "ok" | "refused" | "failed" {
  if (result === "done" || result === "ok") return "ok";
  return result === "refused" ? "refused" : "failed";
}

const STATUS_TEXT: Record<string, string> = { active: "Acting", paused: "Paused", off: "Off" };

export function statusText(s: Supervisor): string {
  return STATUS_TEXT[s.status] ?? capital(words(s.status));
}

/** Whether a person may pause it now, or resume it; neither when it is off. */
export const canPause = (s: Supervisor) => s.status === "active";
export const canResume = (s: Supervisor) => s.status === "paused";

export function when(at: number | null): string {
  return at == null ? "" : new Date(at * 1000).toLocaleString();
}

export const iso = (at: number | null) => (at == null ? undefined : new Date(at * 1000).toISOString());

function capital(s: string): string {
  return s ? s[0].toUpperCase() + s.slice(1) : s;
}
