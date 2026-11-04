/** What the Sortie that holds a story does with it, and the actions that
 * unblock it (agent-platform/run-supervision: Unblock from the lane). Tests
 * substitute a fake client. */
import { ApiError, projectApi } from "./api";
import type { ChatItem } from "./chat";
import type { Question } from "./types";

export type RunStatus = "running" | "retrying" | "budget" | "parked" | "reaction" | "between" | "blocked" | "idle";
export type RunAction = "watch" | "last_run" | "grant" | "answer" | "open_pr" | "blockers" | null;

export interface PastRun {
  dispatch_id: string;
  status: string | null;
  rule: string | null;
  started_at: number | null;
  completed_at: number | null;
  error: string | null;
}

export interface RunPull { number: number | null; url: string | null; repository: string | null; waits: string; ready: boolean }

export interface RunState {
  sortie: string;
  identifier: string;
  status: RunStatus;
  headline: string | null;
  text: string | null;
  why: string[];
  releases: string | null;
  action: RunAction;
  question: Question | null;
  budget: { reason: string | null; used: number | null; sessions: number | null; since: number | null } | null;
  blockers: { identifier: string; state: string | null; reason: string | null }[];
  retry: { attempt: number | null; due_at: number | null; error: string | null } | null;
  running: { session_id: string | null; dispatch_id: string | null; rule: string | null; started_at: number | null; watchable: boolean; held: boolean; holder: string | null } | null;
  pulls: RunPull[];
  next: { condition: string; text: string }[];
  runs: PastRun[];
  can_grant: boolean | null;
  max_sessions: number | null;
}

/** One agent event Sortie kept for a run. */
export interface RunEvent {
  seq: number;
  at: string;
  event: string;
  message: string;
  text: string;
  tool_line?: string;
  tool_error?: boolean;
}

/** The answer to a grant: what it did, or why it could not. */
export type GrantResult = { ok: true } | { ok: false; unsupported: boolean; message: string };

export interface RunClient {
  state(identifier: string): Promise<RunState>;
  events(identifier: string, dispatchId: string): Promise<RunEvent[]>;
  grant(identifier: string, add: number, reason?: string): Promise<GrantResult>;
}

export function httpRun(slug: string): RunClient {
  const api = projectApi(slug);
  return {
    state: (id) => api.workerRun(id),
    events: async (id, d) => (await api.workerRunEvents(id, d)).events,
    async grant(id, add, reason) {
      try {
        await api.grantSessions(id, add, reason ?? "");
        return { ok: true };
      } catch (err) {
        if (err instanceof ApiError && err.code === "grant_unsupported") return { ok: false, unsupported: true, message: err.message };
        if (err instanceof ApiError && err.status === 403) return { ok: false, unsupported: false, message: "Granting sessions needs the approver role." };
        return { ok: false, unsupported: false, message: err instanceof Error ? err.message : "The grant did not go through." };
      }
    },
  };
}

/** What a Sortie that cannot grant sessions yet leaves a person to do. */
export function grantFallback(sortie: string, maxSessions: number | null): string {
  return `${sortie}'s Sortie cannot grant sessions yet. Raise agent.max_sessions${maxSessions ? ` (now ${maxSessions})` : ""} in ${sortie}'s workflow instead, and Sortie runs it again at its next poll.`;
}

/** A run's kept events as a transcript: the agent's messages, its tool
 * calls, and Sortie's notes. */
export function runItems(events: RunEvent[]): ChatItem[] {
  const out: ChatItem[] = [];
  for (const e of events) {
    const time = Date.parse(e.at) || 0;
    const key = `r${e.seq}`;
    if (e.event === "tool_result") out.push({ kind: "tool", key, title: e.tool_line || e.text, status: e.tool_error ? "failed" : "completed", time });
    // The agent's own messages arrive as notifications; Sortie's notes there
    // are short lowercase phrases ("verifying the agent credential").
    else if (e.event === "notification" && e.message && !/^[a-z]/.test(e.message)) out.push({ kind: "agent", key, text: e.message, time });
    else if (e.event === "turn_completed" || e.event === "token_usage") continue;
    else out.push({ kind: "notice", key, text: e.text || e.message || e.event, time });
  }
  return out;
}

/** "Parked on taichi", "Held at its session budget on p1". */
export function statusWord(s: RunState): string {
  switch (s.status) {
    case "running": return s.running?.held ? "Running, held by a person" : "Running";
    case "retrying": return s.retry?.attempt ? `Retrying, attempt ${s.retry.attempt}` : "Retrying";
    case "budget": return s.budget?.reason === "session_budget" ? "Held at its session budget" : "Held at its budget";
    case "parked": return "Parked: the agent asks you";
    case "reaction": return "Waiting on its pull request";
    case "between": return "Between two stages";
    case "blocked": return "Held by its blockers";
    default: return "Not running";
  }
}

/** "cancelled · review · ended 3 h ago". */
export function runLine(r: PastRun, now: number): string {
  const parts = [r.status ?? "", r.rule ?? ""];
  if (r.completed_at) parts.push(`ended ${ago(now - r.completed_at)} ago`);
  else if (r.started_at) parts.push(`started ${ago(now - r.started_at)} ago`);
  return parts.filter(Boolean).join(" · ");
}

function ago(seconds: number): string {
  const m = Math.max(0, Math.round(seconds / 60));
  if (m < 1) return "moments";
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  return h < 48 ? `${h} h` : `${Math.floor(h / 24)} days`;
}
