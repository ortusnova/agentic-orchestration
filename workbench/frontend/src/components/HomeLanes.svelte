<script lang="ts">
  import {
    ACTION_TEXT, boxGeometry, capacityText, clockText, duration, LANE_FROM, LANE_TICKS, LANE_TO, lanePct, laneText, laneWindow,
    leaseText, minutes, missingText, WORKER_VERDICT_TEXT,
    type Job, type Lanes, type Missing, type Projected, type Reason, type Stuck, type Worker,
  } from "../lib/fleet";
  import { VERDICT_TEXT, waitWhat, type Health, type HomeQuestion } from "../lib/home";
  import { graphHref, watchHref, workersHref } from "../lib/router";
  import QuestionBox from "./QuestionBox.svelte";

  /** The workers as slot lanes in time, one lane per agent slot, on an axis
   * from 30 minutes back to an hour ahead, now at one third: left of now the
   * runs each slot ran, at now the running job, its elapsed time solid and
   * its estimated rest hatched, and right of now the jobs that wait for a
   * slot where the forecast places them. Above them, whether work moves
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
  const win = $derived(laneWindow(now));
  const nowAt = lanePct(0, 0);
  const waiting = $derived(lanes?.waiting ?? []);
  const geo = (from: number, to: number) => boxGeometry(from, to, win);
  const upper = (t: string) => t.charAt(0).toUpperCase() + t.slice(1);
  const overdue = (j: Job) => j.ends_at <= now;
  const leftText = (j: Job) => (overdue(j) ? "overdue" : `~${minutes(j.ends_at - now)} left`);
  // The elapsed share of a job's box, solid; the rest is its estimate, hatched.
  const split = (j: Job) => {
    const g = geo(j.started_at, Math.max(j.ends_at, now));
    const n = lanePct(now, now) - g.left;
    return g.width > 0 ? Math.max(0, Math.min(100, Math.round((n / g.width) * 100))) : 100;
  };
  const shown = (ns: Projected[] | undefined) => (ns ?? []).filter((x) => x.starts_at < win.end);
  const hidden = (ns: Projected[] | undefined) => (ns ?? []).length - shown(ns).length;
  const laterOf = (w: Worker) => w.waits.filter((x) => x.kind === "live_proof" && x.identifier);
  // Waiting on the supervisor asks nothing of the operator: no alarm colour.
  // Full is Sortie's cap at work, every agent it allows busy: no alarm either.
  const tone = $derived(!verdict || verdict === "waiting" ? "none" : verdict === "stuck" || verdict === "idle_now" ? "alert" : verdict === "will_idle" ? "warn" : "ok");
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
        {#if lanes}{`${lanes.busy}${lanes.slots != null ? ` of ${lanes.slots}` : ""} slots busy${lanes.cap != null && (lanes.slots == null || lanes.cap < lanes.slots) ? ` · Sortie cap ${lanes.cap}` : ""}${lanes.runway != null ? ` · ${lanes.runway} ${lanes.runway === 1 ? "story" : "stories"} ready` : ""}`}
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
      {#each LANE_TICKS as t (t.at)}
        <span class="tick" class:n={t.at === 0} class:first={t.at === LANE_FROM} class:end={t.at === LANE_TO} style:left="{lanePct(now + t.at, now)}%">{t.label}</span>
      {/each}
    </div>
    {#each workers as w (w.sortie + "/" + w.name)}
      <div class="worker" data-worker={w.name} data-worker-verdict={w.verdict}>
        <div class="wh">
          <h3>{w.name}</h3>
          <span class="busy"><b>{w.busy}</b> of {w.slots} slots busy</span>
          <span class="chip {w.verdict}">{WORKER_VERDICT_TEXT[w.verdict]}</span>
          <span class="host">{capacityText(w).slice(1).join(" · ")}</span>
        </div>
        {#each w.lanes as lane (lane.slot)}
          {@const s = lane.stuck}
          {@const j = lane.job}
          {@const nexts = shown(lane.next)}
          <div class="lane" data-lane={lane.slot} data-busy={!!j} data-stuck={!!s} aria-label={laneText(lane)}>
            <div class="ll">
              <div class="no">Slot {lane.slot}</div>
              {#if j}
                <div class="st" class:stalled={!!j.hold || overdue(j)} class:working={!j.hold && !overdue(j)}>{upper(j.phase_text)} · {j.hold ? "held" : overdue(j) ? "overdue" : "working"}</div>
                <div class="jt"><span class="id">{j.identifier}</span> <b>{j.label}</b></div>
                <div class="jm"><b>{duration(j.elapsed)}</b> · <span class:alarm={overdue(j)}>{leftText(j)}</span>
                  {#if j.key && j.identifier && !(s && s.action === "open")}<a class="btn ink sm" data-act="open" href={watchHref(slug, j.key, j.identifier)}>{ACTION_TEXT.open}</a>{/if}</div>
              {:else if s}
                <div class="st stalled">Stuck</div>
                <p class="alarm">{s.label}</p>
              {:else}
                <div class="st free">Free</div>
                {#if nexts[0]}<p>next: {nexts[0].label}{nexts[0].starts_at > now + 30 ? ` in ~${minutes(nexts[0].starts_at - now)}` : ", now"}</p>{/if}
              {/if}
            </div>
            <div class="lt">
              <i class="now" style:left="{nowAt}%" aria-hidden="true"></i>
              {#each lane.past ?? [] as r (r.identifier + ":" + r.started_at)}
                {@const g = geo(r.started_at, r.ended_at)}
                <a class="job ran {r.status ?? ''}" class:cut={r.started_at < win.start} data-past={r.identifier} href={r.key ? storyHref(r.key) : undefined}
                  style:left="{g.left}%" style:width="calc({g.width}% - 2px)">
                  <span class="h"><span class="id">{r.identifier}</span><b>{r.label}</b></span>
                  <span class="f"><span>{r.status ?? "ended"}</span><span>{duration(r.ended_at - r.started_at)}</span></span>
                  <span class="tip" role="tooltip"><b>{r.identifier} {r.title}</b><span>{upper(r.phase_text)}{r.rule ? ` · rule ${r.rule}` : ""} · {r.status ?? "ended"}</span>
                    <span>ran {duration(r.ended_at - r.started_at)}, {clockText(r.started_at, now)} to {clockText(r.ended_at, now)} · ended {duration(now - r.ended_at)} ago</span>
                    {#if r.error}<span class="mute">{r.error}</span>{/if}</span>
                </a>
              {/each}
              {#if j}
                {@const g = geo(j.started_at, Math.max(j.ends_at, now))}
                <svelte:element this={j.key && j.identifier ? "a" : "div"} class="job" class:stalled={!!j.hold || overdue(j)} class:cut={j.started_at < win.start}
                  role={j.key && j.identifier ? undefined : "group"}
                  href={j.key && j.identifier ? watchHref(slug, j.key, j.identifier) : undefined} data-job={j.identifier}
                  style:left="{g.left}%" style:width="calc({g.width}% - 2px)" style:--split="{split(j)}%">
                  <span class="h"><span class="id">{j.identifier}</span><b>{j.label}</b></span>
                  <span class="f"><span class="mono" data-step>{j.phase_text}{j.step ? ` · ${j.step}` : ""} · {duration(j.elapsed)}</span><span class:alarm={overdue(j)}>{leftText(j)}</span></span>
                  <span class="tip" role="tooltip"><b>{j.identifier} {j.title}</b>
                    <span>{upper(j.phase_text)}{j.step ? ` · ${j.step}` : ""}{j.stage ? ` · rule ${j.stage}` : ""}</span>
                    <span><b>{duration(j.elapsed)}</b> running, since {clockText(j.started_at, now)}</span>
                    <span class:alarm={overdue(j)}>{overdue(j) ? `overdue: estimated to end at ${clockText(j.ends_at, now)}` : `${leftText(j)}, to about ${clockText(j.ends_at, now)}`} · {j.estimated ? "a default for its phase" : "median of past runs"}</span>
                    {#if j.hold}<span class="alarm">{j.hold.reason}</span>{/if}
                    {#if j.status_text}<span class="mute">{j.status_text}</span>{/if}</span>
                </svelte:element>
              {:else if s}
                {@const g = geo(s.since ?? now - 300, now)}
                <div class="job stalled" class:cut={(s.since ?? now) < win.start} data-stuck-box={s.identifier} style:left="{g.left}%" style:width="calc({g.width}% - 2px)">
                  <span class="h"><span class="id">{s.identifier}</span><b>{s.label}</b></span><span class="f"><span class="alarm">stuck</span>{#if s.since}<span>{duration(now - s.since)}</span>{/if}</span>
                </div>
              {/if}
              {#each nexts as x (x.identifier)}
                {@const g = geo(x.starts_at, x.ends_at)}
                <a class="job next" data-next-job={x.identifier} href={x.key ? storyHref(x.key) : undefined} style:left="{g.left}%" style:width="calc({g.width}% - 2px)">
                  <span class="h"><span class="id">{x.identifier}</span><b>{x.label}</b></span>
                  <span class="f">next · ~{minutes(x.estimate)}</span>
                  <span class="tip" role="tooltip"><b>{x.identifier} {x.title}</b><span>Next on this slot, as the forecast places it{x.rule ? ` · rule ${x.rule}` : ""}</span>
                    <span>{x.starts_at > now + 30 ? `starts in about ${minutes(x.starts_at - now)}` : "starts as soon as it is dispatched"} · takes about {minutes(x.estimate)}</span>
                    <span class="mute">{x.basis}; the forecast flows again as slots open</span></span>
                </a>
              {/each}
              {#if hidden(lane.next)}<span class="more">+ {hidden(lane.next)} more</span>{/if}
            </div>
          </div>
          {#if s}
            <div class="why" data-why={s.kind}>
              <p><b>Stuck:</b> {s.label} {s.reason}</p>
              {@render act(s, true)}
            </div>
            {@render resolve(s.key)}
          {/if}
        {/each}
        {#each w.stuck.filter((x) => x.slot == null) as s (s.identifier)}
          <div class="why" data-why={s.kind}><p><b>Also stuck:</b> {s.label} {s.reason}</p>{@render act(s, true)}</div>
          {@render resolve(s.key)}
        {/each}
        {#each w.waits.filter((x) => x.kind === "supervisor") as x (x.identifier)}
          <div class="sup" data-wait-supervisor={x.identifier}>
            <p>{#if x.key}<a href={storyHref(x.key)}>{x.label}</a>{:else}{x.label}{/if} {x.reason}</p>
            {#if x.lease}<p class="lease" data-lease>{leaseText(x.lease, now)}</p>{/if}
          </div>
        {/each}
        {#if laterOf(w).length || w.later || w.past_hidden}
          <div class="later" data-next>
            <h4>Then on {w.name}</h4>
            <ul>
              {#each laterOf(w) as x, i (`${x.identifier}:${i}`)}
                <li><i></i>{#if x.key}<a href={storyHref(x.key)}><b>{x.label}</b></a>{:else}<b>{x.label}</b>{/if} <small>({x.reason})</small></li>
              {/each}
              {#if w.later}<li><i></i><b>+ {w.later} more</b><small>the forecast places them more than 6 hours ahead</small></li>{/if}
              {#if w.past_hidden}<li><i></i><b>{w.past_hidden} earlier {w.past_hidden === 1 ? "run" : "runs"}</b><small>ran at once with more than its slots</small></li>{/if}
            </ul>
          </div>
        {/if}
      </div>
    {/each}
    {#if waiting.length}
      <div class="later nowhere" data-waiting>
        <h4>Waiting for a worker</h4>
        <ul>
          {#each waiting as x (x.identifier)}
            <li class={x.kind === "capability" ? "unowned" : "held"} data-waiting-for={x.identifier}><i></i>{#if x.key}<a href={storyHref(x.key)}><b>{x.label}</b></a>{:else}<b>{x.label}</b>{/if}
              <small>{x.reason}{x.release ? `; ${x.release}` : ""}</small></li>
          {/each}
        </ul>
      </div>
    {/if}
    {#if lanes?.unattributed}
      <p class="cap unattributed" data-unattributed>{lanes.unattributed} {lanes.unattributed === 1 ? "run" : "runs"} of the last hour ended on no worker the workbench saw running it; Sortie's run history does not record the worker.</p>
    {/if}
    <div class="legend" aria-hidden="true">
      <span><i class="sw ran"></i>ran</span>
      <span><i class="sw so-far"></i>run so far</span>
      <span><i class="sw rest"></i>estimated rest</span>
      <span><i class="sw next"></i>next on this slot (forecast)</span>
      <span><i class="sw stalled"></i>stuck or overdue</span>
    </div>
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

  /* The prototype's lanes (.impeccable/mocks/execution/lanes.html): a label column, then the axis. */
  .lanes-home { --label: 250px; }
  .axis { position: relative; height: 40px; margin: 14px 0 0 var(--label); border-bottom: 1px solid var(--ink-rule); }
  .tick { position: absolute; bottom: 5px; transform: translateX(-50%); white-space: nowrap; font: italic 400 15px var(--font-serif); color: var(--mute); }
  .tick.first { transform: none; }
  .tick.end { transform: translateX(-100%); }
  .tick.n { bottom: 8px; padding: 2px 8px; border-radius: var(--r-tag); background: var(--ink-rule); color: #fff; font: 700 10px var(--font-sans); letter-spacing: 0.05em; }
  .worker { margin-top: 18px; }
  .wh { display: flex; align-items: baseline; gap: 14px; flex-wrap: wrap; padding: 6px 0 8px; border-bottom: 1px solid var(--line); }
  .wh h3 { width: calc(var(--label) - 14px); font: 700 24px/1.1 var(--font-sans); letter-spacing: -0.02em; }
  .wh .busy { font-size: 15px; }
  .wh .busy b { font-size: 20px; }
  .wh .host { color: var(--mute); font: 500 11px var(--font-mono); }
  .chip { padding: 1px 8px; border: 1px solid var(--line); border-radius: var(--r-pill); font-size: 11px; font-weight: 700; color: var(--mute); }
  .chip.stuck { border-color: #e9b8b3; background: #fbeceb; color: #8f241d; }
  .chip.moving { border-color: #bcd6c6; background: var(--done-tint); color: var(--done); }
  .chip.draining { border-color: #ecd9a8; background: var(--amber-tint); color: var(--amber-ink); }
  .lane { display: grid; grid-template-columns: var(--label) 1fr; min-height: 96px; border-bottom: 1px solid var(--hair); }
  .ll { min-width: 0; padding: 12px 14px 10px 0; }
  .ll .no { font: 700 11px var(--font-sans); letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); }
  .ll .st { margin-top: 2px; font-weight: 600; }
  .st.working { color: var(--amber-ink); }
  .st.stalled { color: var(--alert); }
  .st.free { color: var(--slate-ink); font: italic 400 17px var(--font-serif); }
  .ll p { margin-top: 2px; color: var(--mute); overflow-wrap: anywhere; }
  .ll p.alarm { color: var(--alert); font-weight: 600; }
  .jt { margin-top: 4px; font-size: 14px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .jt b { font-weight: 600; }
  .jm { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; color: var(--mute); font-variant-numeric: tabular-nums; }
  .jm b { color: var(--ink); }
  .jm .btn { margin-left: 8px; }
  .alarm { color: var(--alert) !important; }
  .id { font: 500 11px var(--font-mono); color: var(--mute); }
  .lt { position: relative; min-height: 96px; }
  .now { position: absolute; top: 0; bottom: 0; width: 1.5px; background: var(--ink-rule); z-index: 2; pointer-events: none; }
  .job { position: absolute; z-index: 3; top: 18px; height: 60px; min-width: 6px; padding: 7px 10px; display: flex; flex-direction: column; justify-content: space-between;
    border: 1.5px solid var(--amber); border-radius: var(--r-card); color: var(--ink); text-align: left; text-decoration: none; font-size: 13px;
    background: linear-gradient(to right, var(--amber-tint) 0 var(--split, 100%), transparent var(--split, 100%)), repeating-linear-gradient(135deg, #fff 0 6px, rgba(201, 138, 26, 0.18) 6px 7px);
    transition: box-shadow 0.2s cubic-bezier(0.2, 0.8, 0.2, 1); }
  a.job:hover { box-shadow: var(--shadow-lift); }
  .job:hover, .job:focus-visible { z-index: 6; }
  .job .h, .job .f { display: flex; gap: 6px; align-items: baseline; min-width: 0; max-width: 100%; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  .job .h b { font-weight: 600; overflow: hidden; text-overflow: ellipsis; }
  .job .f { gap: 12px; color: var(--mute); font-variant-numeric: tabular-nums; }
  .job .f .mono { overflow: hidden; text-overflow: ellipsis; }
  .job.stalled { border-color: var(--alert); background: #fff; }
  .job.next { border: 1.5px dashed var(--slate); background: #fff; color: var(--mute); }
  .job.ran { border-color: var(--slate); background: var(--strip); color: var(--slate-ink); }
  .job.ran.failed, .job.ran.cancelled { border-color: #e9b8b3; }
  .job.cut { border-left-style: dashed; border-top-left-radius: 0; border-bottom-left-radius: 0; }
  .more { position: absolute; right: 6px; top: 36px; color: var(--mute); font-weight: 600; }
  .tip { display: none; position: absolute; left: 0; bottom: calc(100% + 6px); z-index: 7; min-width: 280px; max-width: 380px; padding: 10px 12px; flex-direction: column; gap: 3px;
    border-radius: var(--r-button); background: var(--ink); color: #fff; white-space: normal; font-weight: 400; box-shadow: 0 10px 24px -12px rgba(20, 22, 26, 0.5); pointer-events: none; }
  .tip b { font-weight: 600; }
  .tip span { color: #d6d6da; }
  .tip .mute { color: var(--ink-mute); }
  .tip .alarm { color: #f08a80 !important; }
  .job:hover .tip, .job:focus-visible .tip { display: flex; }
  .why { display: flex; align-items: baseline; justify-content: space-between; gap: 10px; margin: 6px 0 6px var(--label); font-size: 12.5px; color: #6f1c16; }
  .why p { min-width: 0; overflow-wrap: anywhere; }
  .later { display: grid; grid-template-columns: var(--label) 1fr; padding: 8px 0 4px; }
  .later h4 { padding-top: 3px; font: 700 11px var(--font-sans); letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); }
  .later ul { list-style: none; display: flex; flex-wrap: wrap; gap: 6px 18px; }
  .later li { display: flex; gap: 6px; align-items: baseline; }
  .later li i { width: 7px; height: 7px; border-radius: 50%; background: var(--slate); display: inline-block; flex: none; }
  .later li a { color: inherit; }
  .later li small { color: var(--mute); }
  .later li.unowned { color: var(--alert); }
  .later li.unowned i { background: var(--alert); }
  .later li.held { color: var(--alert); }
  .later li.held i { background: none; border: 1.5px solid var(--alert); }
  .nowhere { margin-top: 14px; border-top: 1px solid var(--line); }
  .unattributed { margin: 8px 0 0 var(--label); }
  .legend { margin: 18px 0 0 var(--label); color: var(--mute); display: flex; gap: 22px; flex-wrap: wrap; font-size: 12.5px; }
  .sw { display: inline-block; width: 22px; height: 10px; margin-right: 6px; border-radius: var(--r-swatch); vertical-align: -1px; }
  .sw.ran { border: 1.5px solid var(--slate); background: var(--strip); }
  .sw.so-far { border: 1.5px solid var(--amber); background: var(--amber-tint); }
  .sw.rest { border: 1.5px dashed var(--amber); background: repeating-linear-gradient(135deg, #fff 0 4px, rgba(201, 138, 26, 0.3) 4px 5px); }
  .sw.next { border: 1.5px dashed var(--slate); }
  .sw.stalled { border: 1.5px solid var(--alert); }
  .resolve { margin: 0 0 8px var(--label); padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--r-card); background: var(--panel); }
  .grant { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 12.5px; }
  .grant input { width: 64px; margin-left: 6px; padding: 4px 6px; border: 1px solid var(--line); border-radius: var(--r-button); }
  .err { margin-top: 6px; color: var(--alert); font-size: 12px; }
  .sup { margin: 2px 0 6px var(--label); padding: 6px 10px; border-left: 3px solid var(--slate); background: var(--panel); font-size: 12.5px; color: var(--ink); }
  .sup p { overflow-wrap: anywhere; }
  .sup a { color: inherit; font-weight: 600; }
  .sup .lease { color: var(--mute); font-size: 12px; }
  .cap { font-size: 11.5px; color: var(--mute); }
  .btn { flex: none; display: inline-flex; align-items: center; height: 26px; padding: 0 10px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; color: var(--ink); font-size: 12px; font-weight: 700; text-decoration: none; white-space: nowrap; }
  .btn.sm { height: 22px; }
  .btn.go { border-color: var(--ink); background: var(--ink); color: #fff; }
  .btn.ink { border-color: var(--ink); }
  .btn:disabled { opacity: 0.45; cursor: default; }
  @media (max-width: 760px) {
    .head { grid-template-columns: 1fr; }
    .lanes-home { --label: 120px; }
    .why, .resolve, .sup { margin-left: 0; }
  }
</style>
