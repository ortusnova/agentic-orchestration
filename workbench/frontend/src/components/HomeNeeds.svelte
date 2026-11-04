<script lang="ts">
  import { can } from "../lib/identity.svelte";
  import { graphHref } from "../lib/router";
  import { eventText, plainTask, REASON_TEXT, slackText, slackWidth, type Column, type ExpiredHold, type HomeFault, type HomeNotification, type HomeQuestion, type NotReadyItem } from "../lib/home";
  import FaultBox from "./FaultBox.svelte";
  import QuestionBox from "./QuestionBox.svelte";

  let {
    items, questions = [], faults = [], expiredHolds = [], notifications = [], slug, marks, onapprove, onanswer, onreplace, onfaultdone, onseen,
  }: {
    items: NotReadyItem[];
    /** The open questions of parked stories, ranked first. */
    questions?: HomeQuestion[];
    /** The platform faults only a person can clear, ranked with the questions. */
    faults?: HomeFault[];
    /** The stories whose supervisor lease ran out, ranked after the questions. */
    expiredHolds?: ExpiredHold[];
    /** What the Sorties' notifications sent: unread ones first. */
    notifications?: HomeNotification[];
    /** A person saw these notifications; resolves to an error message, or null. */
    onseen?: (ids: number[]) => Promise<string | null>;
    slug: string;
    marks: Set<string>;
    /** Approve stories; resolves to an error message, or null. */
    onapprove: (keys: string[]) => Promise<string | null>;
    /** Answer a parked story's question; resolves to an error message, or null. */
    onanswer: (key: string, text: string) => Promise<string | null>;
    /** Confirm a replacement's supersede; resolves to an error message, or null. */
    onreplace?: (replacement: string) => Promise<string | null>;
    /** Mark a platform fault's action done, so Sortie checks it again at once; resolves to an error message, or null. */
    onfaultdone?: (id: string) => Promise<string | null>;
  } = $props();

  // The questions and the platform faults, in one ranked list.
  const asks = $derived(
    [...questions.map((q) => ({ kind: "question" as const, rank: q.rank, id: q.key, q })),
      ...faults.map((f) => ({ kind: "fault" as const, rank: f.rank, id: f.id, f }))].sort((a, b) => a.rank - b.rank),
  );

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

  const unread = $derived(notifications.filter((n) => n.unread));
  let seeing = $state(false);
  let seenError = $state("");
  async function see(ids: number[]) {
    if (!onseen || !ids.length) return;
    seeing = true;
    seenError = (await onseen(ids)) ?? "";
    seeing = false;
  }
  const when = (t: number) => new Date(t * 1000).toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
  /** What the notification says: the agent's words first, such as its question. */
  const said = (n: HomeNotification) => (n.agent_text || n.body || n.title).trim();

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
    <h2 id="needs-h">Needs a person<span>by what you do · numbers rank all {questions.length + faults.length + expiredHolds.length + person.length} by urgency</span></h2>
    <p class="legend">
      <span>The bar is the time before an item holds agents up, in stories until estimates exist. Shorter is more urgent.</span>
      <span class="key"><i class="u-now"></i>blocking now</span>
      <span class="key"><i class="u-soon"></i>within 4 stories</span>
      <span class="key"><i class="u-later"></i>room to spare</span>
    </p>
  </div>
  {#if notifications.length}
    <div class="inbox" data-notifications role="region" aria-labelledby="inbox-h">
      <div class="ch">
        <h3 id="inbox-h">From Sortie</h3>
        <span>{unread.length} unread · {notifications.length} shown</span>
        {#if onseen && unread.length > 1}
          <button type="button" class="more" disabled={seeing} onclick={() => see(unread.map((n) => n.id))}>Mark all seen</button>
        {/if}
      </div>
      {#if seenError}<p class="err" role="alert">{seenError}</p>{/if}
      <ol>
        {#each notifications as n (n.id)}
          <li class="note-item" class:unread={n.unread} data-notification={n.id} data-event={n.event_type} data-unread={n.unread}>
            <span class="dot" aria-label={n.unread ? "Unread" : "Seen"}></span>
            <div class="body">
              <span class="line">
                <a class="name" href={graphHref(slug, { epic: n.story.change, selected: n.story.key })} title="{n.story.key}: {n.story.title}">{n.story.label}</a>
                <span class="what">{n.identifier} {eventText(n)}{n.host ? ` on ${n.host}'s Sortie` : ""}</span>
                {#if n.again}<span class="upd">still open after 4 hours</span>{:else if n.unread}<span class="upd">unread</span>{/if}
              </span>
              <span class="said">{said(n)}</span>
              <span class="note">
                {when(n.sent_at)}{#if n.question} · answer it under Answer{/if}{#if n.escalation?.url} · <a href={n.escalation.url} target="_blank" rel="noreferrer">open the session</a>{/if}{#if !n.unread && n.seen_by} · seen by {n.seen_by}{/if}
              </span>
            </div>
            {#if n.unread && onseen}
              <button type="button" class="btn" disabled={seeing} onclick={() => see([n.id])}>Seen</button>
            {/if}
          </li>
        {/each}
      </ol>
    </div>
  {/if}
  <div class="cols">
    <div class="col" data-column="answer">
      <div class="ch"><h3>Answer</h3><span>{asks.length} · {asks.length === 1 ? "an agent waits" : "agents wait"}</span></div>
      {#if !asks.length && !expiredHolds.length}
        <p class="none">No agent waits on an answer.</p>
      {/if}
      <ol>
        {#each asks as a (a.kind + a.id)}
          {#if a.kind === "question"}
            {@const q = a.q}
            <li class="item q hot" class:moved={marks.has(q.key)} data-rank={q.rank} data-key={q.key}>
              <span class="rk" aria-label="Rank {q.rank}">{q.rank}</span>
              <div class="body">
                <a class="name" href={graphHref(slug, { epic: q.change, selected: q.key })} title="{q.key}: {q.title}">{q.label}</a>
                <span class="note">{[q.identifier, q.title].join(" · ")}</span>
                <QuestionBox question={q} story={q.key} canAnswer={can("approver")} onanswer={(text) => onanswer(q.key, text)}
                  replacedBy={q.replaced_by ?? null} onreplace={onreplace && q.replaced_by ? () => onreplace(q.replaced_by!.key) : undefined} />
              </div>
            </li>
          {:else}
            {@const f = a.f}
            <li class="item q hot" class:moved={f.keys.some((k) => marks.has(k))} data-rank={f.rank} data-key={f.key} data-platform-fault={f.id}>
              <span class="rk" aria-label="Rank {f.rank}">{f.rank}</span>
              <div class="body">
                <span class="name">Platform fault</span>
                <span class="note">{f.stories.length} {f.stories.length === 1 ? "story" : "stories"} held · {f.sortie}</span>
                <FaultBox fault={f} {slug} canAct={can("approver")} ondone={() => (onfaultdone ? onfaultdone(f.id) : Promise.resolve("This page cannot mark a fault done."))} />
              </div>
            </li>
          {/if}
        {/each}
        {#each expiredHolds as h (h.key)}
          <li class="item q hot" class:moved={marks.has(h.key)} data-rank={h.rank} data-key={h.key} data-expired-hold={h.identifier}>
            <span class="rk" aria-label="Rank {h.rank}">{h.rank}</span>
            <div class="body">
              <a class="name" href={graphHref(slug, { epic: h.change, selected: h.key })} title="{h.key}: {h.title}">{h.label}</a>
              <span class="note">{[h.identifier, h.title].join(" · ")}</span>
              <p class="expired" data-expired-text>{h.text.charAt(0).toUpperCase() + h.text.slice(1)}. Release the hold in Sortie, or ask the supervisor to renew it.</p>
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
  .expired { margin-top: 4px; font-size: 12.5px; color: #6f1c16; overflow-wrap: anywhere; }
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
  .inbox { margin-bottom: 14px; padding: 10px 14px 6px; border: 1px solid var(--line); border-radius: var(--r-well); background: var(--panel); }
  .inbox .ch { justify-content: flex-start; }
  .inbox .ch .more { margin: 0 0 0 auto; padding: 0; }
  .note-item { display: grid; grid-template-columns: 12px minmax(0, 1fr) auto; gap: 10px; align-items: start; padding: 8px 0; border-bottom: 1px solid var(--hair); }
  .note-item:last-child { border-bottom: 0; }
  .dot { width: 8px; height: 8px; margin-top: 5px; border-radius: 50%; border: 1.5px solid var(--line); }
  .note-item.unread .dot { border-color: var(--alert); background: var(--alert); }
  .line { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 8px; }
  .what { font-size: 12px; color: var(--ink); }
  .note-item:not(.unread) .what, .note-item:not(.unread) .said { color: var(--mute); }
  .said { font-size: 12.5px; color: var(--ink); white-space: pre-line; overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 3; line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
  .item.moved { animation: moved 1.4s ease-out 3; border-radius: 4px; }
  @keyframes moved { from { box-shadow: 0 0 0 0 rgba(201, 138, 26, 0.5); } to { box-shadow: 0 0 0 8px rgba(201, 138, 26, 0); } }

  @media (max-width: 1100px) { .cols { grid-template-columns: 1fr 1fr; } }
  @media (max-width: 760px) { .cols { grid-template-columns: 1fr; } }
</style>
