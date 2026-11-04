<script lang="ts">
  import { grantFallback, type GrantResult } from "../lib/run";

  /** More sessions for a story its Sortie holds at the session budget. A
   * Sortie that cannot grant them yet says so, with the setting to raise. */
  let {
    identifier, sortie, used = null, sessions = null, canGrant = null, maxSessions = null, canAct, ongrant,
  }: {
    identifier: string;
    /** The worker whose Sortie holds it. */
    sortie: string;
    used?: number | null;
    sessions?: number | null;
    /** false when the Sortie is known to have no grant route. */
    canGrant?: boolean | null;
    maxSessions?: number | null;
    /** Only an approver grants; the backend checks the role too. */
    canAct: boolean;
    ongrant: (add: number) => Promise<GrantResult>;
  } = $props();

  let add = $state(4);
  let busy = $state(false);
  let error = $state("");
  let unsupported = $state(false);
  let done = $state(0);
  const fallback = $derived(unsupported || canGrant === false);

  async function grant(e: SubmitEvent) {
    e.preventDefault();
    if (busy || add < 1) return;
    busy = true;
    error = "";
    const r = await ongrant(add);
    busy = false;
    if (r.ok) done = add;
    else if (r.unsupported) unsupported = true;
    else error = r.message;
  }
</script>

<div class="grant" data-grant={identifier}>
  {#if used != null && sessions != null}<p class="used">Used <b>{used}</b> of its <b>{sessions}</b> sessions.</p>{/if}
  {#if done}
    <p class="ok" role="status">Gave {identifier} {done} more {done === 1 ? "session" : "sessions"}. {sortie}'s Sortie runs it again at its next poll.</p>
  {:else if fallback}
    <p class="fallback" role="status" data-note="grant-unsupported">{grantFallback(sortie, maxSessions)}</p>
  {:else if canAct}
    <form onsubmit={grant}>
      <label>Give <input type="number" min="1" max="50" bind:value={add} disabled={busy} aria-label="Sessions to give" /> more {add === 1 ? "session" : "sessions"}</label>
      <button type="submit" class="send" data-act="grant" disabled={busy || !(add >= 1)}>{busy ? "Granting…" : `Give ${add} more`}</button>
    </form>
  {:else}
    <p class="note" data-note="approver">Granting sessions needs the approver role.</p>
  {/if}
  {#if error}<p class="err" role="alert">{error}</p>{/if}
</div>

<style>
  .grant { display: flex; flex-direction: column; gap: 6px; min-width: 0; font-size: 12.5px; }
  .used { color: var(--ink); }
  .used b { font-variant-numeric: tabular-nums; }
  form { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  label { display: inline-flex; align-items: center; gap: 6px; color: var(--ink); }
  input { width: 56px; height: 28px; padding: 0 6px; border: 1px solid var(--line); border-radius: var(--r-card); background: #fff; font: inherit; font-variant-numeric: tabular-nums; }
  input:focus-visible { outline: 2px solid var(--ink); outline-offset: 1px; }
  .send { height: 28px; padding: 0 12px; border: 1px solid var(--ink); border-radius: 7px; background: var(--ink); color: #fff; font-size: 12px; font-weight: 700; }
  .send:hover:not(:disabled) { background: #000; }
  .send:disabled { opacity: 0.6; cursor: default; }
  .ok { color: var(--done); }
  .fallback { color: var(--amber-ink); max-width: 68ch; }
  .note { color: var(--mute); }
  .err { font-size: 11.5px; color: #8f241d; }
</style>
