/** The questions agents ask people, as every view of a project shows them (agent-platform/agent-questions). */
import type { MergedGraph } from "./types";

/** How many questions wait on a person: the stories that wait on an answer,
 * and the platform faults only a person can clear. */
export function openQuestions(doc: MergedGraph): number {
  const faults = doc.home && !doc.home.error ? (doc.home.faults?.length ?? 0) : 0;
  return doc.nodes.filter((n) => n.kind === "story" && n.question).length + faults;
}

/** "1 question", "2 questions". */
export function questionsLabel(n: number): string {
  return `${n} ${n === 1 ? "question" : "questions"}`;
}

/** The page title of a project view: the open questions first, while there are any. */
export function pageTitle(projectName: string, questions: number): string {
  const base = projectName ? `${projectName} · Plan workbench` : "Plan workbench";
  return questions ? `${questionsLabel(questions)} · ${base}` : base;
}

/** When a story stopped, as a moment and in words relative to now. */
export function stoppedAt(parkedAt: number | null, now = Date.now()): { iso: string; text: string } | null {
  if (parkedAt == null) return null;
  const at = new Date(parkedAt * 1000);
  const mins = Math.max(0, Math.round((now - at.getTime()) / 60000));
  const ago = mins < 1 ? "just now" : mins < 60 ? `${mins} min ago` : mins < 48 * 60 ? `${Math.round(mins / 60)} h ago` : `${Math.round(mins / 1440)} days ago`;
  return { iso: at.toISOString(), text: `${at.toLocaleString()} (${ago})` };
}
