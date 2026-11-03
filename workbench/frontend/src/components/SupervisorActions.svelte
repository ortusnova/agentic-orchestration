<script lang="ts">
  import { graphHref } from "../lib/router";
  import { actionText, iso, resultText, resultTone, when } from "../lib/supervisor";
  import type { SupervisorAction } from "../lib/types";
  import Icon from "./Icon.svelte";

  let {
    actions, slug = null, onselect,
  }: {
    actions: SupervisorAction[];
    /** With a slug, each action links to the story it touched; beside the story, it need not. */
    slug?: string | null;
    /** Select the story in the graph, instead of following a link to it. */
    onselect?: (key: string) => void;
  } = $props();

  const SHA = /^[0-9a-f]{40}$/;
  const shown = (v: string) => (SHA.test(v) ? v.slice(0, 7) : v);
</script>

<ol class="acts">
  {#each actions as a (a.id || `${a.action}@${a.at}`)}
    <li class="act" data-action={a.action} data-result={a.result} data-act-id={a.id} data-act-key={a.key}>
      <div class="line">
        <b class="what">{actionText(a)}</b>
        <span class="res {resultTone(a.result)}">{resultText(a.result)}</span>
        {#if a.at != null}<time datetime={iso(a.at)}>{when(a.at)}</time>{/if}
      </div>
      {#if (slug && a.key) || a.pull}
        <div class="refs">
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
      {/if}
      {#if a.detail}<p class="detail">{a.detail}</p>{/if}
      {#if a.evidence.length}
        <dl class="ev" aria-label="Evidence">
          {#each a.evidence as e, i (i)}
            <div data-evidence={e.name ?? ""}>
              <dt>{e.name ?? "evidence"}</dt>
              <dd>{#if e.url}<a class="mono" href={e.url} target="_blank" rel="noopener">{shown(e.value)}</a>{:else}<span class:mono={SHA.test(e.value)}>{shown(e.value)}</span>{/if}</dd>
            </div>
          {/each}
        </dl>
      {/if}
    </li>
  {/each}
</ol>

<style>
  .acts { list-style: none; display: flex; flex-direction: column; }
  .act { display: flex; flex-direction: column; gap: 3px; padding: 8px 0; border-bottom: 1px solid var(--hair); font-size: 12.5px; }
  .act:last-child { border-bottom: 0; }
  .line { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 8px; }
  .what { font-weight: 700; color: var(--ink); }
  .res { padding: 0 6px; border: 1px solid var(--line); border-radius: var(--r-tag); font-size: 10.5px; font-weight: 700; line-height: 16px; text-transform: uppercase; letter-spacing: 0.04em; }
  .res.ok { border-color: #bcd6c6; background: var(--done-tint); color: var(--done); }
  .res.refused { border-color: #ecd9a8; background: var(--amber-tint); color: var(--amber-ink); }
  .res.failed { border-color: #e9b8b3; background: #fbeceb; color: #8f241d; }
  time { margin-left: auto; font-size: 11px; color: var(--mute); white-space: nowrap; }
  .refs { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 10px; }
  .story { font-weight: 600; color: var(--ink); text-decoration: underline; text-underline-offset: 3px; text-decoration-color: var(--line); }
  .story:hover { text-decoration-color: var(--ink); }
  .ext { display: inline-flex; align-items: center; gap: 4px; font: 500 11.5px var(--font-mono); }
  .mono { font: 500 11.5px var(--font-mono); color: var(--mute); word-break: break-word; }
  a.mono { color: inherit; }
  .detail { color: var(--mute); }
  .ev { display: grid; grid-template-columns: max-content 1fr; gap: 2px 10px; margin-top: 2px; }
  .ev div { display: contents; }
  .ev dt { font-size: 10.5px; font-weight: 700; letter-spacing: 0.05em; text-transform: uppercase; color: var(--mute); padding-top: 1px; }
  .ev dd { min-width: 0; overflow-wrap: anywhere; color: var(--ink); }
</style>
