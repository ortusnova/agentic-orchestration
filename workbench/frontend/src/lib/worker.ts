/** Watching a worker Sortie runs; tests substitute a fake. */
import { projectApi, type WorkerCommand } from "./api";
import type { BrokerEvent } from "./chat";
import type { GraphNode } from "./types";

export interface RunInfo {
  identifier: string;
  session_id: string;
  stage: string | null;
  /** True when the stream moved on to a later run of the same story. */
  follow: boolean;
}

/** The events Sortie kept of a story's last run, after it ended. */
export interface LastTranscript {
  identifier: string;
  dispatch_id: string | null;
  sortie: string;
  events: { seq: number | null; at: string | null; event: string | null; text: string; tool_line?: string | null }[];
}

export interface WorkerHandlers {
  onEvent: (e: BrokerEvent) => void;
  onRun: (r: RunInfo) => void;
  /** Nothing runs now: the events Sortie kept of the story's last run. */
  onTranscript?: (t: LastTranscript) => void;
  onEnded: () => void;
  onDown: () => void;
}

export interface WorkerClient {
  messages(): Promise<{ commands: WorkerCommand[] }>;
  command(cmd: Record<string, unknown>): Promise<unknown>;
  subscribe(h: WorkerHandlers): () => void;
}

/** The worker of one story, through the backend. */
export function httpWorker(slug: string, identifier: string): WorkerClient {
  const api = projectApi(slug);
  return {
    messages: () => api.workerMessages(identifier),
    command: (cmd) => api.workerCommand(identifier, cmd),
    subscribe(h) {
      const es = new EventSource(api.workerEvents(identifier), { withCredentials: true });
      es.onmessage = (e) => h.onEvent(JSON.parse(e.data));
      es.addEventListener("run", (e) => h.onRun(JSON.parse((e as MessageEvent).data)));
      es.addEventListener("transcript", (e) => h.onTranscript?.(JSON.parse((e as MessageEvent).data)));
      es.addEventListener("ended", () => {
        es.close();
        h.onEnded();
      });
      es.onerror = () => {
        if (es.readyState === EventSource.CLOSED) h.onDown();
      };
      return () => es.close();
    },
  };
}

/** The stories Sortie runs, retries, or holds, in plan order. */
export function workersOf(nodes: GraphNode[]): GraphNode[] {
  return nodes.filter((n) => n.kind === "story" && n.runtime && n.tracker);
}

/** A worker's status in a few words. */
export function workerStatus(n: GraphNode): string {
  const rt = n.runtime;
  if (!rt) return "";
  if (rt.status === "retrying") return rt.attempt ? `retrying, attempt ${rt.attempt}` : "retrying";
  if (rt.status === "budget_held") return "held: budget used";
  if (rt.held) return rt.holder ? `held by ${rt.holder}` : "held";
  return "running";
}
