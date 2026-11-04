<script lang="ts">
  import { graphHref } from "../lib/router";
  import { actionText, clock, detailOf, iso, linkParts, resultText, resultTone, when } from "../lib/supervisor";
  import type { SupervisorAction } from "../lib/types";
  import Icon from "./Icon.svelte";

  let {
    actions, slug = null, onselect, first = null, total = null, scope = null, compact = false,
  }: {
    actions: SupervisorAction[];
    /** With a slug, each action links to the story it touched; beside the story, it need not. */
    slug?: string | null;
    /** Select the story in the graph, instead of following a link to it. */
    onselect?: (key: string) => void;
    /** How many of the newest show until a person asks for all; all with none. */
    first?: number | null;
    /** How many Sortie recorded, when more than it served. */
    total?: number | null;
    /** What the count is of, such as "on this project's stories"; the foot names the total with it. */
    scope?: string | null;
    /** The narrower time column of a story's details. */
    compact?: boolean;
  } = $props();

  const SHA = /^[0-9a-f]{40}$/;
  const shown = (v: string) => (SHA.test(v) ? v.slice(0, 7) : v);

  let all = $state(false);
  const visible = $derived(all || first == null ? actions : actions.slice(0, first));
  const count = $derived(Math.max(total ?? 0, actions.length));
</script>

<div class="acts" class:compact>
  <ol>
    {#each visible as a (a.id || `${a.action}@${a.at}`)}
      {@const why = a.detail ? detailOf(a.detail) : null}
      {@const at = a.at != null ? clock(a.at) : null}
      <li class="act" data-action={a.action} data-result={a.result} data-act-id={a.id} data-act-key={a.key}>
        {#if at}<time datetime={iso(a.at)} title={when(a.at)}>{at.time}{#if at.day}<small>{at.day}</small>{/if}</time>{:else}<span></span>{/if}
        <div class="what">
          <b>{actionText(a)}</b>
          {#if slug && a.key}
            {#if onselect}
              <button type="button" class="story" data-story={a.key} onclick={() => onselect(a.key!)}>{a.label ?? a.key}</button>
            {:else}
              <a class="story" data-story={a.key} href={graphHref(slug, { epic: a.key.split("/")[0], selected: a.key })} title={a.title ? `${a.key}: ${a.title}` : a.key}>{a.label ?? a.key}</a>
            {/if}
            {#if a.identifier}<span class="mono">{a.identifier}</span>{/if}
          {/if}
          {#if a.pull}
            {#if a.pull.url}
              <a class="ext" data-pull={a.pull.ref} href={a.pull.url} target="_blank" rel="noopener">{a.pull.ref} <Icon name="external" size={12} /></a>
            {:else}<span class="mono" data-pull={a.pull.ref}>{a.pull.ref}</span>{/if}
          {/if}
        </div>
        <span class="res {resultTone(a.result)}">{resultText(a.result)}</span>
        {#if why}
          <p class="why detail {resultTone(a.result)}">Sortie: {#if why.code}<code>{why.code}</code>{" "}{/if}{why.message}</p>
        {/if}
        {#if a.evidence.length}
          <details class="ev">
            <summary><b>Evidence</b>{#each a.evidence as e, i (i)}<span data-evidence={e.name ?? ""}>{#if e.name}<i>{e.name}</i>{/if}{#if e.url}<a class="mono" href={e.url} target="_blank" rel="noopener">{shown(e.value)}</a>{:else if e.name}<span class:mono={SHA.test(e.value)}>{shown(e.value)}</span>{:else}{#each linkParts(e.value) as p, j (j)}{#if p.url}<a href={p.url} target="_blank" rel="noopener">{p.text}</a>{:else}{p.text}{/if}{/each}{/if}</span>{/each}</summary>
          </details>
        {/if}
      </li>
    {/each}
  </ol>
  {#if count > visible.length}
    <div class="foot">
      <span>The {visible.length} newest{scope ? ` of ${count} ${scope}` : ""}</span>
      {#if actions.length > visible.length}
        <button type="button" class="btn" data-act="show-all-actions" onclick={() => (all = true)}>{actions.length < count ? `Show the ${actions.length} newest` : `Show all ${count}`}</button>
      {/if}
    </div>
  {/if}
</div>

<style>
  ol { list-style: none; display: flex; flex-direction: column; }
  .act { display: grid; grid-template-columns: 72px 1fr auto; column-gap: 14px; padding: 10px 0; border-bottom: 1px solid var(--hair); font-size: 12.5px; }
  .compact .act { grid-template-columns: 52px 1fr auto; column-gap: 10px; }
  .act:last-child { border-bottom: 0; }
  time { padding-top: 1px; font-size: 12px; font-weight: 600; color: var(--mute); font-variant-numeric: tabular-nums; white-space: nowrap; }
  time small { display: block; font-size: 11px; font-weight: 500; }
  .what { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 10px; min-width: 0; }
  .what b { font-weight: 700; color: var(--ink); }
  .res { align-self: start; padding: 0 6px; border: 1px solid var(--line); border-radius: var(--r-tag); font-size: 10.5px; font-weight: 700; line-height: 17px; text-transform: uppercase; letter-spacing: 0.04em; }
  .res.ok { border-color: #bcd6c6; background: var(--done-tint); color: var(--done); }
  .res.refused { border-color: #ecd9a8; background: var(--amber-tint); color: var(--amber-ink); }
  .res.failed { border-color: #e9b8b3; background: #fbeceb; color: #8f241d; }
  .story { font-weight: 600; color: var(--ink); text-decoration: underline; text-underline-offset: 3px; text-decoration-color: var(--line); }
  .story:hover { text-decoration-color: var(--ink); }
  .ext { display: inline-flex; align-items: center; gap: 4px; font: 500 11.5px var(--font-mono); }
  .mono { font: 500 11.5px var(--font-mono); color: var(--mute); word-break: break-word; }
  a.mono { color: inherit; }
  .why { grid-column: 2 / 4; margin-top: 3px; font-weight: 600; color: var(--amber-ink); }
  .why.failed { color: #8f241d; }
  .why code { font: 500 11.5px var(--font-mono); }
  .ev { grid-column: 2 / 4; margin-top: 3px; color: var(--mute); }
  .ev summary { list-style: none; cursor: pointer; display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; max-width: 150ch; }
  .ev summary::-webkit-details-marker { display: none; }
  .ev summary:hover, .ev[open] summary { color: var(--ink); }
  .ev[open] summary { display: block; -webkit-line-clamp: none; line-clamp: none; }
  .ev summary b { margin-right: 8px; font-size: 10.5px; font-weight: 700; letter-spacing: 0.05em; text-transform: uppercase; }
  .ev summary i { margin: 0 6px 0 10px; font-style: normal; font-size: 10.5px; font-weight: 700; letter-spacing: 0.05em; text-transform: uppercase; }
  .ev summary a { color: var(--ink); text-underline-offset: 3px; }
  .foot { display: flex; align-items: center; gap: 12px; padding: 9px 0 11px; border-top: 1px solid var(--line); font-size: 12px; color: var(--mute); }
  .btn { height: 26px; padding: 0 14px; border: 1px solid var(--line); border-radius: 7px; background: #fff; color: var(--ink); font-size: 12px; font-weight: 700; }
  .btn:hover { border-color: var(--ink); }
</style>
