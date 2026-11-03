/** The merged graph the backend serves on /api/graph and /api/events. */
import type { Fleet, RunState } from "./fleet";
import type { HomeDoc } from "./home";

export type NodeStatus = "draft" | "synced" | "orphaned";

export interface Runtime {
  status: "running" | "retrying" | "budget_held";
  turn_count?: number | null;
  last_message?: string | null;
  attempt?: number | null;
  /** From Sortie's state: the workflow rule running it, its session, and whether its broker can be watched. */
  stage?: string | null;
  session_id?: string | null;
  held?: boolean;
  holder?: string | null;
  watchable?: boolean;
}

/** The question an agent asked when it stopped, while Sortie keeps its story parked. */
export interface Question {
  identifier: string;
  /** The agent's stop statement, whole. */
  statement: string;
  /** When the story stopped, in Unix seconds, if Sortie says. */
  parked_at: number | null;
  reason: string | null;
  /** The Sortie that holds the question, by name; absent from an older backend. */
  sortie?: string;
}

export interface TrackerView {
  id: string;
  identifier: string;
  title: string;
  state: string;
  terminal: boolean;
  url: string | null;
  parent_id: string | null;
  /** The story's pull request, as owner/repo#number, when Sortie holds it. */
  pr?: string | null;
}

export interface GraphNode {
  plan_key: string;
  kind: "epic" | "story";
  change: string;
  title: string;
  body?: string;
  approval: "approved" | "draft" | "unplanned" | null;
  has_spec: boolean | null;
  depth: number;
  deliverable_kind: string | null;
  covers: string[];
  acceptance_criteria: string[];
  status: NodeStatus;
  tracker: TrackerView | null;
  runtime: Runtime | null;
  /** Stories only: the open question of a parked story. */
  question?: Question | null;
  /** Stories only: where it runs or why it waits, its pull request, and what unblocks it. */
  run_state?: RunState | null;
  // Stories only: the plan status the workbench derives from approvals and Sortie.
  plan_status?: PlanStatus | null;
  held_by?: string[];
  rereview?: { reason: string; ref: string } | null;
  frozen?: boolean;
  approved_by?: { name: string; id: string } | null;
  approved_at?: number | null;
  review?: "human" | "criteria" | "ui";
  supersedes?: string | null;
  /** The pull requests the story names in delivered_by, as GitHub reports them; null when it names none. */
  delivery?: Delivery | null;
  /** A ui story only: the preview of its pull request, once the kit deployed it; null before. */
  preview?: Preview | null;
  /** Stories only: what Sortie's supervisor did to the story, or was refused, newest first. */
  supervisor_actions?: SupervisorAction[];
}

/** One action of Sortie's supervisor, as Sortie recorded it. */
export interface SupervisorAction {
  id: string;
  /** When Sortie recorded it, in Unix seconds. */
  at: number | null;
  /** What the supervisor did or asked to do, such as "record_merge". */
  action: string;
  /** The story it touched; null for an action on no story, such as filing an issue. */
  identifier: string | null;
  key: string | null;
  label: string | null;
  title: string | null;
  url: string | null;
  pull: { ref: string; url: string | null } | null;
  /** What it was based on: named values, a link where one applies. */
  evidence: { name: string | null; value: string; url: string | null }[];
  /** "done", "refused", "rejected" or "failed". */
  result: string;
  detail: string | null;
  /** The Sortie whose supervisor took it. */
  sortie?: string | null;
}

/** One Sortie's supervisor status: whether it acts, and who paused it and when. */
export interface SupervisorStatus {
  /** "active", "paused" or "off". */
  status: string;
  paused_at: number | null;
  paused_by: string | null;
  reason: string | null;
}

/** Sortie's supervisor: whether it acts, and what it did lately. The status
 * is that of the Sortie that dispatches the project. */
export interface Supervisor extends SupervisorStatus {
  /** Each Sortie that reports a supervisor, this project's own first. Absent from an older backend. */
  sorties?: (SupervisorStatus & { name: string | null; own: boolean })[];
  /** The recent actions on this project's stories and on none, newest first. */
  actions: SupervisorAction[];
  total: number;
}

export type PlanStatus = "needs_spec" | "draft" | "held" | "ready" | "released" | "frozen" | "building" | "in_review" | "click_proofs" | "merging" | "needs_rereview" | "delivered" | "delivery_unconfirmed";

