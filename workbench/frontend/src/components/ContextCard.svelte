<script lang="ts">
  import { can } from "../lib/identity.svelte";
  import type { GraphModel } from "../lib/model";
  import { deliveryText, planStatusLabel, rereviewText, type StoryActions } from "../lib/sync";
  import type { GraphNode, ViewState } from "../lib/types";
  import Icon from "./Icon.svelte";
  import { workersOf, workerStatus } from "../lib/worker";
  import { renderInline, renderMarkdown } from "../lib/markdown";
  import { groupCovers, type ChangeRequirements, type CoveredRequirement, type SpecReader } from "../lib/spec";

  let {
    model, view, attached, onattach, onselect, actions, onwatch, watching = null, specs,
  }: {
    model: GraphModel;
    view: ViewState;
    attached: string[];
    onattach: (key: string) => void;
    onselect: (key: string) => void;
    actions?: StoryActions;
    onwatch?: (node: GraphNode) => void;
    /** The identifier of the worker the chat pane shows, if any. */
    watching?: string | null;
    /** Reads the requirements a selected story covers. */
    specs?: SpecReader;
  } = $props();

  let busy = $state("");
  let actError = $state("");
  // An action that asks first: its question and button, until the person answers.
  let pending: { name: string; text: string; label: string; run: () => Promise<unknown> } | null = $state(null);
  $effect(() => {
    void view.selected;
    pending = null;
  });
  async function act(name: string, run: () => Promise<unknown>, ask?: { text: string; label: string }) {
    if (ask) {
      pending = { name, run, ...ask };
      return;
    }
    pending = null;
    busy = name;
    actError = "";
    try {
      await run();
    } catch (err) {
      actError = err instanceof Error ? err.message : "That did not work.";
    } finally {
      busy = "";
    }
  }

  const approver = $derived(!!actions && can("approver"));
  const epicStories = $derived(view.epic ? model.storiesOf(view.epic) : []);
  const drafts = $derived(epicStories.filter((n) => n.plan_status === "draft"));
  const epicNeverApproved = (epic: GraphNode) => model.storiesOf(epic.plan_key).every((n) => n.approval !== "approved");

  let open = $state(true);
  const node = $derived(model.node(view.selected));
  const scope = $derived(view.epic ? model.storiesOf(view.epic) : model.epics);
  const ready = $derived(scope.filter((n) => model.isReady(n)));
  const active = $derived(scope.filter((n) => (n.kind === "epic" ? model.badge(n) !== null : n.runtime !== null)));
  const unplanned = $derived(scope.filter((n) => n.has_spec === false));
  const noun = $derived(view.epic ? "stories" : "epics");
  const workers = $derived(workersOf(view.epic ? model.storiesOf(view.epic) : model.doc.nodes));

  // The selected story's requirements, read once per change and plan version.
  let reqs: ChangeRequirements | null = $state(null);
  let reqError = $state("");
  let expanded: Record<string, boolean> = $state({});
  let reqKey = "";
  $effect(() => {
    const n = node;
    const version = model.doc.version;
    if (!n || !n.covers.length || !specs) {
      reqKey = "";
      reqs = null;
      return;
    }
    const key = `${n.change}@${version}`;
    if (key === reqKey) return;
    if (!reqKey.startsWith(`${n.change}@`)) {
      reqs = null;
      expanded = {};
    }
    reqKey = key;
    reqError = "";
    specs.requirements(n.change).then(
      (r) => { if (reqKey === key) reqs = r; },
      (e) => { if (reqKey === key) { reqs = {}; reqError = e instanceof Error ? e.message : "The specs could not be read."; } },
    );
  });
  const covered = $derived(node ? groupCovers(node.covers, reqs) : []);
  const allOpen = (items: CoveredRequirement[]) => items.every((it) => expanded[it.ref]);
  function toggleAll(items: CoveredRequirement[]) {
    const to = !allOpen(items);
    for (const it of items) expanded[it.ref] = to;
  }

  function why(n: GraphNode): string {
    return model.badge(n)?.label ?? model.statusLabel(n);
  }
</script>

