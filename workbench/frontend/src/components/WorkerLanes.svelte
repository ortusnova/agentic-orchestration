<script lang="ts">
  import { onDestroy } from "svelte";
  import {
    ago, duration, groupLater, hostLine, jobBox, laneState, nextBox, pct, retryBox, stuckAction, TICKS, tokens, VERDICT_WORD,
    type Lane, type LaneJob, type Worker,
  } from "../lib/lanes";
  import { can } from "../lib/identity.svelte";
  import type { RunClient } from "../lib/run";
  import { graphHref } from "../lib/router";
  import type { Question } from "../lib/types";
  import GrantBox from "./GrantBox.svelte";
  import QuestionBox from "./QuestionBox.svelte";

  /** The workers' agent slots in time, half an hour back to an hour ahead:
   * one section per worker, one lane per slot (agent-platform/run-supervision).
   * Every stuck lane has the action that resolves it, in place: Answer opens
   * the question and its box under the lane; a held session budget offers
   * more sessions; Open opens the story's session, or its last run. */
  let { workers, slug, now: fixed, onanswer, runs }: {
    workers: Worker[];
    slug: string;
    now?: number;
    /** Answer a parked story's question; resolves to an error message, or null. */
    onanswer?: (key: string, text: string) => Promise<string | null>;
    /** Grants a story held at its session budget more sessions. */
    runs?: RunClient;
  } = $props();

  // The stuck work whose action is open under its lane: "<worker>/<identifier>".
  let opened: string | null = $state(null);
  const toggle = (w: Worker, id: string) => (opened = opened === `${w.name}/${id}` ? null : `${w.name}/${id}`);
  const isOpen = (w: Worker, id: string) => opened === `${w.name}/${id}`;
  const approver = $derived(can("approver"));

  let tick = $state(Date.now() / 1000);
  const timer = setInterval(() => (tick = Date.now() / 1000), 15_000);
  onDestroy(() => clearInterval(timer));
  const now = $derived(fixed ?? tick);

  const at = pct(0);
  const storyHref = (key: string) => graphHref(slug, { epic: key.split("/")[0], selected: key });
  // The story with its run open in the chat pane: its live session, or its last run.
  const runHref = (key: string) => graphHref(slug, { epic: key.split("/")[0], selected: key, open: "run" });
  const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
  const answerOf = (key: string | null) => (text: string) =>
    !key ? Promise.resolve("This story is not on the plan, so it cannot be answered here.") : onanswer ? onanswer(key, text) : Promise.resolve("Answering is not available here.");

  /** Every fact of a job, for its hover card. */
  function facts(lane: Lane, j: LaneJob): string[] {
    const out: string[] = [];
    if (j.started_at != null) out.push(`${duration(now - j.started_at)} running` + (j.turns != null ? ` · ${plural(j.turns, "turn", "turns")}` : "") + (j.tokens != null ? ` · ${tokens(j.tokens)} tokens` : " · tokens not reported"));
    if (lane.kind === "job") {
      const b = jobBox(j, now);
      out.push(b.overdue ? `overdue: past the ${j.estimate!.basis}` : j.estimate ? `about ${b.left_min} min left · ${j.estimate.basis}` : "no estimate: too few past runs to say");
    }
    if (lane.kind === "retry" && j.due_at != null) out.push(j.due_at > now ? `runs again in ${duration(j.due_at - now)}` : "runs again now");
    if (lane.stuck) out.push(lane.stuck.text);
    if (j.since != null) out.push(`waits for ${duration(now - j.since)}`);
    if (j.status && lane.kind !== "job") out.push(j.status);
    if (j.last_event) out.push(`${j.last_event}${j.last_event_at != null ? ` · ${ago(now - j.last_event_at)} ago` : ""}`);
    return out;
  }

  function opens(j: LaneJob): string {
    if (!j.key) return "Not on this plan";
    return j.watchable ? "Click to open its session: watch it, or send its agent a message" : "Click to open its story and its run";
  }
</script>

