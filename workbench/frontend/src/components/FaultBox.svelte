<script lang="ts">
  import type { HomeFault } from "../lib/home";
  import { stoppedAt } from "../lib/questions";
  import { graphHref } from "../lib/router";

  let {
    fault, slug, canAct, ondone,
  }: {
    fault: HomeFault;
    slug: string;
    /** Only an approver marks an action done; the backend checks the role too. */
    canAct: boolean;
    /** Say the action is done, so Sortie checks the fault again at once; resolves to an error message, or null. */
    ondone: () => Promise<string | null>;
  } = $props();

  let busy = $state(false);
  let error = $state("");
  // The check this person asked for, until the fault leaves or the page shows its result.
  let asked = $state(false);
  const when = $derived(stoppedAt(fault.recorded_at));
  const done = $derived(stoppedAt(fault.done_at));

  async function markDone() {
    if (busy) return;
    busy = true;
    error = "";
    const err = await ondone();
    busy = false;
    if (err) {
      error = err;
      return;
    }
    asked = true;
  }
</script>

<div class="fault" data-fault={fault.id}>
  <p class="cause" data-cause>{fault.cause || "Sortie gave no cause."}</p>
  <dl>
    <dt>Do this</dt>
    <dd data-action>{fault.action}</dd>
    {#if fault.where}
      <dt>Where</dt>
      <dd data-where>{fault.where}</dd>
    {/if}
    <dt>Holds</dt>
    <dd data-holds>
      {#each fault.stories as s, i (s.identifier)}{i ? ", " : ""}{#if s.key}<a href={graphHref(slug, { epic: s.change ?? s.key.split("/")[0], selected: s.key })} title="{s.key}: {s.title}" data-held={s.key}>{s.label}</a>{:else}<span class="other" title="Another project's story">{s.display ?? s.identifier}</span>{/if}{/each}
    </dd>
  </dl>
  {#if fault.detail}
    <details>
      <summary>{fault.step ? `Full output of ${fault.step}` : "Full output"}</summary>
      <pre data-detail>{fault.detail}</pre>
    </details>
  {/if}
  <p class="when">
    A platform fault{fault.class ? ` (${fault.class.replace(/_/g, " ")})` : ""}, recorded {#if when}<time datetime={when.iso}>{when.text}</time>{:else}at a time Sortie does not report{/if} · on {fault.sortie}'s Sortie.
    The stories keep their budgets while it holds them.
  </p>
  {#if fault.last_check_error}
    <p class="still" role="status" data-check-error>
      {fault.done_by ? `Marked done by ${fault.done_by}${done ? ` ${done.text}` : ""}, but Sortie's check still fails: ` : "Sortie's last check failed: "}{fault.last_check_error}
    </p>
  {/if}
  {#if fault.checking || (asked && !fault.last_check_error)}
    <p class="sent" role="status">Sortie is checking again; the stories run once it passes.</p>
  {/if}
  {#if canAct}
    <button type="button" class="send" data-act="fault-done" disabled={busy || fault.checking} onclick={markDone}>{busy ? "Checking…" : "Done"}</button>
  {:else}
    <p class="none" data-note="approver">Marking it done needs the approver role.</p>
  {/if}
  {#if error}<p class="err" role="alert">{error}</p>{/if}
</div>

<style>
  .fault { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
  .cause { font: 500 12px var(--font-mono); line-height: 1.5; color: var(--ink); overflow-wrap: anywhere; max-width: 72ch; }
  dl { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 2px 10px; font-size: 12.5px; }
  dt { font-size: 11px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); line-height: 19px; }
  dd { color: var(--ink); overflow-wrap: anywhere; }
  dd[data-action] { font-weight: 700; }
  dd a { color: var(--ink); font-weight: 600; }
  .other { color: var(--mute); }
  details { font-size: 12px; }
  summary { cursor: pointer; color: var(--mute); font-weight: 600; }
  pre { margin-top: 4px; max-height: 240px; overflow: auto; padding: 8px 10px; border: 1px solid var(--line); border-radius: var(--r-card); background: var(--strip); font: 500 11.5px/1.45 var(--font-mono); white-space: pre-wrap; overflow-wrap: anywhere; }
  .when { font-size: 11.5px; color: var(--mute); }
  .still { font-size: 12px; color: #8f241d; overflow-wrap: anywhere; }
  .send { align-self: flex-start; height: 28px; padding: 0 12px; border: 1px solid var(--ink); border-radius: 7px; background: var(--ink); color: #fff; font-size: 12px; font-weight: 700; }
  .send:hover:not(:disabled) { background: #000; }
  .send:disabled { opacity: 0.6; cursor: default; }
  .none { font-size: 12px; color: var(--mute); }
  .sent { font-size: 12px; color: var(--done); }
  .err { font-size: 11.5px; color: #8f241d; }
</style>
