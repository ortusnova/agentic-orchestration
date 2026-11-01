<script lang="ts">
  import { onDestroy } from "svelte";
  import { api, ApiError, projectApi, subscribeGraph } from "../lib/api";
  import { changedStories } from "../lib/home";
  import { openQuestions, pageTitle } from "../lib/questions";
  import { graphHref, homeHref, type ProjectPage } from "../lib/router";
  import { httpSpecs } from "../lib/spec";
  import { httpSession } from "../lib/session";
  import { httpSync } from "../lib/sync";
  import { httpWorker } from "../lib/worker";
  import { emptyGraph, type MergedGraph, type TrackerProject, type ViewState } from "../lib/types";
  import ChatPane from "./ChatPane.svelte";
  import GraphPane from "./GraphPane.svelte";
  import HomePage from "./HomePage.svelte";
  import QuestionCount from "./QuestionCount.svelte";
  import Shell from "./Shell.svelte";
  import SyncStatus from "./SyncStatus.svelte";

  let { slug, page = "home", view = $bindable(), onmissing }: { slug: string; page?: ProjectPage; view: ViewState; onmissing: () => void } = $props();

  let doc: MergedGraph = $state(emptyGraph());
  let name = $state("");
  let tracker: TrackerProject | null = $state(null);
  // The tracker identifier of the worker shown in the chat pane, if any.
  let watching: string | null = $state(null);
  const workerClient = $derived(watching ? httpWorker(slug, watching) : null);
  const workerNode = $derived(watching ? doc.nodes.find((n) => n.tracker?.identifier === watching) : undefined);
  const worker = $derived(workerClient && workerNode ? { node: workerNode, client: workerClient } : null);
  // svelte-ignore state_referenced_locally
  const session = httpSession(slug);
  // svelte-ignore state_referenced_locally
  const sync = httpSync(slug);
  // svelte-ignore state_referenced_locally
  const specs = httpSpecs(slug);
  let unsubscribe: (() => void) | null = null;

  const tabs = $derived([
    { label: "Home", href: homeHref(slug), current: page === "home" },
    { label: "Graph", href: graphHref(slug), current: page === "graph" },
  ]);

  // A story whose state changed is marked for a few seconds (D9).
  const MARK_MS = 4000;
  let seen = new Map<string, string>();
  let marks: Set<string> = $state(new Set());
  function show(next: MergedGraph) {
    const { changed, now } = changedStories(seen, next.nodes);
    seen = now;
    doc = next;
    if (!changed.length) return;
    marks = new Set([...marks, ...changed]);
    setTimeout(() => (marks = new Set([...marks].filter((k) => !changed.includes(k)))), MARK_MS);
  }

  async function approve(keys: string[]): Promise<string | null> {
    try {
      await projectApi(slug).approve({ keys });
      return null;
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) return "Approving needs the approver role.";
      return err instanceof Error ? err.message : "The approval did not go through.";
    }
  }

  // The open questions, in the bar and the title of every view, live.
  const questions = $derived(openQuestions(doc));

  async function answer(key: string, text: string): Promise<string | null> {
    try {
      await projectApi(slug).answer(key, text);
      return null;
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) return "Answering needs the approver role.";
      if (err instanceof ApiError && err.status === 409 && err.code === "sync_disabled") return "This workbench is read-only: answer from the one that owns the project.";
      return err instanceof Error ? err.message : "The answer did not go through.";
    }
  }

  /** Select a node from outside the graph, moving to the level that shows it. */
  function selectAnywhere(key: string | null) {
    const n = key ? doc.nodes.find((x) => x.plan_key === key) : undefined;
    if (n) view.epic = n.kind === "story" ? (doc.parents.find((p) => p.child === key)?.parent ?? n.change) : null;
    view.selected = key;
  }

  async function start() {
    try {
      const detail = await api.project(slug);
      name = detail.project.name;
      tracker = detail.tracker;
      show(await projectApi(slug).graph());
      unsubscribe = subscribeGraph(slug, show);
    } catch {
      onmissing();
    }
  }
  start();
  onDestroy(() => unsubscribe?.());
</script>

<svelte:head><title>{pageTitle(name, questions)}</title></svelte:head>

{#if page === "home"}
  <div class="home-page">
    <HomePage {doc} {slug} projectName={name} {tabs} {marks} onapprove={approve} onanswer={answer}>
      {#snippet actions()}
        <QuestionCount count={questions} href={homeHref(slug)} />
        <SyncStatus status={doc.sync} onretry={() => sync.retry()} />
      {/snippet}
    </HomePage>
  </div>
{:else}
<Shell>
  {#snippet graph()}
    <GraphPane
      {doc}
      {view}
      {tabs}
      onunmark={() => (view.mark = null)}
      projectName={name}
      onselect={(k) => (view.selected = k)}
      onopen={(k) => { view.epic = k; view.selected = null; }}
      onback={() => { view.selected = view.epic; view.epic = null; }}
      ontogglecross={(b) => (view.showCross = b)}
    >
      {#snippet notice()}
        {#if tracker && tracker.exists && !tracker.ready}
          <div class="tracker-warn" role="status">
            <p><b>Huly project {tracker.key} cannot take plan writes yet.</b> Fix this in Huly; the plan syncs at the next change, or retry it from the sync status:</p>
            <ul>{#each tracker.problems as p (p)}<li>{p}</li>{/each}</ul>
          </div>
        {/if}
      {/snippet}
      {#snippet actions()}
        <QuestionCount count={questions} href={homeHref(slug)} />
        <SyncStatus status={doc.sync} onretry={() => sync.retry()} />
      {/snippet}
    </GraphPane>
  {/snippet}
  {#snippet chat()}
    <ChatPane {doc} {view} {session} onselect={(k) => selectAnywhere(k)} actions={sync} {worker} {specs}
      onwatch={(n) => { watching = n.tracker?.identifier ?? null; selectAnywhere(n.plan_key); }} onunwatch={() => (watching = null)} />
  {/snippet}
</Shell>
{/if}

<style>
  .home-page { height: 100%; display: flex; flex-direction: column; min-height: 0; }
  .tracker-warn { flex: none; padding: 10px var(--s-gutter); border-bottom: 1px solid #ecd9a8; background: var(--amber-tint); color: var(--amber-ink); font-size: 12.5px; }
  .tracker-warn b { color: var(--ink); font-weight: 600; }
  .tracker-warn ul { margin: 4px 0 0 18px; }
</style>