<section class="context" aria-label="Context">
  <header>
    {#if node}
      <p class="meta">{node.kind === "epic" ? "Change" : node.deliverable_kind === "enabler" ? "Enabler" : "Story"} · <span class="mono">{model.identifier(node)}</span>{#if node.tracker}<span class="mono"> · {node.plan_key}</span>{/if}</p>
    {:else}
      <h2 class="glance">{view.epic ? (model.node(view.epic)?.title ?? "This change") : "The plan at a glance"}</h2>
    {/if}
    <button type="button" class="fold" aria-expanded={open} onclick={() => (open = !open)}>{open ? "Hide" : "Show"}</button>
  </header>

  {#if open}
    {#if node}
      <div class="sel">
        <h2>{node.title}</h2>
        <div class="chips">
          <span class="chip {model.statusClass(node)}"><i></i>{model.statusLabel(node)}</span>
          {#if node.status === "orphaned"}<span class="chip">orphaned: no longer in the plan</span>{/if}
          {#if node.plan_status}<span class="chip plan {node.plan_status}" data-plan-status={node.plan_status}>{planStatusLabel(node.plan_status)}</span>{/if}
          {#if node.kind === "story" && node.review}<span class="chip">review: {node.review === "criteria" ? "acceptance criteria" : "a person"}</span>{/if}
          {#if approver && actions}
            {#if node.kind === "epic" && node.approval === "draft" && epicNeverApproved(node)}
              <button type="button" class="chip go" data-act="approve-epic" disabled={!!busy}
                onclick={() => act("approve", () => actions.approve({ epic: node.change }))}>{busy === "approve" ? "Approving…" : "Approve the epic"}</button>
            {/if}
            {#if node.kind === "story" && (node.plan_status === "draft" || node.plan_status === "needs_rereview") && !node.frozen}
              <button type="button" class="chip go" data-act="approve" disabled={!!busy}
                onclick={() => act("approve", () => actions.approve({ keys: [node.plan_key] }))}>{busy === "approve" ? "Approving…" : "Approve"}</button>
            {/if}
          {/if}
          {#if node.kind === "story" && node.runtime?.watchable && onwatch && node.tracker?.identifier !== watching}
            <button type="button" class="chip act" data-act="watch" onclick={() => onwatch(node)}>Watch the worker</button>
          {/if}
          {#if !attached.includes(node.plan_key)}
            <button type="button" class="chip act" onclick={() => onattach(node.plan_key)}>Attach to message</button>
          {/if}
          {#if approver && actions}
            {#if node.kind === "story" && node.plan_status === "released"}
              <button type="button" class="chip act" data-act="pull-back" disabled={!!busy}
                onclick={() => act("pull-back", () => actions.pullBack(node.plan_key))}>{busy === "pull-back" ? "Pulling back…" : "Pull back"}</button>
            {/if}
            {#if node.kind === "story" && node.supersedes && node.plan_status !== "draft"}
              <button type="button" class="chip act" data-act="supersede" disabled={!!busy}
                onclick={() => act("supersede", () => actions.supersede(node.plan_key), { text: `Cancel ${node.supersedes} and move the stories that depend on it to ${node.plan_key}?`, label: `Replace ${node.supersedes}` })}>Confirm: replaces {node.supersedes}</button>
            {/if}
            {#if node.kind === "story" && node.tracker && !node.tracker.terminal}
              <button type="button" class="chip act danger" data-act="cancel" disabled={!!busy}
                onclick={() => act("cancel", () => actions.cancel(node.plan_key), { text: `Cancel ${node.plan_key}? Its worker stops, its issue is cancelled, and the stories that depend on it need re-review.`, label: "Cancel story" })}>Cancel story</button>
            {/if}
          {/if}
        </div>
        {#if pending}
          <div class="confirm" role="group" aria-label={pending.label} data-confirm={pending.name}>
            <p>{pending.text}</p>
            <div>
              <button type="button" class="small danger" data-act="confirm" onclick={() => pending && act(pending.name, pending.run)}>{pending.label}</button>
              <button type="button" class="small" data-act="keep" onclick={() => (pending = null)}>Keep it</button>
            </div>
          </div>
        {/if}
        {#if actError}<p class="err" role="alert">{actError}</p>{/if}
        {#if node.plan_status === "held" && node.held_by?.length}
          <p class="why" data-why="held">Waits for the approval of {#each node.held_by as k, i (k)}{i ? ", " : ""}<button type="button" class="link" onclick={() => onselect(k)}>{k}</button>{/each}.</p>
        {:else if node.plan_status === "needs_rereview"}
          <p class="why warn" data-why="rereview">{rereviewText(node.rereview)}</p>
        {:else if node.plan_status === "delivery_unconfirmed"}
          <p class="why warn" data-why="delivery">{deliveryText(node.delivery)}</p>
        {:else if node.plan_status === "frozen"}
          <p class="why" data-why="frozen">Agents have started it, so its content is fixed. Change scope with a new story that depends on it, or one that supersedes it.</p>
        {/if}
        {#if node.approved_by}<p class="approver">Approved by {node.approved_by.name}{#if node.approved_at} · {new Date(node.approved_at * 1000).toLocaleString()}{/if}</p>{/if}

        {#if node.runtime}
          <div class="agent" class:warn={node.runtime.status !== "running"}>
            <b><span class="pulse" aria-hidden="true"></span>{model.badge(node)?.label}</b>
            {#if node.runtime.last_message}<p>{node.runtime.last_message}</p>{/if}
          </div>
        {/if}

        <dl>
          <dt>Depends on</dt>
          <dd>
            {#each model.blockers(node.plan_key) as k (k)}
              <button type="button" class="link" onclick={() => onselect(k)}>{k}</button>
            {:else}<span class="none">Nothing</span>{/each}
          </dd>
          <dt>Unblocks</dt>
          <dd>
            {#each model.unblocks(node.plan_key) as k (k)}
              <button type="button" class="link" onclick={() => onselect(k)}>{k}</button>
            {:else}<span class="none">Nothing</span>{/each}
          </dd>
          {#if node.delivery}
            <dt>Delivered by</dt>
            <dd class="pulls">
              {#each node.delivery.pulls as p (p.ref)}
                <span class="pull" data-pull={p.ref} data-state={p.state}>
                  {#if p.url}<a class="ext-link" href={p.url} target="_blank" rel="noopener">{p.ref} <Icon name="external" size={13} /></a>{:else}<span class="mono">{p.ref}</span>{/if}
                  {#if p.state === "merged"}
                    <span class="none">merged{#if p.merge_commit} as {#if p.merge_commit_url}<a class="mono" data-commit={p.merge_commit} href={p.merge_commit_url} target="_blank" rel="noopener">{p.merge_commit.slice(0, 7)}</a>{:else}<span class="mono">{p.merge_commit.slice(0, 7)}</span>{/if}{/if}</span>
                  {:else}
                    <span class="none warn">{p.reason ?? p.state}</span>
                  {/if}
                </span>
              {/each}
            </dd>
          {/if}
          {#if node.tracker}
            <dt>Tracker</dt>
            <dd>{#if node.tracker.url}<a class="ext-link" href={node.tracker.url} target="_blank" rel="noopener">{node.tracker.identifier} <Icon name="external" size={13} /></a>{:else}{node.tracker.identifier}{/if}</dd>
          {/if}
          <dt>Spec</dt>
          <dd class:spec={node.covers.length > 0}>
            {#if node.covers.length}
              {#each covered as g (g.capability)}
                <div class="cap" data-capability={g.capability}>
                  <div class="cap-head">
                    <span class="cap-name">{g.capability}</span>
                    {#if reqs && g.items.some((it) => it.requirement)}
                      <button type="button" class="cap-toggle" onclick={() => toggleAll(g.items)}>{allOpen(g.items) ? "Collapse all" : "Expand all"}</button>
                    {/if}
                  </div>
                  <ul class="reqs">
                    {#each g.items as it (it.ref)}
                      <li>
                        <details bind:open={expanded[it.ref]} data-requirement={it.name}>
                          <summary><span class="chev"><Icon name="chevron" size={12} /></span><span>{it.name}</span></summary>
                          <div class="req-body">
                            {#if it.requirement}
                              <div class="req-text">{@html renderMarkdown(it.requirement.text)}</div>
                              {#each it.requirement.scenarios as s (s.name)}
                                <div class="scn">
                                  <p class="scn-name">{s.name}</p>
                                  <ol class="steps">
                                    {#each s.steps as step, i (i)}
                                      <li class:cont={step.keyword === "AND" || step.keyword === "BUT"}><b>{step.keyword.toLowerCase()}</b><span>{@html renderInline(step.text)}</span></li>
                                    {/each}
                                  </ol>
                                </div>
                              {/each}
                            {:else if reqs === null}
                              <p class="none">Loading…</p>
                            {:else}
                              <p class="none">Not found in {node.change}'s specs.</p>
                            {/if}
                          </div>
                        </details>
                      </li>
                    {/each}
                  </ul>
                </div>
              {/each}
              {#if reqError}<p class="err">{reqError}</p>{/if}
            {:else if node.kind === "story"}<span class="none">No spec yet</span>
            {:else}<span class="mono">openspec/changes/{node.change}</span>{/if}
          </dd>
          {#if node.acceptance_criteria.length}
            <dt>Acceptance</dt>
            <dd><ul class="ac">{#each node.acceptance_criteria as a, i (i)}<li>{a}</li>{/each}</ul></dd>
          {/if}
        </dl>
      </div>
    {:else}
      {#if approver && actions && view.epic && drafts.length}
        <div class="acts">
          {#if epicStories.every((n) => n.approval !== "approved")}
            <button type="button" class="attach approve" data-act="approve-epic" disabled={!!busy}
              onclick={() => act("approve", () => actions.approve({ epic: view.epic! }))}>Approve the epic ({drafts.length} {drafts.length === 1 ? "story" : "stories"})</button>
          {:else}
            <button type="button" class="attach approve" data-act="approve-drafts" disabled={!!busy}
              onclick={() => act("approve", () => actions.approve({ keys: drafts.map((n) => n.plan_key) }))}>Approve {drafts.length} draft {drafts.length === 1 ? "story" : "stories"}</button>
          {/if}
        </div>
        {#if actError}<p class="err" role="alert">{actError}</p>{/if}
      {/if}
      <div class="lists">
        {#each [["Ready to start", ready], ["Agent activity", active], ["Without a spec", unplanned]] as [title, list] (title)}
          <div>
            <span class="label">{title}</span>
            <ul class="list" data-list={title}>
              {#each list as GraphNode[] as n (n.plan_key)}
                <li><button type="button" onclick={() => onselect(n.plan_key)}><span class="dot {model.statusClass(n)}"></span>{n.title}<em>{why(n)}</em></button></li>
              {:else}<li class="none">No {noun}</li>{/each}
            </ul>
          </div>
        {/each}
      </div>
      <div class="workers">
        <span class="label">Workers</span>
        {#if !model.doc.sortie.dispatched}
          <p class="none" data-note="not-dispatched">Sortie does not run agents for this project.</p>
        {:else}
          <ul class="list" data-list="Workers">
            {#each workers as w (w.plan_key)}
              <li class="worker" data-worker={w.tracker?.identifier}>
                <div class="row">
                  <button type="button" onclick={() => onselect(w.plan_key)}>
                    <span class="dot prog"></span><span class="mono">{w.tracker?.identifier}</span>&nbsp;{w.title}
                    <em>{[workerStatus(w), w.runtime?.stage, w.runtime?.turn_count && `turn ${w.runtime.turn_count}`].filter(Boolean).join(" · ")}</em>
                  </button>
                  {#if w.runtime?.watchable && onwatch}<button type="button" class="watch" data-act="watch" onclick={() => onwatch(w)}>Watch</button>{/if}
                </div>
                {#if w.runtime?.last_message}<p class="last">{w.runtime.last_message}</p>{/if}
              </li>
            {:else}<li class="none">No workers</li>{/each}
          </ul>
        {/if}
      </div>
    {/if}
  {/if}
</section>

<style>
  .why { font-size: 12.5px; color: var(--mute); }
  .why.warn { color: var(--amber-ink); }
  .approver { font-size: 12px; color: var(--mute); }
  .chip.plan.ready, .chip.plan.released { color: var(--done); }
  .chip.plan.needs_rereview, .chip.plan.delivery_unconfirmed { color: var(--amber-ink); }
  .chip.plan.delivered { color: var(--done); }
  dd.pulls { flex-direction: column; gap: 4px; }
  .pull { display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px; }
  .none.warn { color: var(--amber-ink); }
  .danger { color: var(--alert); }
  .confirm { display: flex; flex-direction: column; gap: 8px; margin-top: var(--s-md); padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--r-card); background: var(--panel); font-size: 12.5px; }
  .confirm div { display: flex; gap: 6px; }
  .small { height: 30px; padding: 0 12px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; color: var(--ink); font-size: 12.5px; font-weight: 600; }
  .small:hover { background: var(--strip); }
  .small.danger { border-color: var(--alert); color: var(--alert); }
  .workers { display: flex; flex-direction: column; gap: 6px; }
  .worker .row { display: flex; align-items: center; gap: 8px; }
  .list .worker .row button:first-child { flex: 1; min-width: 0; margin-right: 0; }
  .worker .last { margin: 0 0 4px 16px; font-size: 12px; color: var(--mute); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .list .worker .watch { flex: none; width: auto; margin: 0; justify-content: center; height: 28px; padding: 0 12px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; font-size: 12px; }
  .list .worker .watch:hover { border-color: var(--ink); background: #fff; }
  /* A shadow at the bottom edge shows only while more content sits below it:
     the white cover scrolls with the content and hides it at the end. */
  .context {
    background:
      linear-gradient(rgba(255, 255, 255, 0), #fff 70%) 0 100% / 100% 40px no-repeat local,
      radial-gradient(farthest-side at 50% 100%, rgba(20, 22, 26, 0.14), rgba(20, 22, 26, 0)) 0 100% / 100% 12px no-repeat scroll,
      #fff;
  }
  .context { flex: none; max-height: 60%; overflow-y: auto; padding: var(--s-gutter) var(--s-panel); border-bottom: 1px solid var(--line); display: flex; flex-direction: column; gap: var(--s-md); }
  header { display: flex; justify-content: space-between; align-items: center; gap: var(--s-md); }
  .meta { font-size: 12px; color: var(--mute); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .meta .mono { font: 500 12px var(--font-mono); }
  .glance { font-size: 15px; font-weight: 600; letter-spacing: -0.005em; }
  .ext-link { display: inline-flex; align-items: center; gap: 4px; }
  .pulse { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--amber); box-shadow: var(--shadow-pulse); }
  .agent.warn .pulse { background: var(--alert); box-shadow: none; }
  .label { font-size: 11px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); }
  .fold { flex: none; min-height: 32px; padding: 0 8px; margin-right: -8px; font-size: 12px; font-weight: 600; color: var(--mute); }
  @media (max-width: 900px) { .fold { min-height: 44px; min-width: 44px; } }
  .fold:hover { color: var(--ink); }
  h2:not(.glance) { font-size: 26px; font-weight: 700; line-height: 1.12; letter-spacing: -0.02em; text-wrap: balance; }
  .chips { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 10px; }
  .chip { display: inline-flex; align-items: center; gap: 6px; height: 24px; padding: 0 9px; border: 1px solid var(--line); border-radius: var(--r-pill); font-size: 12px; font-weight: 600; }
  .chip i { width: 8px; height: 8px; border-radius: 50%; background: var(--c); }
  .chip.done { --c: var(--done); border-color: #b9d3c3; background: var(--done-tint); color: var(--done); }
  .chip.prog { --c: var(--amber); border-color: #ecc77f; background: var(--amber-tint); color: var(--amber-ink); }
  .chip.todo { --c: var(--slate); background: var(--slate-tint); color: var(--slate-ink); }
  .chip.cancelled { --c: var(--mute); color: var(--mute); }
  .chip.id { font: 500 11.5px var(--font-mono); color: var(--mute); }
  .agent { margin-top: var(--s-md); padding: 12px 14px; border-radius: var(--r-well); background: var(--ink); color: #fff; }
  .agent b { display: flex; align-items: center; gap: 8px; font-size: 14px; }
  .agent.warn { background: #fff; color: var(--ink); border: 1px solid var(--alert); }
  .agent p { margin-top: 6px; font: 500 12px/1.5 var(--font-mono); color: #c7c7cc; }
  .agent.warn p { color: var(--mute); }
  dl { display: grid; grid-template-columns: 92px 1fr; gap: 8px 12px; margin-top: var(--s-md); font-size: 13px; }
  dt { font-size: 11px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); padding-top: 2px; }
  dd { display: flex; flex-wrap: wrap; gap: 4px 10px; min-width: 0; }
  .ac { list-style: none; display: flex; flex-direction: column; gap: 3px; }
  .mono { font: 500 11.5px var(--font-mono); word-break: break-word; }
  dd.spec { display: flex; flex-direction: column; gap: var(--s-md); }
  .cap-head { display: flex; align-items: baseline; justify-content: space-between; gap: var(--s-sm); }
  .cap-name { font: 500 11.5px var(--font-mono); color: var(--mute); word-break: break-word; }
  .cap-toggle { flex: none; font-size: 12px; font-weight: 600; color: var(--mute); text-decoration: underline; text-underline-offset: 3px; text-decoration-color: var(--line); }
  .cap-toggle:hover { color: var(--ink); text-decoration-color: var(--ink); }
  .reqs { list-style: none; margin-top: 4px; }
  .reqs > li + li { border-top: 1px solid var(--hair); }
  summary { display: flex; align-items: flex-start; gap: 6px; padding: 6px 0; cursor: pointer; list-style: none; font-size: 13px; font-weight: 600; line-height: 1.4; color: var(--ink); }
  summary::-webkit-details-marker { display: none; }
  summary:hover { color: #000; }
  summary:focus-visible { outline: 2px solid var(--ink); outline-offset: 2px; border-radius: var(--r-tag); }
  .chev { display: inline-flex; padding-top: 3px; color: var(--mute); transition: transform 160ms cubic-bezier(0.16, 1, 0.3, 1); }
  details[open] .chev { transform: rotate(90deg); color: var(--ink); }
  .req-body { padding: 0 0 12px 18px; max-width: 68ch; font-size: 12.5px; line-height: 1.55; color: var(--ink); }
  .req-text :global(p + p) { margin-top: 6px; }
  .req-body :global(code) { font: 500 11.5px var(--font-mono); padding: 1px 4px; border-radius: var(--r-tag); background: var(--strip); }
  .scn { margin-top: 10px; }
  .scn-name { font-size: 12.5px; font-weight: 600; color: var(--ink); }
  .steps { list-style: none; display: grid; grid-template-columns: max-content 1fr; gap: 3px 10px; margin-top: 4px; }
  .steps li { display: contents; }
  .steps b { font-size: 10.5px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); padding-top: 2px; text-align: right; }
  .steps li.cont b { font-weight: 600; color: var(--slate-ink); }
  .ac li { padding-left: 12px; position: relative; }
  .ac li::before { content: "–"; position: absolute; left: 0; color: var(--mute); }
  .link { font: 500 11.5px var(--font-mono); text-decoration: underline; text-underline-offset: 3px; text-decoration-color: var(--line); }
  .link:hover { text-decoration-color: var(--ink); }
  .none { color: var(--mute); font-size: 12px; }
  .acts { display: flex; gap: 8px; flex-wrap: wrap; margin-top: var(--s-md); }
  .chip.act { background: #fff; cursor: pointer; }
  .chip.act:hover:not(:disabled) { border-color: var(--ink); }
  .chip.act.danger { border-color: var(--alert); }
  .chip.act:disabled { opacity: 0.6; cursor: default; }
  .chip.go { background: var(--done); border-color: var(--done); color: #fff; cursor: pointer; }
  .chip.go:hover:not(:disabled) { background: #245a3f; border-color: #245a3f; }
  .chip.go:disabled { opacity: 0.6; cursor: default; }
  .attach.approve { background: var(--ink); color: #fff; border-color: var(--ink); }
  .attach.approve:hover:not(:disabled) { background: #000; }
  .err { color: var(--alert); font-size: 12px; margin-top: 6px; }
  .attach { height: 42px; padding: 0 14px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; font-weight: 600; }
  .attach:hover { border-color: var(--ink); }
  .lists { display: flex; flex-direction: column; gap: var(--s-md); }
  .list { list-style: none; display: flex; flex-direction: column; gap: 2px; margin-top: 6px; }
  .list button { width: 100%; display: flex; align-items: center; gap: 10px; padding: 4px 6px; margin: 0 -6px; border-radius: var(--r-tab); font-size: 13px; font-weight: 600; text-align: left; }
  .list button:hover { background: var(--strip); }
  .list em { margin-left: auto; font-size: 12px; font-style: normal; font-weight: 500; color: var(--mute); white-space: nowrap; }
  .dot { width: 10px; height: 10px; flex: none; border-radius: 50%; border: 2.5px solid var(--c); background: #fff; }
  .dot.done { --c: var(--done); } .dot.prog { --c: var(--amber); } .dot.todo { --c: var(--slate); } .dot.cancelled { --c: var(--line); }
</style>
