<script lang="ts">
  import { onDestroy } from "svelte";
  import Icon from "./Icon.svelte";
  import GrantBox from "./GrantBox.svelte";
  import QuestionBox from "./QuestionBox.svelte";
  import { statusWord, type RunClient, type RunState } from "../lib/run";
  import type { Question } from "../lib/types";

  /** What the Sortie that holds a story does with it: its status, why, what
   * releases it, its pull request, and the action that unblocks it, at the
   * top of the story's panel (agent-platform/run-supervision). */
  let {
    identifier, story, runs, question = null, canAct, onanswer, onwatch, onlastrun, onblocker, every = 15_000, withActions = true,
  }: {
    identifier: string;
    /** The story's plan key. */
    story: string;
    runs: RunClient;
    /** The open question, as the graph carries it, until the run info says. */
    question?: Question | null;
    canAct: boolean;
    onanswer: (text: string) => Promise<string | null>;
    onwatch?: () => void;
    onlastrun?: () => void;
    /** Select a blocker by its tracker identifier. */
    onblocker?: (identifier: string) => void;
    every?: number;
    /** False while another pane shows the action. */
    withActions?: boolean;
  } = $props();

  let info = $state<RunState | null>(null);
  let error = $state("");

  async function load() {
    try {
      info = await runs.state(identifier);
      error = "";
    } catch (err) {
      error = err instanceof Error ? err.message : "Sortie did not say what it does with this story.";
    }
  }
  load();
  // svelte-ignore state_referenced_locally
  const timer = setInterval(load, every);
  onDestroy(() => clearInterval(timer));

  const q = $derived(info?.question ?? question);
  const hot = $derived(!!info && ["parked", "budget", "blocked", "retrying"].includes(info.status));
</script>

<section class="run" class:hot data-run-state={info?.status ?? "loading"} aria-label="Run state">
  {#if !info}
    <p class="none">{error || "Reading what Sortie does with it…"}</p>
  {:else}
    <div class="head">
      <b><span class="dot" aria-hidden="true"></span>{statusWord(info)}</b>
      <span class="on">on {info.sortie}</span>
    </div>
    {#if info.headline}<p class="headline">{info.headline}</p>{/if}
    {#each info.why as w (w)}<p class="why">{w}</p>{/each}
    {#if info.blockers.length}
      <p class="why">Waits for {#each info.blockers as b, i (b.identifier)}{i ? ", " : ""}<button type="button" class="link" onclick={() => onblocker?.(b.identifier)}>{b.identifier}</button>{b.state ? ` (${b.state})` : ""}{/each}.</p>
    {/if}
    {#if info.releases}<p class="releases" data-releases><span>Releases it</span> {info.releases}</p>{/if}
    {#if info.pulls.length}
      <ul class="pulls">
        {#each info.pulls as p (p.number)}
          <li data-pull={p.number}>
            {#if p.url}<a href={p.url} target="_blank" rel="noopener">{p.repository ?? ""}#{p.number} <Icon name="external" size={12} /></a>{:else}<span>#{p.number}</span>{/if}
            <span class="waits" class:ready={p.ready}>{p.waits.replace(/^#\d+ /, "")}</span>
          </li>
        {/each}
      </ul>
    {/if}
    {#if info.next.length && info.status === "reaction"}
      <ul class="next">{#each info.next as n (n.condition)}<li>{n.condition}: {n.text}</li>{/each}</ul>
    {/if}

    <div class="act">
      {#if !withActions}
        <span class="none">Its action is in the run below.</span>
      {:else if info.status === "parked" && q}
        {#key identifier}
          <QuestionBox question={q} {story} canAnswer={canAct} onanswer={async (t) => { const e = await onanswer(t); if (!e) load(); return e; }} />
        {/key}
      {:else if info.status === "budget" && info.action === "grant"}
        <GrantBox {identifier} sortie={info.sortie} used={info.budget?.used} sessions={info.budget?.sessions}
          canGrant={info.can_grant} maxSessions={info.max_sessions} {canAct}
          ongrant={async (n) => { const r = await runs.grant(identifier, n); if (r.ok) load(); return r; }} />
      {/if}
      <div class="buttons">
        {#if info.running?.watchable && onwatch}
          <button type="button" class="btn ink" data-act="open-session" onclick={onwatch}>Open the session</button>
        {:else if info.status === "running"}
          <span class="none">Its broker has not reported yet, so its session cannot be opened.</span>
        {/if}
        {#if info.status !== "running" && info.runs.length && onlastrun}
          <button type="button" class="btn" data-act="last-run" onclick={onlastrun}>Open the last run</button>
        {/if}
        {#each info.pulls.filter((p) => p.url) as p (p.number)}
          <a class="btn" data-act="open-pr" href={p.url} target="_blank" rel="noopener">Open #{p.number}</a>
        {/each}
      </div>
    </div>
    {#if error}<p class="err" role="alert">{error}</p>{/if}
  {/if}
</section>

<style>
  .run { display: flex; flex-direction: column; gap: 6px; margin-top: var(--s-md); padding: 12px 14px; border: 1px solid var(--line); border-radius: var(--r-well); background: var(--panel); font-size: 12.5px; }
  .run.hot { border-color: #ecc77f; background: var(--amber-tint); }
  .run[data-run-state="running"] { border-color: var(--ink); }
  .head { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; }
  .head b { display: inline-flex; align-items: center; gap: 8px; font-size: 14px; font-weight: 700; color: var(--ink); }
  .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--slate); }
  .run.hot .dot { background: var(--alert); }
  .run[data-run-state="running"] .dot { background: var(--amber); box-shadow: var(--shadow-pulse); }
  .run[data-run-state="reaction"] .dot { background: var(--amber); }
  .on { font: 500 11.5px var(--font-mono); color: var(--mute); }
  .headline { color: var(--ink); font-weight: 600; }
  .why { color: var(--ink); max-width: 68ch; overflow-wrap: anywhere; }
  .releases { color: var(--ink); max-width: 68ch; }
  .releases span { margin-right: 8px; font-size: 10.5px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); }
  .run.hot .releases span, .run.hot .on { color: var(--amber-ink); }
  .pulls, .next { list-style: none; display: flex; flex-direction: column; gap: 3px; }
  .pulls li { display: flex; flex-wrap: wrap; gap: 8px; align-items: baseline; }
  .pulls a { display: inline-flex; align-items: center; gap: 4px; font: 500 11.5px var(--font-mono); }
  .waits { color: var(--amber-ink); }
  .waits.ready { color: var(--done); }
  .next li { color: var(--mute); }
  .act { display: flex; flex-direction: column; gap: 8px; margin-top: 4px; }
  .buttons { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .btn { height: 30px; display: inline-flex; align-items: center; padding: 0 12px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; color: var(--ink); font-size: 12.5px; font-weight: 600; text-decoration: none; }
  .btn:hover { border-color: var(--ink); }
  .btn.ink { background: var(--ink); border-color: var(--ink); color: #fff; }
  .btn.ink:hover { background: #000; }
  .link { font: 500 11.5px var(--font-mono); text-decoration: underline; text-underline-offset: 3px; text-decoration-color: var(--line); }
  .link:hover { text-decoration-color: var(--ink); }
  .none { color: var(--mute); font-size: 12px; }
  .err { color: var(--alert); font-size: 12px; }
</style>
