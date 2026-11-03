<script lang="ts">
  import {
    ACTION_TEXT, boxGeometry, capacityText, duration, hourTicks, laneText, laneWindow, missingText, WORKER_VERDICT_TEXT,
    type Lanes, type Missing, type Reason, type Stuck, type Worker,
  } from "../lib/fleet";
  import { VERDICT_TEXT, waitWhat, type Health, type HomeQuestion } from "../lib/home";
  import { graphHref, watchHref, workersHref } from "../lib/router";
  import QuestionBox from "./QuestionBox.svelte";

  /** The workers as slot lanes in time, one lane per agent slot, each job a
   * box from its start to its estimated end; above them, whether work moves
   * or is stuck and why. They take the place of the Execution, Agent slots,
   * Runway and Needs a person tiles; the count of what needs a person stays
   * with the Needs a person section below. */
  let {
    lanes, health = null, questions = [], slug, canAct, onanswer, onsessions,
  }: {
    lanes: Lanes | null;
    /** The verdict of an older backend, which sends no lanes. */
    health?: Health | null;
    questions?: HomeQuestion[];
    slug: string;
    /** Only an approver answers or grants; the backend checks the role too. */
    canAct: boolean;
    onanswer: (key: string, text: string) => Promise<string | null>;
    onsessions?: (key: string, sessions: number) => Promise<string | null>;
  } = $props();

  const verdict = $derived(lanes?.verdict ?? health?.verdict ?? null);
  const reasons: Reason[] = $derived(lanes?.reasons ?? health?.reasons ?? []);
  const workers: Worker[] = $derived(lanes?.workers ?? []);
  const missingBySortie = $derived.by(() => {
    const by = new Map<string, Missing[]>();
    for (const m of lanes?.missing ?? []) by.set(m.sortie ?? "", [...(by.get(m.sortie ?? "") ?? []), m]);
    return [...by.entries()];
  });
  const now = $derived(lanes?.generated_at ?? Date.now() / 1000);
  const win = $derived(laneWindow(workers, now));
  const ticks = $derived(hourTicks(win, now));
  const nowAt = $derived(boxGeometry(now, now, win).left);
  const tone = $derived(!verdict ? "none" : verdict === "stuck" || verdict === "idle_now" ? "alert" : verdict === "will_idle" ? "warn" : "ok");
  const word = (v: string | null) => (!v ? "No verdict" : (VERDICT_TEXT as Record<string, string>)[v] ?? v);
  const storyHref = (key: string) => graphHref(slug, { epic: key.split("/")[0], selected: key });
  // What the released stories wait on while slots are free, and who acts on each.
  const waits = $derived(health?.reasons.some((r) => r.code === "waiting") ? (health?.waits ?? []) : []);

  // The stuck box whose action is open in place: its key and what it does.
  let open: { key: string; kind: "answer" | "sessions" } | null = $state(null);
  let grant = $state(4);
  let busy = $state(false);
  let note = $state("");
  let error = $state("");

  const questionOf = (key: string | null) => questions.find((q) => q.key === key);

  function toggle(s: { key?: string | null; action?: string | null }) {
    if (!s.key || (s.action !== "answer" && s.action !== "sessions")) return;
    note = "";
    error = "";
    open = open?.key === s.key && open.kind === s.action ? null : { key: s.key, kind: s.action };
  }

  async function sendGrant(key: string) {
    if (!onsessions || busy) return;
    busy = true;
    error = "";
    const err = await onsessions(key, grant);
    busy = false;
    if (err) {
      error = err;
      return;
    }
    note = `${grant} more sessions granted to ${key.split("/").pop()}; its Sortie runs it again.`;
    open = null;
  }
</script>

