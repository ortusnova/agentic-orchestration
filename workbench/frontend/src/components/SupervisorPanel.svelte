<script lang="ts">
  import { can } from "../lib/identity.svelte";
  import { canPause, canResume, iso, statusText, when } from "../lib/supervisor";
  import type { Supervisor } from "../lib/types";
  import SupervisorActions from "./SupervisorActions.svelte";

  let {
    supervisor, slug, dispatched, reachable, oncontrol,
  }: {
    supervisor: Supervisor | null | undefined;
    slug: string;
    /** Whether Sortie runs agents for this project, and so supervises its runs. */
    dispatched: boolean;
    reachable: boolean;
    /** Pause or resume the supervisor; resolves to an error message, or null. */
    oncontrol?: (op: "pause" | "resume") => Promise<string | null>;
  } = $props();

  let busy: "pause" | "resume" | null = $state(null);
  let error = $state("");

  async function control(op: "pause" | "resume") {
    if (!oncontrol) return;
    busy = op;
    error = "";
    error = (await oncontrol(op)) ?? "";
    busy = null;
  }
</script>

<section class="sup" aria-labelledby="sup-h" data-supervisor={supervisor?.status ?? "none"}>
  <div class="head">
    <h2 id="sup-h">Supervisor<span>Sortie's own agent: what it did to the runs, and what it was refused</span></h2>
    {#if supervisor}
      <div class="ctl">
        <span class="st {supervisor.status}" data-supervisor-status={supervisor.status}><i aria-hidden="true"></i>{statusText(supervisor)}</span>
        {#if supervisor.status === "paused" && (supervisor.paused_by || supervisor.paused_at != null)}
          <span class="by">{supervisor.paused_by ? `paused by ${supervisor.paused_by}` : "paused"}{#if supervisor.paused_at != null}{" · "}<time datetime={iso(supervisor.paused_at)}>{when(supervisor.paused_at)}</time>{/if}</span>
        {/if}
        {#if can("approver") && oncontrol}
          {#if canPause(supervisor)}
            <button type="button" class="btn" data-act="pause-supervisor" disabled={!!busy} onclick={() => control("pause")}>{busy === "pause" ? "Pausing…" : "Pause"}</button>
          {:else if canResume(supervisor)}
            <button type="button" class="btn go" data-act="resume-supervisor" disabled={!!busy} onclick={() => control("resume")}>{busy === "resume" ? "Resuming…" : "Resume"}</button>
          {/if}
        {/if}
      </div>
    {/if}
  </div>
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
  .st.active { border-color: #bcd6c6; background: var(--done-tint); color: var(--done); }
  .st.active i { background: var(--done); }
  .st.paused { border-color: #ecd9a8; background: var(--amber-tint); color: var(--amber-ink); }
  .st.paused i { background: var(--amber); }
  .by { font-size: 12px; color: var(--mute); }
  .btn { height: 28px; padding: 0 14px; border: 1px solid var(--ink); border-radius: 7px; background: #fff; color: var(--ink); font-size: 12px; font-weight: 700; }
  .btn:hover:not(:disabled) { background: var(--strip); }
  .btn.go { border-color: var(--done); background: var(--done); color: #fff; }
  .btn.go:hover:not(:disabled) { background: #255a3f; }
  .btn:disabled { opacity: 0.6; cursor: progress; }
  .sup :global(.acts) { padding: 2px 14px; border: 1px solid var(--line); border-radius: var(--r-well); background: var(--panel); }
  .none { padding: 6px 0; color: var(--mute); font-size: 12.5px; }
  .err { font-size: 12px; color: #8f241d; }
</style>
