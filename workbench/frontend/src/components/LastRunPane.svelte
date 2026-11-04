<script lang="ts">
  import { tick } from "svelte";
  import { groupTurns } from "../lib/chat";
  import { runItems, runLine, statusWord, type RunClient, type RunEvent, type RunState } from "../lib/run";
  import type { GraphNode } from "../lib/types";
  import GrantBox from "./GrantBox.svelte";
  import QuestionBox from "./QuestionBox.svelte";
  import Transcript from "./Transcript.svelte";

  /** A story that does not run now: the transcript of its last runs, as
   * Sortie kept them, and the action that unblocks it, in the chat pane. */
  let { node, runs, canAct, onanswer, onclose, now: fixed }: {
    node: GraphNode;
    runs: RunClient;
    canAct: boolean;
    onanswer: (text: string) => Promise<string | null>;
    onclose: () => void;
    now?: number;
  } = $props();

  const identifier = $derived(node.tracker?.identifier ?? node.plan_key);
  let info = $state<RunState | null>(null);
  let chosen = $state<string | null>(null);
  let events: RunEvent[] = $state([]);
  let loading = $state(false);
  let error = $state("");
  let log: HTMLElement | undefined = $state();
  const now = $derived(fixed ?? Date.now() / 1000);

  async function load() {
    try {
      info = await runs.state(identifier);
      if (!chosen && info.runs.length) await pick(info.runs[0].dispatch_id);
    } catch (err) {
      error = err instanceof Error ? err.message : "Sortie did not say what it does with this story.";
    }
  }

  async function pick(id: string) {
    chosen = id;
    loading = true;
    error = "";
    try {
      events = await runs.events(identifier, id);
    } catch (err) {
      events = [];
      error = err instanceof Error ? err.message : "Sortie kept no events of this run.";
    } finally {
      loading = false;
    }
    await tick();
    if (log) log.scrollTop = log.scrollHeight;
  }
  load();

  const items = $derived(groupTurns(runItems(events), false));
  const run = $derived(info?.runs.find((r) => r.dispatch_id === chosen) ?? null);
</script>

<div class="session-bar" data-last-run={identifier}>
  <div class="who">
    <span class="state"><i></i>Last run of {identifier}{info ? ` · ${statusWord(info).toLowerCase()} on ${info.sortie}` : ""}</span>
    {#if info && info.runs.length > 1}
      <label class="sub">Run
        <select value={chosen} onchange={(e) => pick(e.currentTarget.value)} aria-label="Run">
          {#each info.runs as r (r.dispatch_id)}<option value={r.dispatch_id}>{runLine(r, now)}</option>{/each}
        </select>
      </label>
    {:else if run}
      <span class="sub">{runLine(run, now)}</span>
    {/if}
  </div>
  <button type="button" class="ghost" data-act="back-to-planning" onclick={onclose}>Back to planning</button>
</div>

<Transcript bind:el={log} {items}
  empty={loading ? "Reading the run…" : info && !info.runs.length ? "Sortie keeps no run of this story." : error || "No agent event of this run was kept."} />

{#if error && items.length}<p class="error" role="alert">{error}</p>{/if}

{#if info}
  <div class="unblock" data-unblock={info.status}>
    {#if info.releases}<p class="releases"><b>Releases it</b> {info.releases}</p>{/if}
    {#if info.status === "parked" && (info.question ?? node.question)}
      <QuestionBox question={(info.question ?? node.question)!} story={node.plan_key} canAnswer={canAct}
        onanswer={async (t) => { const e = await onanswer(t); if (!e) load(); return e; }} />
    {:else if info.status === "budget" && info.action === "grant"}
      <GrantBox {identifier} sortie={info.sortie} used={info.budget?.used} sessions={info.budget?.sessions}
        canGrant={info.can_grant} maxSessions={info.max_sessions} {canAct}
        ongrant={async (n) => { const r = await runs.grant(identifier, n); if (r.ok) load(); return r; }} />
    {:else if info.pulls.some((p) => p.url)}
      {#each info.pulls.filter((p) => p.url) as p (p.number)}
        <a class="ghost link" href={p.url} target="_blank" rel="noopener" data-act="open-pr">Open #{p.number}: {p.waits.replace(/^#\d+ /, "")}</a>
      {/each}
    {/if}
  </div>
{/if}

<style>
  .session-bar { flex: none; display: flex; align-items: center; justify-content: space-between; gap: var(--s-md); padding: 10px var(--s-panel); border-bottom: 1px solid var(--hair); }
  .who { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .state { display: flex; align-items: center; gap: 8px; font-size: 12px; font-weight: 700; color: var(--ink); }
  .state i { width: 8px; height: 8px; border-radius: 50%; background: var(--slate); }
  .sub { display: flex; align-items: center; gap: 6px; font: 500 11.5px var(--font-mono); color: var(--mute); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  select { max-width: 100%; height: 26px; border: 1px solid var(--line); border-radius: var(--r-card); background: #fff; font: 500 11.5px var(--font-mono); color: var(--ink); }
  .ghost { height: 36px; padding: 0 14px; border-radius: var(--r-button); font-weight: 700; font-size: 13px; background: #fff; border: 1px solid var(--line); }
  .ghost:hover { border-color: var(--ink); }
  .ghost.link { display: inline-flex; align-items: center; height: 32px; color: var(--ink); text-decoration: none; font-weight: 600; font-size: 12.5px; }
  .error { flex: none; padding: 6px var(--s-panel); color: var(--alert); font-size: 12px; }
  .unblock { flex: none; display: flex; flex-direction: column; gap: 8px; max-height: 45%; overflow-y: auto; padding: var(--s-md) var(--s-panel) var(--s-panel); border-top: 1px solid var(--line); background: var(--amber-tint); }
  .releases { font-size: 12.5px; color: var(--ink); max-width: 68ch; }
  .releases b { margin-right: 6px; font-size: 10.5px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--amber-ink); }
</style>
