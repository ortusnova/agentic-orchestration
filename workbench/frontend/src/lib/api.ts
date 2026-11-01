/** The workbench backend: the browser's only server. */
import type { About, Identity } from "./identity.svelte";
import type { MergedGraph, ProjectSummary, SyncStatus, TrackerProject } from "./types";

export class ApiError extends Error {
  constructor(readonly status: number, readonly code: string, message: string, readonly body: unknown = null) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? {} : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await r.json().catch(() => ({}));
  if (!r.ok) {
    const err = (data as { error?: { code?: string; message?: string } }).error ?? {};
    throw new ApiError(r.status, err.code ?? "error", err.message ?? r.statusText, data);
  }
  return data as T;
}

export const api = {
  me: () => request<{ signed_in: boolean; mode: "oidc" | "local"; identity: Identity }>("GET", "/api/me"),
  authMode: () => request<{ mode: "oidc" | "local" }>("GET", "/api/auth/mode"),
  about: () => request<About>("GET", "/api/about"),
  login: (password: string) => request<{ signed_in: boolean }>("POST", "/api/login", { password }),
  logout: () => request<{ signed_in: false; end_session_url: string | null }>("POST", "/api/logout"),
  projects: () => request<{ projects: ProjectSummary[] }>("GET", "/api/projects"),
  createProject: (body: { name: string; slug: string; huly_project: string }) =>
    request<{ project: ProjectSummary; tracker: TrackerProject }>("POST", "/api/projects", body),
  project: (slug: string) =>
    request<{ project: ProjectSummary; tracker: TrackerProject | null; tracker_error: string | null }>("GET", projectPath(slug)),
  renameProject: (slug: string, name: string) => request<{ project: ProjectSummary }>("PATCH", projectPath(slug), { name }),
  deleteProject: (slug: string) => request<{ deleted: string; store_path: string; huly_project: string }>("DELETE", projectPath(slug)),
};

export const projectPath = (slug: string) => `/api/projects/${encodeURIComponent(slug)}`;

/** The routes of one project: its graph, sync, approval, and planning session. */
export function projectApi(slug: string) {
  const p = projectPath(slug);
  return {
    graph: () => request<MergedGraph>("GET", `${p}/graph`),
    syncStatus: () => request<SyncStatus>("GET", `${p}/sync`),
    retrySync: () => request<SyncStatus>("POST", `${p}/sync/retry`),
    approve: (target: { keys?: string[]; epic?: string }) => request<{ approved: string[] }>("POST", `${p}/approve`, target),
    pullBack: (key: string) => request<unknown>("POST", `${p}/stories/pull-back`, { key }),
    cancel: (key: string) => request<{ cancelled: string; rereview: string[] }>("POST", `${p}/stories/cancel`, { key }),
    supersede: (key: string) => request<{ superseded: string; by: string }>("POST", `${p}/stories/supersede`, { key }),
    answer: (key: string, text: string) => request<{ answered: string; identifier: string }>("POST", `${p}/stories/answer`, { key, text }),
    session: () => request<SessionStatus>("GET", `${p}/session`),
    startSession: (message: string) => request<SessionStatus>("POST", `${p}/session`, { message }),
    stopSession: () => request<SessionStatus>("DELETE", `${p}/session`),
    command: (cmd: Record<string, unknown>) => request<unknown>("POST", `${p}/session/commands`, cmd),
    sessionConfig: () => request<SessionConfig>("GET", `${p}/session/config`),
    setSessionConfig: (category: ConfigCategory, value: string) => request<SessionConfig>("PUT", `${p}/session/config`, { category, value }),
    sessionEvents: `${p}/session/events`,
    workerMessages: (id: string) => request<{ commands: WorkerCommand[] }>("GET", `${p}/workers/${encodeURIComponent(id)}/messages`),
    workerCommand: (id: string, cmd: Record<string, unknown>) => request<unknown>("POST", `${p}/workers/${encodeURIComponent(id)}/commands`, cmd),
    workerEvents: (id: string) => `${p}/workers/${encodeURIComponent(id)}/events`,
    events: `${p}/events`,
  };
}

/** A command a person sent to a worker, and who sent it. */
export interface WorkerCommand {
  id: number;
  op: string;
  text: string | null;
  session_id: string | null;
  actor_id: string;
  actor_name: string;
  time: number;
}

export interface SessionStatus {
  active: boolean;
  key?: string;
  acp_session_id?: string | null;
  /** False for a session whose driver predates model choice: it cannot change model. */
  can_configure?: boolean;
  model?: string | null;
  thought_level?: string | null;
  error?: string | null;
}

/** The ACP config option categories a person may choose. */
export type ConfigCategory = "model" | "thought_level";

/** One choosable session config option, as the agent reports it over ACP. */
export interface ConfigOption {
  id: string;
  category: ConfigCategory;
  name: string;
  currentValue: string | null;
  options: { value: string; name: string }[];
}

export interface SessionConfig {
  options: ConfigOption[];
  /** False while the session starts and its model is still being selected. */
  ready: boolean;
}

/** Subscribe to graph updates; the browser's EventSource reconnects by itself. */
export function subscribeGraph(slug: string, onGraph: (g: MergedGraph) => void, onError?: () => void): () => void {
  const es = new EventSource(projectApi(slug).events, { withCredentials: true });
  es.addEventListener("graph", (e) => onGraph(JSON.parse((e as MessageEvent).data)));
  if (onError) es.onerror = onError;
  return () => es.close();
}