{#snippet action(w: Worker, id: string, key: string | null, code: string | undefined, q: Question | null | undefined, budget: LaneJob["budget"])}
  <div class="opened" data-opened={id} role="region" aria-label="{id}: {code === 'question' ? 'its question' : 'its sessions'}">
    <div class="oh">
      <b><span class="id">{id}</span> {code === "question" ? "asks" : "is held at its session budget"}</b>
      <span>on {w.name}</span>
      {#if key}<a href={runHref(key)}>{code === "question" ? "Open its story" : "Open its last run"}</a>{/if}
      <button type="button" class="x" aria-label="Close" onclick={() => (opened = null)}>Close</button>
    </div>
    {#if code === "question" && q}
      <QuestionBox question={q} story={key ?? id} canAnswer={approver && !!key} onanswer={answerOf(key)} />
    {:else if code === "budget" && runs}
      <GrantBox identifier={id} sortie={w.name} used={budget?.used} sessions={budget?.sessions} canGrant={w.can_grant} maxSessions={w.max_sessions}
        canAct={approver} ongrant={(n) => runs.grant(id, n)} />
    {/if}
  </div>
{/snippet}

<section class="lanes" aria-labelledby="lanes-h">
  <h2 id="lanes-h" class="sr-only">Workers</h2>
  {#if !workers.length}
    <p class="empty">Reading the workers…</p>
  {:else}
    <div class="axis" aria-hidden="true">
      {#each TICKS as [m, label] (m)}
        <span class="tick" class:n={m === 0} class:first={m === TICKS[0][0]} class:end={m === TICKS[TICKS.length - 1][0]} style:left="{pct(m)}%">{label}</span>
      {/each}
    </div>

    {#each workers as w (w.name)}
      <section class="worker" data-worker={w.name} data-verdict={w.verdict ?? "elsewhere"} aria-label="Worker {w.name}">
        <div class="wh">
          <h3>{w.name}</h3>
          {#if !w.dispatched}
            <span class="host">runs agents for another project</span>
          {:else if w.reachable}
            <span class="busy" data-busy><b>{w.busy}</b> of {w.slots ?? "?"} slots busy</span>
            <span class="verdict {w.verdict}">{VERDICT_WORD[w.verdict ?? "idle"]}</span>
            <span class="host">{hostLine(w, now)}</span>
          {:else}
            <span class="verdict unreachable">unreachable</span>
          {/if}
        </div>

        {#if !w.dispatched}
          <p class="notice">{w.name}'s Sortie runs agents for its own tracker project, not this one, so its slots are not shown here.</p>
        {:else if !w.reachable}
          <p class="notice alert" role="status">{w.reasons?.[0]?.text ?? `${w.name}'s Sortie is unreachable`}. Its slots show again when it answers.</p>
        {:else}
          {#if w.paused}<p class="notice">{w.paused}: no new run starts on {w.name}.</p>{/if}
          <div class="track">
            <div class="nowline" style:left="calc(var(--label) + (100% - var(--label)) * {at / 100})" aria-hidden="true"></div>
            {#each w.lanes ?? [] as lane (lane.slot)}
              {@const j = lane.job}
              <div class="lane" data-lane={lane.kind} data-stuck={lane.stuck?.code}>
                <div class="ll">
                  <div class="no">Slot {lane.slot}</div>
                  <div class="st" class:working={lane.kind === "job" && !lane.stuck} class:stalled={!!lane.stuck} class:free={lane.kind === "free"}>{laneState(lane)}</div>
                  {#if j}
                    {#if j.key}<a class="jt" href={storyHref(j.key)}><span class="id">{j.identifier}</span> <b>{j.title}</b></a>
                    {:else}<span class="jt"><span class="id">{j.identifier}</span> <b>{j.title}</b></span>{/if}
                    {#if lane.kind === "job"}
                      {@const b = jobBox(j, now)}
                      <div class="jm"><b>{j.started_at != null ? duration(now - j.started_at) : "—"}</b>{j.tokens != null ? ` · ${tokens(j.tokens)} tok` : ""}{" · "}{#if b.overdue}<b class="alarm">overdue</b>{:else if b.left_min != null}<b>~{b.left_min} min</b> left{:else}no estimate{/if}</div>
                    {:else if lane.stuck}
                      <p class="alarm" title={lane.stuck.text}>{lane.stuck.text}</p>
                    {/if}
                  {:else if lane.reason}
                    <p>{lane.reason}</p>
                  {/if}
                </div>
                <div class="lt">
                  {#if j && lane.kind === "job"}
                    {@const b = jobBox(j, now)}
                    <svelte:element this={j.key ? "a" : "div"} href={j.key ? runHref(j.key) : undefined} tabindex="0" role={j.key ? undefined : "group"}
                      class="job" class:stalled={!!lane.stuck || b.overdue} class:cut={b.cut}
                      style:left="{b.left}%" style:width="calc({b.width}% - 2px)" style:--split="{b.split}%"
                      aria-label="{j.identifier} {j.title}: {facts(lane, j).join('; ')}">
                      <span class="h"><span class="id">{j.identifier}</span><b>{j.title}</b></span>
                      <span class="f"><span><b>{j.started_at != null ? duration(now - j.started_at) : "—"}</b></span><span>{#if b.overdue}<b class="alarm">overdue</b>{:else if b.left_min != null}<b>~{b.left_min} min</b> left{:else}no estimate{/if}</span></span>
                      <span class="tip" role="tooltip"><b>{j.identifier} {j.title}</b><span>{j.phase}{j.repository ? ` · ${j.repository}` : ""} · on {w.name}</span>{#each facts(lane, j) as f (f)}<span class:alarm={f === lane.stuck?.text}>{f}</span>{/each}<span class="mute">{opens(j)}</span></span>
                    </svelte:element>
                  {:else if j && lane.kind === "retry"}
                    {@const b = retryBox(j, now)}
                    <svelte:element this={j.key ? "a" : "div"} href={j.key ? runHref(j.key) : undefined} tabindex="0" role={j.key ? undefined : "group"}
                      class="job starting" style:left="{b.left}%" style:width="calc({b.width}% - 2px)"
                      aria-label="{j.identifier} {j.title}: {facts(lane, j).join('; ')}">
                      <span class="h"><span class="id">{j.identifier}</span><b>{j.title}</b></span>
                      <span class="f"><span>{b.left_min ? `retries in ${b.left_min} min` : "retries now"}</span></span>
                      <span class="tip" role="tooltip"><b>{j.identifier} {j.title}</b><span>{j.phase} · on {w.name}</span>{#each facts(lane, j) as f (f)}<span class:alarm={f === lane.stuck?.text}>{f}</span>{/each}<span class="mute">{opens(j)}</span></span>
                    </svelte:element>
                  {:else if j && lane.kind === "stuck"}
                    <div class="fault">
                      <b>Needs you:</b> {stuckAction(lane.stuck?.code)}
                      {#if j.since != null}<span>waits for {duration(now - j.since)}</span>{/if}
                      <span class="acts">
                        {#if lane.stuck?.code === "question"}
                          <button type="button" class="ghost ink" data-act="answer" aria-expanded={isOpen(w, j.identifier)} onclick={() => toggle(w, j.identifier)}>Answer</button>
                        {:else if lane.stuck?.code === "budget" && j.budget?.reason === "session_budget"}
                          <button type="button" class="ghost ink" data-act="grant" aria-expanded={isOpen(w, j.identifier)} onclick={() => toggle(w, j.identifier)}>Give more sessions</button>
                        {/if}
                        {#if j.key}<a class="ghost" data-act="open" href={runHref(j.key)}>Open</a>{/if}
                      </span>
                    </div>
                  {:else}
                    {#each lane.next as n (n.identifier)}
                      {@const b = nextBox(n)}
                      <svelte:element this={n.key ? "a" : "div"} href={n.key ? storyHref(n.key) : undefined} tabindex="0" role={n.key ? undefined : "group"}
                        class="job next" class:open={!b.known} style:left="{b.left}%" style:width="calc({b.width}% - 2px)" aria-label="next: {n.identifier} {n.title}, {n.phase}">
                        <span class="h"><span class="id">{n.identifier}</span><b>{n.title}</b></span>
                        <span class="f">next · {n.phase.toLowerCase()}{b.known ? ` · ~${b.left_min} min` : ""}</span>
                        <span class="tip" role="tooltip"><b>{n.identifier} {n.title}</b><span>moves to {n.phase.toLowerCase()} at the next poll</span><span>{n.estimate ? `takes about ${duration(n.estimate.seconds)} · ${n.estimate.basis}` : "no estimate: too few past runs to say"}</span></span>
                      </svelte:element>
                    {/each}
                  {/if}
                </div>
              </div>
              {#if j && lane.kind === "stuck" && isOpen(w, j.identifier)}
                {@render action(w, j.identifier, j.key, lane.stuck?.code, j.question, j.budget)}
              {/if}
            {/each}
          </div>
          {#if w.later?.length}
            <div class="later">
              <h4>Then on {w.name}</h4>
              <ul>
                {#each groupLater(w.later) as i (i.kind + i.identifiers.join(","))}
                  <li class={i.kind} class:alarm={!!i.stuck} data-later={i.kind}>
                    <i aria-hidden="true"></i>
                    {#if i.identifiers.length === 1 && i.key}<a href={storyHref(i.key)}><b>{i.identifier}</b></a>{:else}<b>{i.identifiers.join(", ")}</b>{/if}
                    {#if i.pull?.url}<small><a href={i.pull.url} target="_blank" rel="noopener">{i.text}</a>{i.stuck && i.since != null ? `, for ${duration(now - i.since)}` : ""}</small>
                    {:else}<small title={i.status ?? undefined}>{i.text}</small>{/if}
                    {#if i.code === "question" || (i.code === "budget" && i.budget?.reason === "session_budget")}
                      <button type="button" class="mini" data-act={i.code === "question" ? "answer" : "grant"} aria-expanded={isOpen(w, i.identifier)}
                        onclick={() => toggle(w, i.identifier)}>{i.code === "question" ? "Answer" : "Give more sessions"}</button>
                    {/if}
                  </li>
                {/each}
              </ul>
            </div>
            {#each (w.later ?? []).filter((i) => i.code && isOpen(w, i.identifier)) as i (i.identifier)}
              {@render action(w, i.identifier, i.key, i.code, i.question, i.budget)}
            {/each}
          {/if}
        {/if}
      </section>
    {/each}

    <div class="legend">
      <span><i class="sw ran" aria-hidden="true"></i>run so far</span>
      <span><i class="sw rest" aria-hidden="true"></i>estimated rest, from the median of past runs</span>
      <span><i class="sw nx" aria-hidden="true"></i>next on this slot</span>
      <span><i class="sw st" aria-hidden="true"></i>stuck or overdue</span>
      <span>Click a job to open its session.</span>
    </div>
  {/if}
</section>

<style>
  .lanes { --label: 250px; position: relative; padding: 10px var(--home-x, 40px) 6px; }
  .sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
  .empty { padding: 18px 0; color: var(--mute); font: italic 400 15px var(--font-serif); }

  .axis { position: sticky; top: 0; z-index: 4; height: 40px; margin-left: var(--label); background: var(--paper); border-bottom: 1px solid var(--ink-rule); }
  .tick { position: absolute; bottom: 5px; transform: translateX(-50%); white-space: nowrap; font: italic 400 15px var(--font-serif); color: var(--mute); }
  .tick.first { transform: none; }
  .tick.end { transform: translateX(-100%); }
  .tick.n { bottom: 8px; padding: 2px 8px; border-radius: var(--r-tag); background: var(--ink-rule); color: #fff; font: 700 10px var(--font-sans); letter-spacing: 0.05em; text-transform: uppercase; }

  .worker { margin-top: 18px; }
  .wh { display: flex; align-items: baseline; gap: 14px; flex-wrap: wrap; padding: 6px 0 8px; border-bottom: 1px solid var(--line); }
  h3 { width: calc(var(--label) - 14px); font: 700 24px/1.1 var(--font-sans); letter-spacing: -0.02em; }
  .busy { font-size: 15px; font-variant-numeric: tabular-nums; }
  .busy b { font-size: 20px; }
  .host { color: var(--mute); font: 500 11px var(--font-mono); }
  .verdict { display: inline-flex; align-items: center; gap: 6px; font-size: 10px; font-weight: 700; letter-spacing: 0.05em; text-transform: uppercase; color: var(--slate-ink); }
  .verdict::before { content: ""; width: 6px; height: 6px; border-radius: 50%; background: var(--slate); }
  .verdict.working { color: var(--amber-ink); }
  .verdict.working::before { background: var(--amber); }
  .verdict.stuck, .verdict.unreachable { color: var(--alert); }
  .verdict.stuck::before, .verdict.unreachable::before { background: var(--alert); }

  .notice { margin: 8px 0 4px var(--label); padding: 7px 12px; border-radius: var(--r-card); background: var(--amber-tint); color: var(--amber-ink); }
  .notice.alert { background: #fbeceb; color: #8f241d; }

  .track { position: relative; }
  .nowline { position: absolute; top: 0; bottom: 0; z-index: 2; width: 1.5px; background: var(--ink-rule); pointer-events: none; }
  .lane { display: grid; grid-template-columns: var(--label) 1fr; min-height: 96px; border-bottom: 1px solid var(--hair); }
  .ll { min-width: 0; padding: 12px 14px 10px 0; }
  .no { font-size: 11px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); }
  .st { margin-top: 2px; font-weight: 600; }
  .st.working { color: var(--amber-ink); }
  .st.stalled { color: var(--alert); }
  .st.free { color: var(--slate-ink); font: italic 400 17px var(--font-serif); }
  .ll p { margin-top: 2px; color: var(--mute); }
  .ll p.alarm { display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; overflow-wrap: anywhere; color: var(--alert); font-weight: 600; }
  .jt { display: block; margin-top: 4px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 14px; color: var(--ink); text-decoration: none; }
  .jt b { font-weight: 600; }
  a.jt:hover b { text-decoration: underline; text-underline-offset: 3px; }
  .jm { color: var(--mute); font-variant-numeric: tabular-nums; }
  .jm b { color: var(--ink); }
  .alarm, .jm b.alarm { color: var(--alert); }
  .id { font: 500 11px var(--font-mono); color: var(--mute); }

  .lt { position: relative; min-height: 96px; }
  .job {
    position: absolute; top: 18px; z-index: 3; min-width: 6px; height: 60px; padding: 7px 10px;
    display: flex; flex-direction: column; justify-content: space-between;
    border: 1.5px solid var(--amber); border-radius: var(--r-card); color: var(--ink); text-align: left; text-decoration: none;
    background: linear-gradient(to right, var(--amber-tint) 0 var(--split, 100%), transparent var(--split, 100%)),
      repeating-linear-gradient(135deg, #fff 0 6px, rgba(201, 138, 26, 0.18) 6px 7px);
    transition: box-shadow 0.2s cubic-bezier(0.2, 0.8, 0.2, 1);
  }
  a.job:hover { box-shadow: var(--shadow-lift); }
  .job:hover, .job:focus-visible { z-index: 6; }
  .job.cut { border-left-style: dashed; border-top-left-radius: 0; border-bottom-left-radius: 0; }
  .job.stalled { border-color: var(--alert); }
  .job.starting { border-style: dashed; background: #fff; }
  .job.next { border: 1.5px dashed var(--slate); background: #fff; color: var(--mute); }
  .job.next.open { border-right-color: transparent; border-top-right-radius: 0; border-bottom-right-radius: 0; }
  .job .h, .job .f { max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .job .h { display: flex; gap: 6px; align-items: baseline; min-width: 0; }
  .job .h b { overflow: hidden; text-overflow: ellipsis; font-weight: 600; }
  .job .f { display: flex; gap: 12px; color: var(--mute); font-variant-numeric: tabular-nums; }
  .job .f b { color: var(--ink); }
  .job .f b.alarm { color: var(--alert); }

  .tip {
    display: none; position: absolute; left: 0; bottom: calc(100% + 6px); z-index: 7; min-width: 280px; max-width: 380px;
    flex-direction: column; gap: 3px; padding: 10px 12px; border-radius: var(--r-button);
    background: var(--ink); color: #fff; white-space: normal; font-weight: 400; pointer-events: none;
    box-shadow: 0 10px 24px -12px rgba(20, 22, 26, 0.5);
  }
  .tip b { font-weight: 600; }
  .tip span { color: #d6d6da; overflow-wrap: anywhere; }
  .tip .mute { color: var(--ink-mute); }
  .tip .alarm { color: #f08a80; }
  .job:hover .tip, .job:focus-visible .tip { display: flex; }

  .fault { position: absolute; top: 18px; left: 0; right: 0; z-index: 3; height: 60px; display: flex; align-items: center; gap: 14px; padding: 0 14px; border: 1.5px solid var(--alert); border-radius: var(--r-card); background: #fff; }
  .fault span { color: var(--mute); }
  .acts { margin-left: auto; display: inline-flex; gap: 6px; }
  .ghost.ink { background: var(--ink); border-color: var(--ink); color: #fff; }
  .ghost.ink:hover { background: #000; }
  .ghost[aria-expanded="true"] { box-shadow: 0 0 0 2px var(--amber-tint), 0 0 0 3px var(--amber); }
  .opened { display: flex; flex-direction: column; gap: 10px; margin: 0 0 14px var(--label); padding: 14px 16px; border: 1.5px solid var(--alert); border-top: 0; border-radius: 0 0 var(--r-card) var(--r-card); background: #fff; }
  .later + .opened { margin-top: 6px; border-top: 1.5px solid var(--alert); border-radius: var(--r-card); }
  .oh { display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px 14px; }
  .oh b { font-size: 14px; font-weight: 600; }
  .oh span, .oh a { color: var(--mute); }
  .oh a { color: var(--ink); text-underline-offset: 3px; }
  .oh .x { margin-left: auto; height: 28px; padding: 0 10px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; font-size: 12px; font-weight: 600; }
  .oh .x:hover { border-color: var(--ink); }
  .opened :global(.statement) { max-width: 78ch; font-size: 13px; }
  .mini { height: 24px; padding: 0 9px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; color: var(--ink); font-size: 12px; font-weight: 600; }
  .mini:hover, .mini[aria-expanded="true"] { border-color: var(--ink); }
  .ghost { height: 32px; display: inline-flex; align-items: center; padding: 0 14px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; font-weight: 600; color: var(--ink); text-decoration: none; }
  .ghost:hover { background: var(--strip); }

  .later { display: grid; grid-template-columns: var(--label) 1fr; padding: 8px 0 4px; }
  h4 { padding-top: 3px; font-size: 11px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); }
  .later ul { display: flex; flex-wrap: wrap; gap: 6px 18px; list-style: none; }
  .later li { display: flex; gap: 6px; align-items: baseline; }
  .later li a { color: inherit; text-decoration: none; }
  .later li a:hover { text-decoration: underline; text-underline-offset: 3px; }
  .later li i { flex: none; width: 7px; height: 7px; border-radius: 50%; background: var(--slate); }
  .later li small { color: var(--mute); }
  .later li.ready { color: var(--done); }
  .later li.ready i { background: var(--done); }
  .later li.you { color: var(--amber-ink); }
  .later li.you i { background: var(--amber); }
  .later li.held { color: var(--alert); }
  .later li.held i { background: none; border: 1.5px solid var(--alert); }
  .later li.alarm small { color: var(--alert); }

  .legend { display: flex; flex-wrap: wrap; gap: 22px; margin: 18px 0 0 var(--label); color: var(--mute); }
  .sw { display: inline-block; width: 22px; height: 10px; margin-right: 6px; border-radius: var(--r-swatch); vertical-align: -1px; }
  .sw.ran { border: 1.5px solid var(--amber); background: var(--amber-tint); }
  .sw.rest { border: 1.5px dashed var(--amber); background: repeating-linear-gradient(135deg, #fff 0 4px, rgba(201, 138, 26, 0.3) 4px 5px); }
  .sw.nx { border: 1.5px dashed var(--slate); }
  .sw.st { border: 1.5px solid var(--alert); }

  @media (max-width: 900px) {
    .lanes { --label: 150px; }
    h3 { font-size: 20px; }
    .tick:not(.n):not(.first):not(.end) { display: none; }
  }
  @media (max-width: 600px) {
    .lanes { --label: 0px; }
    .lane, .later { grid-template-columns: 1fr; }
    .nowline { display: none; }
    .ll { padding-bottom: 0; }
    .lt { min-height: 80px; }
    .job, .fault { top: 6px; }
    .notice, .legend, .opened { margin-left: 0; }
    h3 { width: auto; }
  }
</style>