/** The preview of a ui story's pull request: its branch's read-only workbench. */
export interface Preview {
  url: string;
  pull: string;
  pull_url: string;
  sha: string;
  /** The head commit the pull request records the click-proofs for; null while it records none. */
  click_proofs?: string | null;
}

/** One pull request a story names, as GitHub last reported it. */
export interface DeliveryPull {
  ref: string;
  state: "merged" | "open" | "closed" | "unknown" | "pending";
  url: string;
  merge_commit: string | null;
  merge_commit_url: string | null;
  merged_at: string | null;
  /** Why it does not confirm the delivery: "it is open", or why it could not be looked up. */
  reason: string | null;
}

/** "delivered" once every pull request is merged. */
export interface Delivery {
  state: "delivered" | "unconfirmed";
  pulls: DeliveryPull[];
}

export interface PlanError {
  code: string;
  message: string;
  file: string | null;
  change: string | null;
  deliverable: string | null;
  members: string[];
}

/** Sortie's plan-control states, when it runs under plan control. */
export interface PlanStates {
  release: string;
  backlog: string;
  cancel: string;
  done: string;
}

export interface MergedGraph {
  version: number;
  generated_at: number;
  nodes: GraphNode[];
  parents: { child: string; parent: string }[];
  blocks: { blocker: string; blocked: string }[];
  unplanned_changes: string[];
  plan_states?: PlanStates | null;
  validation: { valid: boolean; errors: PlanError[]; version: number };
  /** The project's automatic sync to the tracker. Absent from an older backend. */
  sync?: SyncStatus | null;
  /** The project home, computed with the graph. Absent from an older backend. */
  home?: HomeDoc | null;
  /** Sortie's supervisor, for a project Sortie dispatches; null when Sortie reports none. */
  supervisor?: Supervisor | null;
  /** What each worker does and what waits on it. Absent from an older backend. */
  fleet?: Fleet | null;
  sortie: {
    reachable: boolean; error: string | null; capabilities: string[]; generated_at: string | null;
    /** The tracker project, and whether Sortie runs agents for it (it does for its configured project only). */
    project?: string; dispatched?: boolean;
  };
}

/** An issue a sync wrote, as Sortie reports it. */
export interface SyncIssueRef { plan_key: string; issue_id?: string; identifier?: string }

/** What the last sync wrote, and where it stopped. */
export interface SyncResult {
  created?: SyncIssueRef[]; updated?: SyncIssueRef[]; released?: SyncIssueRef[]; orphans?: SyncIssueRef[];
  failed?: { op: string; plan_key: string; error: string } | null;
}

/** The state of a project's automatic sync. Times are Unix seconds. */
export interface SyncStatus {
  /** "disabled": a read-only workbench (WORKBENCH_SYNC=0) never syncs. */
  state: "idle" | "syncing" | "synced" | "failed" | "disabled";
  synced_at: number | null;
  failed_at: number | null;
  reason: { code: string; message: string } | null;
  result: SyncResult | null;
  retry_at: number | null;
  runs: number;
}

/** A project as the projects list shows it. */
export interface ProjectSummary {
  id: number;
  slug: string;
  name: string;
  huly_project: string;
  store_path: string;
  created_at: number;
  valid: boolean;
  errors: number;
  epics: number;
  stories: number;
  session_active: boolean;
  sortie: { reachable: boolean; dispatched: boolean };
}

/** A tracker project as Sortie reports it. */
export interface TrackerProject {
  key: string;
  name: string | null;
  url: string | null;
  exists: boolean;
  created: boolean;
  ready: boolean;
  dispatched: boolean;
  problems: string[];
}

export function emptyGraph(): MergedGraph {
  return {
    version: 0, generated_at: 0, nodes: [], parents: [], blocks: [], unplanned_changes: [],
    validation: { valid: true, errors: [], version: 0 },
    sortie: { reachable: false, error: null, capabilities: [], generated_at: null },
  };
}

/** Where the person is in the graph; kept across live updates. */
export interface ViewState {
  epic: string | null;
  selected: string | null;
  showCross: boolean;
  /** A milestone whose changes the project level marks. */
  mark?: string | null;
  /** The story whose worker the chat pane shows, by tracker identifier. */
  watch?: string | null;
}
