<script lang="ts">
  import type { Snippet } from "svelte";
  import { ACTION_TEXT, capacityText, duration, missingText, releaseText, WAIT_TEXT, WORKER_VERDICT_TEXT, type Fleet } from "../lib/fleet";
  import { graphHref, watchHref } from "../lib/router";
  import TopBar, { type Tab } from "./TopBar.svelte";

  /** Every worker host: its capacity and use, its jobs with their story,
   * phase, step and time, what waits on it and why, the machines no Sortie
   * started; and every open pull request of a story with what it waits on.
   * It follows the graph's stream, so it updates live. */
  let { fleet, slug, projectName = "", tabs = [], actions }: {
    fleet: Fleet | null | undefined;
    slug: string;
    projectName?: string;
    tabs?: Tab[];
    actions?: Snippet;
  } = $props();

  const now = $derived(fleet?.generated_at ?? Date.now() / 1000);
  const storyHref = (key: string) => graphHref(slug, { epic: key.split("/")[0], selected: key });
  const ago = (t: number | null) => (t == null ? "" : `${duration(now - t)} ago`);
</script>

<TopBar crumbs={[{ label: "Projects", href: "#/" }, { label: projectName || slug, href: `#/p/${encodeURIComponent(slug)}` }, { label: "Workers" }]} {tabs}>
  {#if actions}{@render actions()}{/if}
</TopBar>

<main class="workers" aria-label="Workers">
  {#if fleet?.error}<p class="band alert" role="alert">The workers could not be computed: {fleet.error}</p>{/if}
  {#each fleet?.sorties.filter((s) => !s.reachable) ?? [] as s (s.name)}
    <p class="band warn" role="status" data-unreachable={s.name}>{s.name}'s Sortie is unreachable{s.error ? `: ${s.error}` : ""}. Its workers are not shown.</p>
  {/each}
  {#each fleet?.sorties.filter((s) => s.reachable && s.missing?.length) ?? [] as s (s.name)}
    <p class="band warn" role="status" data-missing={s.name}>{missingText(s.name, s.missing ?? [])}</p>
  {/each}
  {#if !fleet?.workers.length}
    <div class="empty"><b>No worker reports yet</b>The workers appear once a Sortie answers with its state.</div>
  {/if}

  {#each fleet?.workers ?? [] as w (w.sortie + "/" + w.name)}
    <section class="row" data-worker-row={w.name} aria-labelledby="w-{w.name}">
      <header>
        <h2 id="w-{w.name}">{w.name}<span class="chip {w.verdict}">{WORKER_VERDICT_TEXT[w.verdict]}</span></h2>
        <p class="cap">{w.remote ? `worker of ${w.sortie}'s Sortie` : `${w.sortie}'s Sortie runs here`}{w.last_seen ? ` · seen ${ago(w.last_seen)}` : ""}</p>
        <ul class="use" data-capacity>{#each capacityText(w) as c (c)}<li>{c}</li>{/each}</ul>
        <ul class="reasons">{#each w.reasons as r, i (i)}<li data-reason={r.code}>{r.text}</li>{/each}</ul>
      </header>

      <h3>Jobs</h3>
      {#if w.jobs.length || w.stuck.length}
        <table>
          <thead><tr><th scope="col">Slot</th><th scope="col">Story</th><th scope="col">Phase</th><th scope="col">Current step</th><th scope="col" class="n">Time</th><th scope="col"></th></tr></thead>
          <tbody>
            {#each w.jobs as j (j.identifier)}
              <tr data-job={j.identifier} class:held={!!j.hold}>
                <td class="mono">{j.slot ?? ""}</td>
                <td>{#if j.key}<a href={storyHref(j.key)}>{j.label}</a>{:else}{j.label}{/if}<small>{j.identifier}</small></td>
                <td>{j.phase_text}{#if j.hold}<small class="warn">{j.hold.reason}</small>{/if}</td>
                <td class="mono" data-step>{j.step ?? (w.known?.steps === false ? "not reported by its Sortie" : "—")}{#if j.step_at}<small>{ago(j.step_at)}</small>{/if}</td>
                <td class="n">{duration(j.elapsed)}</td>
                <td>{#if j.key && j.identifier}<a class="btn" data-act="open" href={watchHref(slug, j.key, j.identifier)}>{ACTION_TEXT.open}</a>{/if}</td>
              </tr>
            {/each}
            {#each w.stuck as s (s.identifier)}
              <tr data-job={s.identifier} class="held">
                <td class="mono">{s.slot ?? ""}</td>
                <td>{#if s.key}<a href={storyHref(s.key)}>{s.label}</a>{:else}{s.label}{/if}<small>{s.identifier}</small></td>
                <td colspan="2" class="warn">stuck: {s.reason}</td>
                <td class="n">{duration(s.since != null ? now - s.since : null)}</td>
                <td>{#if s.key}<a class="btn go" data-act={s.action} href={storyHref(s.key)}>{s.action ? ACTION_TEXT[s.action] : ACTION_TEXT.story}</a>{/if}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {:else}
        <p class="cap">Nothing runs on {w.name}.</p>
      {/if}

      <h3>What waits on {w.name}</h3>
      {#if w.waits.length}
        <ul class="waits">
          {#each w.waits as x, i (i)}
            <li data-wait={x.kind} data-wait-for={x.identifier ?? x.label}>
              <b>{#if x.key}<a href={storyHref(x.key)}>{x.label}</a>{:else}{x.label}{/if}</b>
              {#if x.pull}<span class="mono">{#if x.pull.url}<a href={x.pull.url} target="_blank" rel="noopener">#{x.pull.number}</a>{:else}#{x.pull.number}{/if}</span>{/if}
              waits for {WAIT_TEXT[x.kind] ?? x.kind}: {x.reason}{#if x.release}<small>{releaseText(x.release, "when")}</small>{/if}
            </li>
          {/each}
        </ul>
      {:else}
        <p class="cap">Nothing waits on {w.name}.</p>
      {/if}

      <h3>Machines Sortie did not start</h3>
      {#if w.known?.unmanaged === false}
        <p class="cap warn" data-unmanaged-unknown>{w.sortie}'s Sortie does not report the machines on {w.name} it did not start, so they cannot be shown.</p>
      {:else if w.unmanaged.length}
        <ul class="waits">
          {#each w.unmanaged as m (m.name)}
            <li data-unmanaged={m.name}><b class="mono">{m.name}</b> {m.state ? `${m.state}, ` : ""}for {m.age_text}</li>
          {/each}
        </ul>
      {:else}
        <p class="cap">None: every machine on {w.name} is Sortie's.</p>
      {/if}
    </section>
  {/each}

  {#if fleet?.pulls.length}
    <section class="row" aria-labelledby="pulls-h">
      <h2 id="pulls-h">Open pull requests</h2>
      <table>
        <thead><tr><th scope="col">Story</th><th scope="col">Pull request</th><th scope="col">Waits on</th><th scope="col">Why</th><th scope="col">Worker</th></tr></thead>
        <tbody>
          {#each fleet.pulls as p (`${p.identifier}#${p.number}`)}
            <tr data-pull-row={p.identifier} data-waits-on={p.waits_on} class:held={p.stuck}>
              <td>{#if p.key}<a href={storyHref(p.key)}>{p.label}</a>{:else}{p.label}{/if}</td>
              <td class="mono">{#if p.url}<a href={p.url} target="_blank" rel="noopener">{p.repository ?? ""}#{p.number}</a>{:else}#{p.number}{/if}</td>
              <td data-waits>{p.waits_text}{#if p.stuck}<small class="warn">stuck: nothing merges it</small>{/if}</td>
              <td>{p.reason ?? ""}</td>
              <td>{p.worker}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </section>
  {/if}
</main>

<style>
  .workers { flex: 1; min-height: 0; overflow-y: auto; padding: 0 40px 32px; }
  .band { margin: 0 -40px; padding: 9px 40px; border-bottom: 1px solid var(--line); font-size: 12.5px; }
  .band.alert { border-color: #e9b8b3; background: #fbeceb; color: #8f241d; }
  .band.warn { border-color: #ecd9a8; background: var(--amber-tint); color: var(--amber-ink); }
  .empty { margin: 80px 0; max-width: 44ch; color: var(--mute); }
  .empty b { display: block; margin-bottom: 4px; font: italic 400 20px var(--font-serif); color: var(--ink); }
  .row { padding: 22px 0 6px; border-bottom: 1px solid var(--line); }
  h2 { display: flex; align-items: center; gap: 10px; font-size: 18px; font-weight: 700; }
  h3 { margin: 14px 0 6px; font-size: 11px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); }
  .chip { padding: 1px 8px; border: 1px solid var(--line); border-radius: var(--r-pill); font-size: 11px; font-weight: 700; color: var(--mute); }
  .chip.stuck { border-color: #e9b8b3; background: #fbeceb; color: #8f241d; }
  .chip.moving { border-color: #bcd6c6; background: var(--done-tint); color: var(--done); }
  .chip.draining { border-color: #ecd9a8; background: var(--amber-tint); color: var(--amber-ink); }
  .use { display: flex; flex-wrap: wrap; gap: 6px 16px; margin-top: 6px; list-style: none; font-size: 12.5px; font-variant-numeric: tabular-nums; }
  .reasons { margin-top: 6px; list-style: none; font-size: 13px; color: var(--ink); }
  .reasons li[data-reason]:not([data-reason="moving"]):not([data-reason="idle"]):not([data-reason="cap_full"]) { color: #8f241d; }
  table { width: 100%; border-collapse: collapse; font-size: 13px; }
  th, td { padding: 6px 8px; border-bottom: 1px solid var(--hair); text-align: left; vertical-align: baseline; }
  thead th { font-size: 11px; font-weight: 700; letter-spacing: 0.05em; text-transform: uppercase; color: var(--mute); border-bottom-color: var(--line); }
  td small { display: block; font-size: 11px; color: var(--mute); }
  td a { color: var(--ink); }
  tr.held td { background: #fbeceb; }
  .warn { color: #8f241d; }
  .n { text-align: right; white-space: nowrap; font-variant-numeric: tabular-nums; }
  .mono { font: 500 12px var(--font-mono); }
  .waits { list-style: none; display: flex; flex-direction: column; gap: 4px; font-size: 13px; }
  .waits small { margin-left: 8px; font-size: 11.5px; color: var(--mute); }
  .waits a { color: var(--ink); }
  .cap { font-size: 12px; color: var(--mute); }
  .btn { display: inline-flex; align-items: center; height: 24px; padding: 0 10px; border: 1px solid var(--ink); border-radius: var(--r-button); background: #fff; color: var(--ink); font-size: 12px; font-weight: 700; text-decoration: none; }
  .btn.go { background: var(--ink); color: #fff; }
  @media (max-width: 760px) {
    .workers { padding: 0 16px 24px; overflow-x: auto; }
    .band { margin: 0 -16px; padding: 9px 16px; }
  }
</style>
