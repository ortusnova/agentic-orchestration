<script lang="ts">
  import { ACTION_TEXT, duration, type RunState } from "../lib/fleet";

  /** A story's run state in its panel: where it runs or why it waits, what
   * releases it, its pull request and what that waits on, and the action
   * that unblocks it. */
  let { run, story, canAct, onopen, onsessions }: {
    run: RunState;
    story: string;
    canAct: boolean;
    /** Open the agent's session: live while it runs, its last transcript after. */
    onopen?: () => void;
    onsessions?: (sessions: number) => Promise<unknown>;
  } = $props();

  let grant = $state(4);
  let busy = $state(false);
  let error = $state("");
  let done = $state("");

  const heading = $derived(
    run.status === "running" ? `Running on ${run.worker}${run.slot ? `, slot ${run.slot}` : ""}`
      : run.status === "stuck" ? `Stuck${run.worker ? ` on ${run.worker}${run.slot ? `, slot ${run.slot}` : ""}` : ""}`
        : run.status === "waiting" ? `Waiting${run.worker ? ` on ${run.worker}` : ""}` : "Its pull request is open",
  );

  async function give(e: Event) {
    e.preventDefault();
    if (!onsessions || busy) return;
    busy = true;
    error = "";
    try {
      await onsessions(grant);
      done = `${grant} more sessions granted; ${run.sortie ?? "its"} Sortie runs it again.`;
    } catch (err) {
      error = err instanceof Error ? err.message : "The sessions were not granted.";
    } finally {
      busy = false;
    }
  }
</script>

<div class="run {run.status}" data-run-state={run.status} data-run-for={story}>
  <b>{heading}</b>
  <p data-why>{run.why}{#if run.status === "running" && run.started_at} · for {duration(Date.now() / 1000 - run.started_at)}{/if}</p>
  {#if run.release}<p class="rel" data-release>Released by {run.release}.</p>{/if}
  {#if run.pull}
    <p class="pr" data-run-pull={run.pull.number}>
      Pull request {#if run.pull.url}<a href={run.pull.url} target="_blank" rel="noopener">#{run.pull.number}</a>{:else}#{run.pull.number}{/if}
      {run.pull.waits_text}{run.pull.reason ? `: ${run.pull.reason}` : ""}{run.pull.stuck ? "; nothing merges it" : ""}
    </p>
  {/if}
  {#if run.action === "open" && onopen}
    <button type="button" class="btn ink" data-act="open" onclick={onopen}>{ACTION_TEXT.open}</button>
  {:else if run.action === "answer"}
    <p class="rel" data-act="answer">Answer it in the box below; the answer goes to {run.sortie ?? "its"} Sortie.</p>
  {:else if run.action === "sessions" && onsessions}
    {#if done}
      <p class="rel" role="status">{done}</p>
    {:else}
      <form class="grant" onsubmit={give}>
        <label>More sessions <input type="number" min="1" max="32" bind:value={grant} /></label>
        <button type="submit" class="btn go" data-act="sessions" disabled={busy || !canAct || grant < 1}
          title={!canAct ? "This needs the approver role" : undefined}>Grant</button>
      </form>
    {/if}
  {:else if run.action === "pull" && run.pull?.url}
    <a class="btn" data-act="pull" href={run.pull.url} target="_blank" rel="noopener">{ACTION_TEXT.pull}</a>
  {/if}
  {#if error}<p class="err" role="alert">{error}</p>{/if}
</div>

<style>
  .run { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; margin-top: var(--s-md); padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--r-card); background: var(--panel); font-size: 12.5px; }
  .run.stuck { border-color: #e9b8b3; background: #fbeceb; color: #6f1c16; }
  .run.waiting { border-color: #ecd9a8; background: var(--amber-tint); color: var(--amber-ink); }
  .run p { overflow-wrap: anywhere; }
  .run a { color: inherit; font-weight: 600; }
  .rel { color: var(--mute); }
  .stuck .rel, .waiting .rel { color: inherit; }
  .grant { display: flex; align-items: center; gap: 8px; }
  .grant input { width: 60px; margin-left: 6px; padding: 3px 6px; border: 1px solid var(--line); border-radius: var(--r-button); }
  .err { color: var(--alert); }
  .btn { display: inline-flex; align-items: center; height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; color: var(--ink); font-size: 12px; font-weight: 700; text-decoration: none; }
  .btn.go { border-color: var(--ink); background: var(--ink); color: #fff; }
  .btn.ink { border-color: var(--ink); }
  .btn:disabled { opacity: 0.45; cursor: default; }
</style>
