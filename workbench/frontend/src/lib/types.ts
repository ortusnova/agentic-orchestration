/** The merged graph the backend serves on /api/graph and /api/events. */
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

export interface TrackerView {
  id: string;
  identifier: string;
  title: string;
  state: string;
  terminal: boolean;
  url: string | null;
  parent_id: string | null;
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
  // Stories only: the plan status the workbench derives from approvals and Sortie.
  plan_status?: PlanStatus | null;
  held_by?: string[];
  rereview?: { reason: string; ref: string } | null;
  frozen?: boolean;
  approved_by?: { name: string; id: string } | null;
  approved_at?: number | null;
  review?: "human" | "criteria";
  supersedes?: string | null;
}

export type PlanStatus = "needs_spec" | "draft" | "held" | "ready" | "released" | "frozen" | "needs_rereview";

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
}
