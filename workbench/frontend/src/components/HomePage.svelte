<script lang="ts">
  import type { Snippet } from "svelte";
  import { progressLines, REASON_TEXT, type HomeDoc } from "../lib/home";
  import { graphHref } from "../lib/router";
  import type { MergedGraph } from "../lib/types";
  import HomeNeeds from "./HomeNeeds.svelte";
  import HomeTimeline from "./HomeTimeline.svelte";
  import TopBar, { type Tab } from "./TopBar.svelte";
  import WorkerLanes from "./WorkerLanes.svelte";

  let {
    doc, slug, projectName = "", tabs = [], marks = new Set(), onapprove, onanswer, onreplace, actions,
  }: {
    doc: MergedGraph;
    slug: string;
    projectName?: string;
    tabs?: Tab[];
    /** Stories whose state just changed. */
    marks?: Set<string>;
    onapprove: (keys: string[]) => Promise<string | null>;
    onanswer: (key: string, text: string) => Promise<string | null>;
    onreplace?: (replacement: string) => Promise<string | null>;
    actions?: Snippet;
  } = $props();

  const home: HomeDoc | null = $derived(doc.home && !doc.home.error ? doc.home : null);
  const health = $derived(home?.health);
  const goal = $derived(home?.milestones.find((m) => m.id === home?.goal));
  const next = $derived(home?.milestones.find((m) => m.id === home?.next));
  const live = $derived(!doc.sortie.reachable ? "Sortie unreachable" : `Sortie · ${health?.busy ?? 0} running`);
  const PATH_MAX = 7;
  const chain = $derived.by(() => {
    const stories = home?.critical_path?.stories ?? [];
    if (stories.length <= PATH_MAX) return { head: stories, skipped: [] as typeof stories, tail: [] as typeof stories };
    return { head: stories.slice(0, PATH_MAX - 2), skipped: stories.slice(PATH_MAX - 2, -1), tail: stories.slice(-1) };
  });
  const storyHref = (key: string) => graphHref(slug, { epic: key.split("/")[0], selected: key });
  const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
</script>

