<script lang="ts">
  import { can } from "../lib/identity.svelte";
  import { canPause, canResume, iso, statusText, when } from "../lib/supervisor";
  import type { Supervisor, SupervisorStatus } from "../lib/types";
  import SupervisorActions from "./SupervisorActions.svelte";

  let {
    supervisor, slug, dispatched, reachable, oncontrol,
  }: {
    supervisor: Supervisor | null | undefined;
    slug: string;
    /** Whether Sortie runs agents for this project, and so supervises its runs. */
    dispatched: boolean;
    reachable: boolean;
    /** Pause or resume a Sortie's supervisor, by the Sortie's name, null for
     * the one that dispatches the project; resolves to an error message, or null. */
    oncontrol?: (op: "pause" | "resume", sortie: string | null) => Promise<string | null>;
  } = $props();

  type Busy = { op: "pause" | "resume"; sortie: string | null };
  let busy: Busy | null = $state(null);
  let error = $state("");

  // The other Sorties that report a supervisor, each with its own status and pause.
  const others = $derived((supervisor?.sorties ?? []).filter((s) => !s.own));

  async function control(op: "pause" | "resume", sortie: string | null = null) {
    if (!oncontrol) return;
    busy = { op, sortie };
    error = "";
    error = (await oncontrol(op, sortie)) ?? "";
    busy = null;
  }
</script>

{#snippet status(s: SupervisorStatus, sortie: string | null)}
  <span class="st {s.status}" data-supervisor-status={s.status}><i aria-hidden="true"></i>{statusText(s)}</span>
  {#if s.status === "paused" && (s.paused_by || s.paused_at != null)}
    <span class="by">{s.paused_by ? `paused by ${s.paused_by}` : "paused"}{#if s.paused_at != null}{" · "}<time datetime={iso(s.paused_at)}>{when(s.paused_at)}</time>{/if}</span>
  {/if}
  {#if can("approver") && oncontrol}
    {#if canPause(s)}
      <button type="button" class="btn" data-act="pause-supervisor" disabled={!!busy} onclick={() => control("pause", sortie)}>{busy?.op === "pause" && busy.sortie === sortie ? "Pausing…" : "Pause"}</button>
    {:else if canResume(s)}
      <button type="button" class="btn go" data-act="resume-supervisor" disabled={!!busy} onclick={() => control("resume", sortie)}>{busy?.op === "resume" && busy.sortie === sortie ? "Resuming…" : "Resume"}</button>
    {/if}
  {/if}
{/snippet}

<section class="sup" aria-labelledby="sup-h" data-supervisor={supervisor?.status ?? "none"}>
  <div class="head">
    <h2 id="sup-h">Supervisor<span>Sortie's own agent: what it did to the runs, and what it was refused</span></h2>
    {#if supervisor}
      <div class="ctl" data-sortie={supervisor.sorties?.[0]?.name ?? ""}>
        {#if others.length && supervisor.sorties?.[0]?.name}<b class="name">{supervisor.sorties[0].name}</b>{/if}
        {@render status(supervisor, null)}
      </div>
    {/if}
  </div>
  {#if supervisor && others.length}
    <ul class="others" aria-label="The other Sorties' supervisors">
      {#each others as s (s.name)}
        <li class="ctl" data-sortie={s.name}><b class="name">{s.name}</b>{@render status(s, s.name)}</li>
      {/each}
    </ul>
  {/if}
  {#if error}<p class="err" role="alert">{error}</p>{/if}
  {#if !reachable}
    <p class="none" data-note="unreachable">Sortie is unreachable, so its supervisor is unknown.</p>
  {:else if !dispatched}
    <p class="none" data-note="not-dispatched">Sortie does not run agents for this project, so its supervisor does not act on it.</p>
  {:else if !supervisor}
    <p class="none" data-note="no-supervisor">This Sortie reports no supervisor.</p>
  {:else}
    {#if supervisor.reason}<p class="none">{supervisor.reason}</p>{/if}
    {#if supervisor.actions.length}
      <SupervisorActions actions={supervisor.actions} {slug} />
      {#if supervisor.total > supervisor.actions.length}<p class="none">The {supervisor.actions.length} newest of {supervisor.total}.</p>{/if}
    {:else}
      <p class="none" data-note="no-actions">It has taken no action on this project's stories.</p>
    {/if}
  {/if}
</section>

<style>
  .sup { padding: 22px var(--home-x, 40px) 0; }
  .head { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 6px 24px; margin-bottom: 6px; }
  h2 { font-size: 15px; font-weight: 700; }
  h2 span { margin-left: 8px; font-size: 12px; font-weight: 600; color: var(--mute); }
  .ctl { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
  .st { display: inline-flex; align-items: center; gap: 6px; height: 24px; padding: 0 9px; border: 1px solid var(--line); border-radius: var(--r-pill); font-size: 12px; font-weight: 700; }
  .st i { width: 8px; height: 8px; border-radius: 50%; background: var(--slate); }
  .st.active, .st.idle, .st.running { border-color: #bcd6c6; background: var(--done-tint); color: var(--done); }
  .st.active i, .st.idle i, .st.running i { background: var(--done); }
  .st.paused { border-color: #ecd9a8; background: var(--amber-tint); color: var(--amber-ink); }
  .st.paused i { background: var(--amber); }
  .st.unavailable, .st.budget_exhausted { border-color: #e9b8b3; background: #fbeceb; color: #8f241d; }
  .st.unavailable i, .st.budget_exhausted i { background: #8f241d; }
  .by { font-size: 12px; color: var(--mute); }
  .name { font: 600 12px var(--font-mono); color: var(--ink); }
  .others { list-style: none; display: flex; flex-direction: column; align-items: flex-end; gap: 6px; margin-bottom: 6px; }
  .btn { height: 28px; padding: 0 14px; border: 1px solid var(--ink); border-radius: 7px; background: #fff; color: var(--ink); font-size: 12px; font-weight: 700; }
  .btn:hover:not(:disabled) { background: var(--strip); }
  .btn.go { border-color: var(--done); background: var(--done); color: #fff; }
  .btn.go:hover:not(:disabled) { background: #255a3f; }
  .btn:disabled { opacity: 0.6; cursor: progress; }
  .sup :global(.acts) { padding: 2px 14px; border: 1px solid var(--line); border-radius: var(--r-well); background: var(--panel); }
  .none { padding: 6px 0; color: var(--mute); font-size: 12.5px; }
  .err { font-size: 12px; color: #8f241d; }
</style>
