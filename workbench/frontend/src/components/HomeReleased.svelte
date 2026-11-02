<script lang="ts">
  import { doingText, type Health, type ReleasedStory } from "../lib/home";
  import { graphHref } from "../lib/router";

  let {
    stories, health, slug, marks,
  }: {
    /** Every story released to Sortie and not yet done, in release order. */
    stories: ReleasedStory[];
    health: Health | undefined;
    slug: string;
    marks: Set<string>;
  } = $props();

  // The time a working story has run counts on between updates.
  let now = $state(Date.now() / 1000);
  $effect(() => {
    const timer = setInterval(() => (now = Date.now() / 1000), 15_000);
    return () => clearInterval(timer);
  });
</script>

<section class="released" aria-labelledby="released-h">
  <h2 id="released-h">In Sortie<span>{stories.length} released {stories.length === 1 ? "story" : "stories"}, in the order they were released</span></h2>
  {#if !stories.length}
    <p class="none">No story is released and not yet done.</p>
  {:else}
    <ol>
      {#each stories as s (s.key)}
        <li class="row {s.state}" class:moved={marks.has(s.key)} data-released={s.key} data-state={s.state}>
          <span class="id">{#if s.url}<a href={s.url} target="_blank" rel="noreferrer">{s.identifier}</a>{:else}{s.identifier}{/if}</span>
          <a class="name" href={graphHref(slug, { epic: s.change, selected: s.key })} title="{s.key}: {s.title}">{s.label}</a>
          <span class="doing" title={s.state === "needs_you" ? (s.question ?? undefined) : undefined}><span class="what">{doingText(s, now)}</span>{#if s.state === "in_review" && s.pr}{" "}{#if s.pr.url}<a class="pr" href={s.pr.url} target="_blank" rel="noreferrer">{s.pr.ref}</a>{:else}<span class="pr">{s.pr.ref}</span>{/if}{/if}</span>
        </li>
      {/each}
    </ol>
  {/if}
  <p class="figure" data-figure="slots">
    {#if health?.slots != null}
      <b>{health.busy ?? 0} of {health.slots}</b> agent {health.slots === 1 ? "slot" : "slots"} in use
    {:else if health?.busy != null}
      <b>{health.busy}</b> agent {health.busy === 1 ? "slot" : "slots"} in use; Sortie does not report how many it has
    {:else}
      Agent slots in use: unknown
    {/if}
  </p>
</section>

<style>
  .released { padding: 22px var(--home-x) 0; }
  h2 { font-size: 15px; font-weight: 700; margin-bottom: 8px; }
  h2 span { margin-left: 8px; font-size: 12px; font-weight: 600; color: var(--mute); }
  .none { padding: 6px 0; color: var(--mute); font-size: 12.5px; }
  ol { list-style: none; border-top: 1px solid var(--line); }
  .row { display: grid; grid-template-columns: 84px minmax(120px, 0.6fr) minmax(0, 1.4fr); gap: 12px; align-items: baseline; padding: 7px 8px; border-bottom: 1px solid var(--hair); font-size: 12.5px; }
  .id { font: 500 11.5px var(--font-mono); color: var(--mute); white-space: nowrap; }
  .id a { color: inherit; }
  .name { font-weight: 700; color: var(--ink); text-decoration: none; overflow-wrap: anywhere; }
  .name:hover { text-decoration: underline; text-underline-offset: 3px; }
  .doing { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .what { color: var(--ink); }
  .waiting .what { color: var(--mute); }
  .needs_you .what, .held .what { font-weight: 700; color: #8f241d; }
  .starting .what { color: var(--amber-ink); }
  .pr { font: 500 11.5px var(--font-mono); color: var(--ink); }
  .figure { margin-top: 10px; font-size: 12.5px; color: var(--mute); }
  .figure b { color: var(--ink); font-variant-numeric: tabular-nums; }
  .row.moved { animation: moved 1.4s ease-out 3; }
  @keyframes moved { from { box-shadow: 0 0 0 0 rgba(201, 138, 26, 0.5); } to { box-shadow: 0 0 0 8px rgba(201, 138, 26, 0); } }
  @media (max-width: 760px) {
    .row { grid-template-columns: 72px minmax(0, 1fr); }
    .doing { grid-column: 1 / -1; white-space: normal; }
  }
</style>
