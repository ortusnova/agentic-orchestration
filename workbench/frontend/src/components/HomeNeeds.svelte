<script lang="ts">
  import { can } from "../lib/identity.svelte";
  import { graphHref } from "../lib/router";
  import { plainTask, REASON_TEXT, slackText, slackWidth, type Column, type HomeQuestion, type NotReadyItem } from "../lib/home";
  import QuestionBox from "./QuestionBox.svelte";

  let {
    items, questions = [], slug, marks, onapprove, onanswer, onreplace,
  }: {
    items: NotReadyItem[];
    /** The open questions of parked stories, ranked first. */
    questions?: HomeQuestion[];
    slug: string;
    marks: Set<string>;
    /** Approve stories; resolves to an error message, or null. */
    onapprove: (keys: string[]) => Promise<string | null>;
    /** Answer a parked story's question; resolves to an error message, or null. */
    onanswer: (key: string, text: string) => Promise<string | null>;
    /** Confirm a replacement's supersede; resolves to an error message, or null. */
    onreplace?: (replacement: string) => Promise<string | null>;
  } = $props();

  // A column shows this many items until a person asks for all of them.
  const SHOWN = 5;
  const COLUMNS: { id: Column; title: string; hint: (n: NotReadyItem[]) => string }[] = [
    { id: "approve", title: "Approve", hint: (l) => `${l.length} · ${l.reduce((a, i) => a + (i.stories?.length ?? 1), 0)} stories` },
    { id: "spec", title: "Write a spec", hint: (l) => `${l.length} · plan in the graph` },
    { id: "operator", title: "Operator tasks", hint: (l) => `${l.length}` },
  ];

  const person = $derived(items.filter((i) => i.column !== null));
  const quiet = $derived(items.filter((i) => i.column === null));
  let expanded: Record<string, boolean> = $state({});
  let busy: string | null = $state(null);
  let errors: Record<string, string> = $state({});

  function href(i: NotReadyItem): string {
    const a = i.action;
    if (a.story) return graphHref(slug, { epic: a.story.split("/")[0], selected: a.story });
    if (i.kind === "story") return graphHref(slug, { epic: i.change ?? i.key.split("/")[0], selected: i.key });
    return graphHref(slug, { epic: a.epic ?? i.change ?? i.key, selected: a.epic ?? i.change ?? i.key });
  }

  function note(i: NotReadyItem): string {
    if (i.kind === "task") return [i.task?.change, i.task?.number ? `task ${i.task.number}` : null].filter(Boolean).join(" · ");
    // A grouped row names its stories, so each one can be found in the list.
    if (i.kind === "change" && i.stories) return `${i.stories.map((k) => k.split("/").pop()).join(", ")} · reviewed together`;
    const parts = [i.change ?? i.key];
    if (i.reason === "waits_on_person" && i.task) parts.push(`waits on ${i.task.change} ${i.task.number ?? ""}`.trim());
    else if (i.after?.length) parts.push(`after ${i.after.map((k) => k.split("/").pop()).join(" and ")}`);
    else parts.push(REASON_TEXT[i.reason]);
    return parts.join(" · ");
  }

  const actionLabel = (i: NotReadyItem) =>
    i.action.kind === "approve" ? "Approve"
      : i.action.kind === "review" ? (i.stories ? `Review ${i.stories.length}` : "Review")
      : i.action.kind === "task" ? "Open task" : "Plan in graph";

  async function approve(i: NotReadyItem) {
    busy = i.key;
    errors = { ...errors, [i.key]: "" };
    const err = await onapprove(i.action.keys ?? [i.key]);
    if (err) errors = { ...errors, [i.key]: err };
    busy = null;
  }
</script>