<TopBar crumbs={[{ label: "Projects", href: "#/" }, { label: projectName || slug }]} {tabs}>
  <span class="live" class:down={!doc.sortie.reachable} title={doc.sortie.error ?? live}><span class="pulse" aria-hidden="true"></span><span class="live-text">{live}</span></span>
  {#if actions}{@render actions()}{/if}
</TopBar>

<main class="home" aria-label="Project home">
  {#if doc.home?.error}
    <p class="band alert" role="alert">The home could not be computed: {doc.home.error}</p>
  {/if}
  {#if home && !home.plan.valid}
    <p class="band alert" role="status" data-notice="invalid">
      The plan has {plural(home.plan.errors, "problem", "problems")}; this page shows the last valid plan. <a href={graphHref(slug)}>See them in the graph</a>.
    </p>
  {/if}
  {#if !doc.sortie.reachable}
    <p class="band warn" role="status" data-notice="sortie">
      Sortie is unreachable{doc.sortie.error ? `: ${doc.sortie.error}` : ""}. Tasks come from the store and still count; {home?.story_progress === "last_known" ? "story progress is the last known" : "story progress is unknown"}.
    </p>
  {/if}
  {#if home && !home.milestones_declared}
    <p class="band" role="status" data-notice="no-milestones">
      No milestones are declared, so the whole plan is one milestone. Declare them in <code>openspec/milestones.yaml</code>.
    </p>
  {/if}

  {#if !home}
    <div class="empty"><b>Nothing to show yet</b>The home appears once the plan validates. Open the graph to start planning.</div>
  {:else}
    <section class="score" aria-labelledby="goal-h">
      <h1 id="goal-h">{goal?.title ?? "The plan"}<span>
        {#if goal?.done}the goal is reached{:else}{goal?.short ?? "It"} is the goal · {Math.round((home.timeline?.to_goal ?? 0) * 100)}% of the work to it is done{/if}
      </span></h1>
      {#if home.timeline}<HomeTimeline {home} {slug} {marks} />{/if}
    </section>

    <WorkerLanes workers={home.workers ?? []} {slug} />

    {#if home.critical_path && next}
      <section class="path" aria-labelledby="path-h">
        <h2 id="path-h" class="t">Critical path<span>to the {next.short} {home.critical_path.gate ? "gate" : "finish"}</span></h2>
        {#if !home.critical_path.stories.length}
          <p class="cap">No unfinished story stands before it.</p>
        {:else}
          <ol class="chain">
            {#each chain.head as s, i (s.key)}
              {#if i > 0}<li class="arr" aria-hidden="true"></li>{/if}
              <li><a class="node" class:need={!!s.needs && s.needs !== "waits_on_work"} class:gate={s.key === home.critical_path.gate} class:moved={marks.has(s.key)}
                href={storyHref(s.key)} title="{s.key}: {s.title}"><span class="mono">{s.key === home.critical_path.gate ? "gate" : s.running ? "running" : s.needs ? REASON_TEXT[s.needs] : "ready"}</span><b>{s.label}</b>{#if s.waited_on}<span class="mono of" data-change={s.change}>{s.change}</span>{/if}</a></li>
            {/each}
            {#if chain.skipped.length}
              <li class="arr" aria-hidden="true"></li>
              <li><span class="node skipped" title={chain.skipped.map((s) => s.key).join(", ")}><span class="mono">+{chain.skipped.length} stories</span><b>{[...new Set(chain.skipped.map((s) => s.change))].join(", ")}</b></span></li>
            {/if}
            {#each chain.tail as s (s.key)}
              <li class="arr" aria-hidden="true"></li>
              <li><a class="node" class:need={!!s.needs && s.needs !== "waits_on_work"} class:gate={s.key === home.critical_path.gate} class:moved={marks.has(s.key)}
                href={storyHref(s.key)} title="{s.key}: {s.title}"><span class="mono">{s.key === home.critical_path.gate ? "gate" : s.needs ? REASON_TEXT[s.needs] : "ready"}</span><b>{s.label}</b>{#if s.waited_on}<span class="mono of" data-change={s.change}>{s.change}</span>{/if}</a></li>
            {/each}
          </ol>
        {/if}
      </section>
    {/if}

    <HomeNeeds items={home.not_ready} questions={home.questions ?? []} {slug} {marks} {onapprove} {onanswer} {onreplace} />

    <section class="progress" aria-labelledby="ms-h">
      <h2 id="ms-h">Milestones<span>stories, agent tasks and person tasks are counted apart, never summed</span></h2>
      <table>
        <thead><tr><th scope="col">Milestone</th><th scope="col" class="n">Stories</th><th scope="col" class="n">Agent tasks</th><th scope="col" class="n">Person tasks</th><th scope="col">State</th></tr></thead>
        <tbody>
          {#each home.milestones as m (m.id)}
            <tr data-milestone-row={m.id} class:next={m.next}>
              <th scope="row"><a href={graphHref(slug, { mark: m.id })}><b class="ms">{m.short}</b> {m.title.startsWith(m.short) ? m.title.slice(m.short.length).replace(/^[:\s]+/, "") : ""}</a></th>
              <td class="n">{m.progress.stories.total ? `${home.story_progress === "unknown" ? "?" : m.progress.stories.done} / ${m.progress.stories.total}` : "—"}{#if m.progress.stories.total && home.story_progress === "last_known"}<small>last known</small>{/if}</td>
              <td class="n">{m.progress.agent_tasks.total ? `${m.progress.agent_tasks.done} / ${m.progress.agent_tasks.total}` : "—"}</td>
              <td class="n">{m.progress.person_tasks.total ? `${m.progress.person_tasks.done} / ${m.progress.person_tasks.total}` : "—"}{#if m.progress.person_tasks.total}<small>{`${m.progress.metal_tasks.total} metal · ${m.progress.operator_tasks.total} operator`}</small>{/if}</td>
              <td>{#if m.done}<span class="st done">done</span>{:else if m.next}<span class="st next">next</span>{:else}<span class="st">ahead</span>{/if}{#if m.goal}<span class="st goal">goal</span>{/if}</td>
            </tr>
          {/each}
        </tbody>
      </table>
      <p class="sr-only">{home.milestones.map((m) => `${m.title}: ${progressLines(m).join(", ")}`).join(". ")}</p>
    </section>
  {/if}
</main>

<style>
  .home { --home-x: 40px; flex: 1; min-height: 0; overflow-y: auto; scrollbar-color: #b9bab3 var(--hair); padding-bottom: 32px; }
  .live { display: flex; align-items: center; gap: 8px; font-size: 12px; color: #d1d1d6; white-space: nowrap; }
  .live.down { color: var(--ink-mute); }
  .pulse { width: 8px; height: 8px; border-radius: 50%; background: var(--amber); box-shadow: var(--shadow-pulse); }
  .live.down .pulse { background: var(--ink-mute); box-shadow: none; }
  @container (max-width: 600px) { .live-text { display: none; } }

  .band { padding: 9px var(--home-x); border-bottom: 1px solid var(--line); background: var(--panel); font-size: 12.5px; color: var(--ink); }
  .band.alert { border-color: #e9b8b3; background: #fbeceb; color: #8f241d; }
  .band.warn { border-color: #ecd9a8; background: var(--amber-tint); color: var(--amber-ink); }
  .band a { color: inherit; font-weight: 600; }
  code { font: 500 11.5px var(--font-mono); }
  .empty { margin: 80px var(--home-x); max-width: 44ch; color: var(--mute); }
  .empty b { display: block; margin-bottom: 4px; font: italic 400 20px var(--font-serif); color: var(--ink); }

  .score { padding: 22px var(--home-x) 6px; background: var(--strip); border-bottom: 1px solid var(--line); }
  h1 { margin-bottom: 30px; font-size: 26px; font-weight: 700; letter-spacing: -0.02em; line-height: 1.12; text-wrap: balance; }
  h1 span { margin-left: 10px; font-size: 15px; font-weight: 400; letter-spacing: 0; color: var(--mute); }

  .cap { margin-top: 4px; font-size: 11px; color: var(--mute); }

  .path { display: flex; align-items: center; gap: 16px; padding: 18px var(--home-x) 0; }
  .t { flex: none; width: 108px; font-size: 11px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; line-height: 1.3; color: var(--mute); }
  .t span { display: block; font-weight: 600; letter-spacing: 0; text-transform: none; }
  .chain { min-width: 0; display: flex; align-items: center; flex-wrap: wrap; row-gap: 8px; list-style: none; }
  .node { display: block; min-width: 112px; max-width: 200px; padding: 6px 10px; border: 1.5px solid var(--line); border-radius: var(--r-card); background: #fff; color: var(--ink); text-decoration: none; }
  .node b { display: block; font-size: 12.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  a.node:hover { box-shadow: var(--shadow-lift); }
  .node.need { border-style: dashed; border-color: var(--alert); }
  .node.gate { border: 2px solid var(--ink-rule); }
  .node.skipped { border-style: dotted; color: var(--mute); }
  .node .of { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .mono { font: 500 10.5px var(--font-mono); color: var(--mute); }
  .arr { width: 18px; height: 1.5px; margin: 0 1px; background: var(--slate); position: relative; }
  .arr::after { content: ""; position: absolute; right: -1px; top: -3.25px; border: 4px solid transparent; border-left-color: var(--slate); }
  .node.moved { animation: moved 1.4s ease-out 3; }
  @keyframes moved { from { box-shadow: 0 0 0 0 rgba(201, 138, 26, 0.55); } to { box-shadow: 0 0 0 8px rgba(201, 138, 26, 0); } }

  .home :global(.needs) { padding: 22px var(--home-x) 0; }

  .progress { padding: 26px var(--home-x) 0; }
  h2 { font-size: 15px; font-weight: 700; margin-bottom: 8px; }
  h2:not(.t) span { margin-left: 8px; font-size: 12px; font-weight: 600; color: var(--mute); }
  table { width: 100%; border-collapse: collapse; font-variant-numeric: tabular-nums; }
  th, td { padding: 7px 8px; border-bottom: 1px solid var(--hair); text-align: left; vertical-align: baseline; }
  thead th { padding: 4px 8px; border-bottom: 1px solid var(--line); font-size: 11px; font-weight: 700; letter-spacing: 0.05em; text-transform: uppercase; color: var(--mute); }
  tbody th { font-weight: 600; }
  tbody th a { color: var(--ink); text-decoration: none; }
  tbody th a:hover { text-decoration: underline; text-underline-offset: 3px; }
  .ms { font: italic 400 17px var(--font-serif); margin-right: 4px; }
  .n { text-align: right; white-space: nowrap; }
  td small { display: block; font-size: 11px; color: var(--mute); }
  tr.next { background: var(--strip); }
  .st { display: inline-block; margin-right: 6px; padding: 1px 8px; border: 1px solid var(--line); border-radius: var(--r-pill); font-size: 11px; font-weight: 700; color: var(--mute); }
  .st.done { border-color: #bcd6c6; background: var(--done-tint); color: var(--done); }
  .st.next { border-color: var(--ink); color: var(--ink); }
  .st.goal { border-color: var(--ink); background: var(--ink); color: #fff; }
  .sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }

  @media (max-width: 760px) {
    .home { --home-x: 16px; }
    h1 { font-size: 22px; margin-bottom: 26px; }
    h1 span { display: block; margin: 6px 0 0; font-size: 13.5px; }
    .path { flex-direction: column; align-items: flex-start; gap: 8px; }
    .t { width: auto; }
    .progress { overflow-x: auto; }
  }
</style>