{#snippet act(s: Stuck | Reason, inLane: boolean)}
  {#if s.key && s.action === "open" && s.identifier}
    <a class="btn ink" data-act="open" href={watchHref(slug, s.key, s.identifier)}>{ACTION_TEXT.open}</a>
  {:else if s.key && (s.action === "answer" || s.action === "sessions")}
    <button type="button" class="btn go" data-act={s.action} aria-expanded={open?.key === s.key && open.kind === s.action}
      disabled={!canAct && s.action === "sessions"} title={!canAct ? "This needs the approver role" : undefined}
      onclick={() => toggle(s)}>{ACTION_TEXT[s.action]}</button>
  {:else if s.action === "pull" && "url" in s && s.url}
    <a class="btn" data-act="pull" href={s.url} target="_blank" rel="noopener">{ACTION_TEXT.pull}</a>
  {:else if s.key && s.action === "story"}
    <a class="btn" data-act="story" href={storyHref(s.key)}>{ACTION_TEXT.story}</a>
  {/if}
{/snippet}

{#snippet resolve(key: string | null)}
  {#if key && open?.key === key}
    <div class="resolve" data-resolve={open.kind}>
      {#if open.kind === "answer"}
        {@const q = questionOf(key)}
        {#if q}
          <QuestionBox question={q} story={key} canAnswer={canAct} onanswer={(text) => onanswer(key, text)} />
        {:else}
          <p class="cap">The question is no longer open.</p>
        {/if}
      {:else}
        <form class="grant" onsubmit={(e) => { e.preventDefault(); sendGrant(key); }}>
          <label>Sessions to add <input type="number" min="1" max="32" bind:value={grant} /></label>
          <button type="submit" class="btn go" disabled={busy || !canAct || grant < 1}>Grant</button>
          <span class="cap">Its Sortie runs it again, with no change to the workflow.</span>
        </form>
      {/if}
      {#if error}<p class="err" role="alert">{error}</p>{/if}
    </div>
  {/if}
{/snippet}

<section class="lanes-home" aria-labelledby="lanes-h">
  <div class="head {tone}" data-verdict={verdict ?? "none"}>
    <div class="say">
      <h2 id="lanes-h">Workers<span><a href={workersHref(slug)}>all workers</a></span></h2>
      <p class="big">{word(verdict)}</p>
      <p class="sum" data-summary>
        {#if lanes}{`${lanes.busy}${lanes.slots != null ? ` of ${lanes.slots}` : ""} slots busy${lanes.runway != null ? ` · ${lanes.runway} ${lanes.runway === 1 ? "story" : "stories"} ready` : ""}`}
        {:else if health?.slots != null}{health.busy} of {health.slots} slots busy{/if}
      </p>
    </div>
    <ul class="reasons">
      {#each reasons as r, i (i)}
        <li data-reason={r.code} class:ask={r.code === "question"}>
          <span>{#if r.key && !r.action}<a href={storyHref(r.key)}>{r.text}</a>{:else}{r.text}{/if}</span>
          {@render act(r, false)}
        </li>
      {/each}
    </ul>
    {#if waits.length}
      <ul class="waits" aria-label="What the stories wait on">
        {#each waits as w (w.kind + (w.pr?.ref ?? w.stories[0]?.key))}
          <li data-wait={w.kind}>{#if w.pr}{#if w.pr.url}<a href={w.pr.url} target="_blank" rel="noreferrer">{w.pr.ref}</a>{:else}{w.pr.ref}{/if}{" "}{/if}{waitWhat(w)}, for {#each w.stories as s, i (s.key)}{i === 0 ? "" : i === w.stories.length - 1 ? " and " : ", "}<a href={storyHref(s.key)}>{s.key}</a>{/each}: <b data-who>{w.who}</b> {w.does}</li>
        {/each}
      </ul>
    {/if}
  </div>
  {#if note}<p class="ok-note" role="status">{note}</p>{/if}
  {#each missingBySortie as [name, ms] (name)}
    <p class="missing" role="status" data-missing={name}>{missingText(name, ms)}</p>
  {/each}

  {#if workers.length}
    <div class="axis" aria-hidden="true">
      <span class="who"></span>
      <div class="ticks">{#each ticks as t (t.at)}<span style:left="{t.left}%">{t.label}</span>{/each}</div>
    </div>
    {#each workers as w (w.sortie + "/" + w.name)}
      <div class="worker" data-worker={w.name} data-worker-verdict={w.verdict}>
        <div class="wh">
          <b>{w.name}</b>
          <span class="chip {w.verdict}">{WORKER_VERDICT_TEXT[w.verdict]}</span>
          <span class="cap">{capacityText(w).join(" · ")}</span>
        </div>
        {#each w.lanes as lane (lane.slot)}
          {@const s = lane.stuck}
          {@const j = lane.job}
          <div class="lane" data-lane={lane.slot} data-busy={!!j} data-stuck={!!s} aria-label={laneText(lane)}>
            <span class="who">slot {lane.slot}</span>
            <div class="track">
              <i class="now" style:left="{nowAt}%" aria-hidden="true"></i>
              {#if j}
                {@const g = boxGeometry(j.started_at, j.ends_at, win)}
                <div class="box {j.phase}" class:held={!!j.hold} data-job={j.identifier} style:left="{g.left}%" style:width="{g.width}%"
                  title="{j.label}: {j.phase_text}{j.step ? ` at ${j.step}` : ''}, {duration(j.elapsed)}{j.estimated ? ', end estimated' : ''}">
                  <b>{j.label}</b><span class="mono" data-step>{j.phase_text}{j.step ? ` · ${j.step}` : ""} · {duration(j.elapsed)}</span>
                </div>
              {:else if s}
                {@const g = boxGeometry(s.since ?? now, now, win)}
                <div class="box stuck" data-stuck-box={s.identifier} style:left="{g.left}%" style:width="{Math.max(g.width, 12)}%"><b>{s.label}</b><span class="mono">stuck</span></div>
              {/if}
            </div>
          </div>
          {#if s}
            <div class="why" data-why={s.kind}>
              <p><b>Stuck:</b> {s.label} {s.reason}</p>
              {@render act(s, true)}
              {#if j && j.key && j.identifier && s.action !== "open"}<a class="btn ink" data-act="open" href={watchHref(slug, j.key, j.identifier)}>{ACTION_TEXT.open}</a>{/if}
            </div>
            {@render resolve(s.key)}
          {:else if j && j.key && j.identifier}
            <div class="why run"><a class="btn ink sm" data-act="open" href={watchHref(slug, j.key, j.identifier)}>{ACTION_TEXT.open}</a></div>
          {/if}
        {/each}
        {#each w.stuck.filter((x) => x.slot == null) as s (s.identifier)}
          <div class="why" data-why={s.kind}><p><b>Also stuck:</b> {s.label} {s.reason}</p>{@render act(s, true)}</div>
          {@render resolve(s.key)}
        {/each}
        <p class="next" data-next>
          <b>Next:</b>
          {#each w.next as x, i (`${x.identifier}:${x.kind}:${i}`)}{i ? "; " : " "}{#if x.key}<a href={storyHref(x.key)}>{x.label}</a>{:else}{x.label}{/if} ({x.reason}){:else} nothing waits on {w.name}{/each}
        </p>
      </div>
    {/each}
  {/if}
</section>

<style>
  .lanes-home { padding: 14px var(--home-x, 40px) 0; }
  .head { display: grid; grid-template-columns: minmax(220px, 1fr) 2fr; gap: 16px; padding: 14px 18px; border: 1px solid var(--line); border-radius: var(--r-well); background: var(--panel); }
  .head.alert { border-color: #e9b8b3; background: #fbeceb; }
  .head.warn { border-color: #ecd9a8; background: var(--amber-tint); }
  .head.ok { border-color: #bcd6c6; background: var(--done-tint); }
  h2 { font-size: 11px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); }
  h2 span { margin-left: 8px; text-transform: none; letter-spacing: 0; font-weight: 600; }
  h2 a { color: inherit; }
  .big { margin-top: 2px; font-size: 30px; font-weight: 700; letter-spacing: -0.02em; line-height: 1.05; }
  .alert .big { color: #8f241d; }
  .warn .big { color: var(--amber-ink); }
  .ok .big { color: var(--done); }
  .sum { margin-top: 4px; font-size: 12.5px; color: var(--ink); font-variant-numeric: tabular-nums; }
  .reasons { list-style: none; display: flex; flex-direction: column; gap: 6px; font-size: 13px; }
  .reasons li { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; }
  .reasons li span { min-width: 0; overflow-wrap: anywhere; }
  .reasons li.ask span { display: -webkit-box; -webkit-line-clamp: 3; line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
  .reasons a { color: inherit; }
  .waits { grid-column: 2; padding-left: 16px; font-size: 12.5px; color: #6f1c16; }
  .waits a { color: inherit; }
  .waits b { font-weight: 600; }
  .ok-note { margin-top: 8px; font-size: 12.5px; color: var(--done); }
  .missing { margin-top: 8px; font-size: 12px; color: var(--amber-ink); }

  .axis, .lane { display: grid; grid-template-columns: 64px 1fr; gap: 8px; align-items: center; }
  .axis { margin-top: 14px; height: 16px; }
  .ticks { position: relative; height: 16px; font: 500 10.5px var(--font-mono); color: var(--mute); }
  .ticks span { position: absolute; transform: translateX(-50%); white-space: nowrap; }
  .worker { margin-top: 10px; padding: 10px 0 8px; border-top: 1px solid var(--hair); }
  .wh { display: flex; align-items: baseline; gap: 10px; flex-wrap: wrap; margin-bottom: 6px; }
  .wh b { font-size: 14px; }
  .chip { padding: 1px 8px; border: 1px solid var(--line); border-radius: var(--r-pill); font-size: 11px; font-weight: 700; color: var(--mute); }
  .chip.stuck { border-color: #e9b8b3; background: #fbeceb; color: #8f241d; }
  .chip.moving { border-color: #bcd6c6; background: var(--done-tint); color: var(--done); }
  .chip.draining { border-color: #ecd9a8; background: var(--amber-tint); color: var(--amber-ink); }
  .who { font: 500 11px var(--font-mono); color: var(--mute); }
  .lane { min-height: 34px; }
  .track { position: relative; height: 30px; border: 1.5px dashed var(--slate); border-radius: 5px; background: #fff; overflow: hidden; }
  .lane[data-busy="true"] .track, .lane[data-stuck="true"] .track { border-style: solid; border-color: var(--line); }
  .now { position: absolute; top: 0; bottom: 0; width: 1.5px; background: var(--amber); }
  .box { position: absolute; top: 2px; bottom: 2px; display: flex; align-items: center; gap: 8px; padding: 0 8px; border-radius: 4px; background: var(--ink); color: #fff; overflow: hidden; white-space: nowrap; font-size: 12px; }
  .box .mono { font: 500 10.5px var(--font-mono); color: #d1d1d6; overflow: hidden; text-overflow: ellipsis; }
  .box.review { background: #3c4a63; }
  .box.fix { background: #6b4a1e; }
  .box.live_proof { background: #1f5a42; }
  .box.deploy { background: #4b3a6b; }
  .box.stuck, .box.held { background: #fbeceb; color: #8f241d; border: 1.5px solid var(--alert); }
  .box.stuck .mono, .box.held .mono { color: #8f241d; }
  .why { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; margin: 2px 0 6px 72px; font-size: 12.5px; color: #6f1c16; }
  .why.run { justify-content: flex-end; margin-top: -2px; }
  .why p { min-width: 0; overflow-wrap: anywhere; }
  .resolve { margin: 0 0 8px 72px; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--r-card); background: var(--panel); }
  .grant { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 12.5px; }
  .grant input { width: 64px; margin-left: 6px; padding: 4px 6px; border: 1px solid var(--line); border-radius: var(--r-button); }
  .err { margin-top: 6px; color: var(--alert); font-size: 12px; }
  .next { margin: 4px 0 0 72px; font-size: 12.5px; color: var(--ink); }
  .next a { color: inherit; }
  .cap { font-size: 11.5px; color: var(--mute); }
  .btn { flex: none; display: inline-flex; align-items: center; height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; color: var(--ink); font-size: 12px; font-weight: 700; text-decoration: none; white-space: nowrap; }
  .btn.sm { height: 22px; }
  .btn.go { border-color: var(--ink); background: var(--ink); color: #fff; }
  .btn.ink { border-color: var(--ink); }
  .btn:disabled { opacity: 0.45; cursor: default; }
  @media (max-width: 760px) {
    .head { grid-template-columns: 1fr; }
    .why, .resolve, .next { margin-left: 0; }
  }
</style>