<section class="needs" aria-labelledby="needs-h">
  <div class="head">
    <h2 id="needs-h">Needs a person<span>by what you do · numbers rank all {questions.length + person.length} by urgency</span></h2>
    <p class="legend">
      <span>The bar is the time before an item holds agents up, in stories until estimates exist. Shorter is more urgent.</span>
      <span class="key"><i class="u-now"></i>blocking now</span>
      <span class="key"><i class="u-soon"></i>within 4 stories</span>
      <span class="key"><i class="u-later"></i>room to spare</span>
    </p>
  </div>
  <div class="cols">
    <div class="col" data-column="answer">
      <div class="ch"><h3>Answer</h3><span>{questions.length} · {questions.length === 1 ? "an agent waits" : "agents wait"}</span></div>
      {#if !questions.length}
        <p class="none">No agent waits on an answer.</p>
      {/if}
      <ol>
        {#each questions as q (q.key)}
          <li class="item q hot" class:moved={marks.has(q.key)} data-rank={q.rank} data-key={q.key}>
            <span class="rk" aria-label="Rank {q.rank}">{q.rank}</span>
            <div class="body">
              <a class="name" href={graphHref(slug, { epic: q.change, selected: q.key })} title="{q.key}: {q.title}">{q.label}</a>
              <span class="note">{[q.identifier, q.title].join(" · ")}</span>
              <QuestionBox question={q} story={q.key} canAnswer={can("approver")} onanswer={(text) => onanswer(q.key, text)}
                replacedBy={q.replaced_by ?? null} onreplace={onreplace && q.replaced_by ? () => onreplace(q.replaced_by!.key) : undefined} />
            </div>
          </li>
        {/each}
      </ol>
    </div>
    {#each COLUMNS as col (col.id)}
      {@const list = person.filter((i) => i.column === col.id)}
      {@const shown = expanded[col.id] ? list : list.slice(0, SHOWN)}
      <div class="col" data-column={col.id}>
        <div class="ch"><h3>{col.title}</h3><span>{col.hint(list)}</span></div>
        {#if !list.length}
          <p class="none">Nothing to {col.id === "approve" ? "approve" : col.id === "spec" ? "plan" : "do"}.</p>
        {/if}
        <ol>
          {#each shown as i (i.key)}
            <li class="item" class:hot={(i.rank ?? 99) <= 3} class:moved={marks.has(i.key) || (i.stories ?? []).some((k) => marks.has(k))} data-rank={i.rank} data-key={i.key}>
              <span class="rk" aria-label="Rank {i.rank}">{i.rank}</span>
              <div class="body">
                <a class="name" href={href(i)} title={i.kind === "story" ? `${i.key}: ${i.title}` : plainTask(i.title)}>{i.kind === "task" ? plainTask(i.title) : i.label}</a>
                <span class="note">{note(i)}</span>
                <span class="slack">
                  <span class="sbar" aria-hidden="true"><i class="u-{i.urgency}" style:width="{slackWidth(i.slack)}%"></i></span>
                  <span class="sl u-{i.urgency}-text">{slackText(i)}</span>
                  {#if i.critical}<span class="crit">critical</span>{/if}
                  {#if marks.has(i.key)}<span class="upd">updated</span>{/if}
                </span>
                {#if errors[i.key]}<span class="err" role="alert">{errors[i.key]}</span>{/if}
              </div>
              {#if i.action.kind === "approve"}
                <button type="button" class="btn go" disabled={busy === i.key} onclick={() => approve(i)}>{busy === i.key ? "Approving…" : "Approve"}</button>
              {:else}
                <a class="btn" class:ink={i.action.kind === "task"} href={href(i)}>{actionLabel(i)}</a>
              {/if}
            </li>
          {/each}
        </ol>
        {#if list.length > SHOWN}
          <button type="button" class="more" aria-expanded={!!expanded[col.id]} onclick={() => (expanded = { ...expanded, [col.id]: !expanded[col.id] })}>
            {expanded[col.id] ? "Show the top 5" : `Show all ${list.length}`}
          </button>
        {/if}
      </div>
    {/each}
  </div>
  {#if quiet.length}
    <p class="quiet" data-waits-on-work>{quiet.length} more {quiet.length === 1 ? "story waits" : "stories wait"} on work only: approved, with an unfinished blocker.</p>
  {/if}
</section>

<style>
  .head { display: flex; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: 6px 24px; margin-bottom: 10px; }
  h2 { font-size: 15px; font-weight: 700; }
  h2 span { margin-left: 8px; font-size: 12px; font-weight: 600; color: var(--mute); }
  .legend { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 16px; font-size: 11.5px; color: var(--mute); }
  .legend > span:first-child { max-width: 62ch; }
  .key i { display: inline-block; width: 18px; height: 6px; margin-right: 6px; border-radius: 3px; vertical-align: 1px; }

  .cols { display: grid; grid-template-columns: 1.4fr 1.1fr 1fr 0.9fr; gap: 14px; align-items: start; }
  .col { min-width: 0; padding: 10px 14px 6px; border: 1px solid var(--line); border-radius: var(--r-well); background: var(--panel); }
  .ch { display: flex; justify-content: space-between; align-items: baseline; gap: 8px; padding-bottom: 8px; border-bottom: 1px solid var(--line); }
  .ch h3 { font-size: 14px; font-weight: 700; }
  .ch span { font-size: 11.5px; font-weight: 600; color: var(--mute); white-space: nowrap; }
  .none { padding: 12px 0; color: var(--mute); font-size: 12.5px; }
  ol { list-style: none; }
  .item { display: grid; grid-template-columns: 26px minmax(0, 1fr) auto; gap: 10px; align-items: center; padding: 8px 0; border-bottom: 1px solid var(--hair); }
  .item:last-child { border-bottom: 0; }
  .item.q { grid-template-columns: 26px minmax(0, 1fr); align-items: start; }
  .item.q .body { gap: 4px; }
  .rk { width: 26px; height: 26px; display: grid; place-items: center; border: 1.5px solid var(--line); border-radius: 50%; color: var(--mute); font-size: 12px; font-weight: 700; font-variant-numeric: tabular-nums; }
  .item.hot .rk { border-color: var(--ink); background: var(--ink); color: #fff; }
  .body { min-width: 0; display: flex; flex-direction: column; gap: 1px; }
  /* Change names are long and hyphenated: break at the hyphens, at most two lines. */
  .name {
    font-size: 12.5px; font-weight: 700; color: var(--ink); text-decoration: none; overflow-wrap: anywhere;
    display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
  }
  .name:hover { text-decoration: underline; text-underline-offset: 3px; }
  .note { font-size: 11.5px; color: var(--mute); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .slack { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 8px; margin-top: 4px; }
  .sbar { position: relative; flex: none; width: 96px; height: 8px; border-radius: 4px; background: var(--strip); overflow: hidden; }
  .sbar i { position: absolute; left: 0; top: 0; bottom: 0; border-radius: 4px; }
  .u-now { background: var(--alert); }
  .u-soon { background: var(--amber); }
  .u-later { background: var(--done); }
  .sl { font-size: 11px; font-weight: 700; white-space: nowrap; }
  .u-now-text { color: #8f241d; }
  .u-soon-text { color: var(--amber-ink); }
  .u-later-text { color: var(--done); }
  .crit, .upd { flex: none; width: max-content; padding: 0 5px; border-radius: var(--r-tag); font-size: 10.5px; font-weight: 700; line-height: 16px; }
  .crit { border: 1px solid #e9b8b3; background: #fbeceb; color: #8f241d; }
  .upd { border: 1px solid var(--amber); background: var(--amber-tint); color: var(--amber-ink); }
  .err { font-size: 11.5px; color: #8f241d; }
  .btn { flex: none; height: 28px; padding: 0 12px; display: inline-flex; align-items: center; justify-content: center; border: 1px solid var(--line); border-radius: 7px; background: #fff; color: var(--ink); font-size: 12px; font-weight: 700; white-space: nowrap; text-decoration: none; }
  .btn:hover { background: var(--strip); }
  .btn.go { border-color: var(--done); background: var(--done); color: #fff; }
  .btn.go:hover { background: #255a3f; }
  .btn.ink { border-color: var(--ink); background: var(--ink); color: #fff; }
  .btn.ink:hover { background: #000; }
  .btn:disabled { opacity: 0.6; cursor: progress; }
  .more { margin: 6px 0 4px; padding: 4px 0; font-size: 12px; font-weight: 600; color: var(--mute); text-decoration: underline; text-underline-offset: 3px; }
  .more:hover { color: var(--ink); }
  .quiet { margin-top: 10px; font-size: 12px; color: var(--mute); }
  .item.moved { animation: moved 1.4s ease-out 3; border-radius: 4px; }
  @keyframes moved { from { box-shadow: 0 0 0 0 rgba(201, 138, 26, 0.5); } to { box-shadow: 0 0 0 8px rgba(201, 138, 26, 0); } }

  @media (max-width: 1100px) { .cols { grid-template-columns: 1fr 1fr; } }
  @media (max-width: 760px) { .cols { grid-template-columns: 1fr; } }
</style>
