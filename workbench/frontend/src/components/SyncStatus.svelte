<script lang="ts">
  import type { SyncIssueRef, SyncStatus } from "../lib/types";

  // Syncing is invisible: this speaks only when the tracker could not be
  // updated, or when the workbench never writes to it (read-only).
  let { status, onretry }: { status: SyncStatus | null | undefined; onretry: () => Promise<unknown> } = $props();

  let open = $state(false);
  let retrying = $state(false);

  const phase = $derived(status?.state ?? "idle");
  const clock = (t: number | null | undefined) =>
    t ? new Date(t * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }) : "";

  const REASONS: Record<string, string> = {
    sortie_unreachable: "Sortie is unreachable.",
    plan_invalid: "The plan is invalid. The tracker catches up once its problems are fixed.",
    tracker_unsupported_operation: "The tracker cannot make this change.",
  };
  const reasonText = $derived(status?.reason ? (REASONS[status.reason.code] ?? "") : "");
  const name = (r: SyncIssueRef) => r.identifier || r.plan_key;
  const written = $derived(
    status?.result ? [...(status.result.created ?? []), ...(status.result.updated ?? []), ...(status.result.released ?? [])] : [],
  );

  async function retry() {
    retrying = true;
    try {
      await onretry();
    } finally {
      retrying = false;
    }
  }
</script>

{#if phase === "failed"}
  <div class="sync-status">
    <button type="button" class="pill failed" data-sync="failed" aria-expanded={open} onclick={() => (open = !open)}
      title={status?.reason?.message}>
      <span class="dot" aria-hidden="true"></span><span class="label">Tracker not updated</span>
    </button>
    {#if open}
      <div class="panel" role="dialog" aria-label="Tracker not updated" data-sync-result>
        {#if status?.reason}
          <p class="alert" role="alert">
            {#if reasonText}<b>{reasonText}</b>{/if}
            <span class="mono">{status.reason.message}</span>
          </p>
        {/if}
        {#if status?.result?.failed}
          <p class="alert" data-sync-failed>Stopped at <span class="mono">{status.result.failed.op} {status.result.failed.plan_key}</span>: {status.result.failed.error}</p>
        {/if}
        {#if written.length}
          <p class="muted" data-sync-list="written">Updated before it stopped: <span class="mono">{written.map(name).join(", ")}</span></p>
        {/if}
        {#if status?.retry_at}<p class="muted">Trying again at {clock(status.retry_at)}, and at the next change.</p>{/if}
        <button type="button" class="retry" data-act="retry-sync" onclick={retry} disabled={retrying}>
          {retrying ? "Trying…" : "Try now"}
        </button>
      </div>
    {/if}
  </div>
{:else if phase === "disabled"}
  <div class="sync-status">
    <span class="pill" data-sync="disabled" title="This workbench never writes to the tracker">
      <span class="dot" aria-hidden="true"></span><span class="label">Read-only</span>
    </span>
  </div>
{/if}

<style>
  .sync-status { position: relative; }
  .pill { display: inline-flex; align-items: center; gap: 8px; height: 32px; padding: 0 12px; border-radius: var(--r-pill);
    background: var(--ink-raised); border: 1px solid var(--ink-edge); color: #fff; font-size: 12.5px; font-weight: 600; white-space: nowrap; }
  .pill:hover { border-color: var(--ink-mute); }
  /* In a narrow top bar the pill keeps its dot; the words stay in its accessible name. */
  @container (max-width: 480px) {
    .pill { padding: 0 10px; }
    .label { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--slate); flex: none; }
  .failed { border-color: var(--alert); }
  .failed .dot { background: var(--alert); }
  .panel { position: absolute; right: 0; top: calc(100% + 8px); z-index: 20; width: min(380px, 90vw); display: flex; flex-direction: column;
    gap: var(--s-sm); padding: var(--s-md) var(--s-card-x); border-radius: var(--r-well); background: #fff; color: var(--ink);
    box-shadow: 0 24px 60px -24px rgba(20, 22, 26, 0.5); border: 1px solid var(--line); }
  .mono { font: 500 12px var(--font-mono); overflow-wrap: anywhere; }
  .muted { color: var(--mute); font-size: 12.5px; }
  .alert { display: flex; flex-direction: column; gap: 4px; padding: 8px 10px; border: 1px solid var(--alert); border-radius: var(--r-button); font-size: 12.5px; }
  .retry { align-self: flex-start; height: 32px; padding: 0 14px; border-radius: var(--r-button); background: var(--ink); color: #fff; font-weight: 700; }
  .retry:disabled { opacity: 0.5; cursor: default; }
</style>
