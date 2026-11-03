<script lang="ts">
  import type { GraphModel } from "../lib/model";
  import { planStatusLabel } from "../lib/sync";
  import type { GraphNode } from "../lib/types";

  const PULL_STATE: Record<string, string> = { open: "open", closed: "closed unmerged", unknown: "lookup failed", pending: "checking" };

  let {
    node, model, x, y, selected = false, near = false, marked, onselect, onopen,
  }: {
    node: GraphNode; model: GraphModel; x: number; y: number;
    selected?: boolean; near?: boolean;
    /** The short name of the milestone the home marked this change for. */
    marked?: string;
    onselect: (key: string) => void; onopen: (key: string) => void;
  } = $props();

  const cls = $derived(model.statusClass(node));
  const unplanned = $derived(node.has_spec === false);
  const badge = $derived(model.badge(node));
  const progress = $derived(node.kind === "epic" ? model.progress(node) : null);
  const pct = $derived(progress && progress.total ? (progress.done / progress.total) * 100 : cls === "done" ? 100 : 0);
  const meta = $derived.by(() => {
    if (progress && progress.total === 0) return node.status === "draft" && node.approval === "unplanned" ? "no breakdown" : "no stories";
    if (progress) return `${progress.done}/${progress.total} done`;
    if (cls === "done") return "complete";
    if (cls === "cancelled") return "cancelled";
    if (node.question) return "waits on an answer";
    if (node.plan_status === "delivery_unconfirmed") {
      const p = node.delivery?.pulls.find((x) => x.state !== "merged");
      return p ? `${p.ref} ${PULL_STATE[p.state] ?? p.state}` : "delivery not confirmed";
    }
    if (node.plan_status === "needs_rereview") return "needs re-review";
    if (node.plan_status === "held") return "held for approval";
    if (node.plan_status === "needs_spec") return "needs a spec";
    if (node.plan_status === "draft") return "not approved";
    if (node.plan_status === "preview") return "ready for you to look";
    if (node.plan_status === "building") return "building";
    if (cls === "prog") return "in progress";
    const open = model.blockers(node.plan_key).filter((b) => !model.isDone(model.node(b))).length;
    return open ? `waiting on ${open}` : "ready";
  });
  const label = $derived(
    `${model.identifier(node)} ${node.title}, ${model.statusLabel(node)}` +
      (unplanned ? ", no spec" : "") +
      (node.plan_status && node.plan_status !== "draft" ? `, ${planStatusLabel(node.plan_status).toLowerCase()}` : "") +
      (node.status === "draft" ? ", draft" : node.status === "orphaned" ? ", orphaned" : "") +
      (marked ? `, in ${marked}` : "") +
      (node.question ? ", asks a question" : ""),
  );
</script>

<button
  type="button"
  class="card {cls}"
  class:unplanned
  class:sel={selected}
  class:near
  class:marked={!!marked}
  class:draft={node.status === "draft"}
  class:orphaned={node.status === "orphaned"}
  data-key={node.plan_key}
  style:left="{x}px"
  style:top="{y}px"
  aria-pressed={selected}
  aria-label={label}
  onclick={() => onselect(node.plan_key)}
  ondblclick={() => node.kind === "epic" && onopen(node.plan_key)}
>
  {#if badge}<span class="badge {badge.kind}"><i></i>{badge.label}</span>{/if}
  <span class="r1">
    <span class="id" title={node.plan_key}>{node.tracker ? node.tracker.identifier : node.kind === "story" ? node.plan_key.slice(node.change.length + 1) : node.plan_key}</span>
    <span class="st">{model.statusLabel(node)}</span>
  </span>
  <span class="t">{node.title}</span>
  <span class="pg">{#if node.kind === "epic" || pct === 100}<i style:width="{pct}%"></i>{/if}</span>
  <span class="r3">
    <span class="meta" class:warn={node.plan_status === "needs_rereview" || node.plan_status === "delivery_unconfirmed"} data-plan-status={node.plan_status ?? null}>{meta}</span>
    <span class="tags">
      {#if marked}<span class="tag in-ms">{marked}</span>{/if}
      {#if node.status === "draft"}<span class="tag marker draft-tag">Draft</span>{/if}
      {#if node.status === "orphaned"}<span class="tag marker orphan-tag">Orphaned</span>{/if}
      {#if unplanned}<span class="tag nospec">No spec</span>{:else if node.has_spec}<span class="tag">OpenSpec</span>{/if}
    </span>
  </span>
</button>

<style>
  .card {
    position: absolute; width: var(--card-w); height: var(--card-h);
    display: flex; flex-direction: column; padding: 11px var(--s-card-x) 10px;
    border: 1.5px solid var(--c); border-radius: var(--r-card); background: var(--t);
    text-align: left; transition: box-shadow 0.2s ease-out, opacity 0.2s ease-out;
  }
  .card.done { --c: var(--done); --tx: var(--done); --t: var(--done-tint); --h: rgba(47, 111, 78, 0.22); }
  .card.prog { --c: var(--amber); --tx: var(--amber-ink); --t: var(--amber-tint); --h: rgba(201, 138, 26, 0.3); }
  .card.todo { --c: var(--slate); --tx: var(--slate-ink); --t: var(--slate-tint); --h: rgba(138, 143, 152, 0.3); }
  .card.cancelled { --c: var(--line); --tx: var(--mute); --t: #fff; --h: rgba(138, 143, 152, 0.18); opacity: 0.75; }
  .card.cancelled .t { text-decoration: line-through; text-decoration-color: var(--mute); }
  .card.unplanned { background: repeating-linear-gradient(135deg, var(--h) 0 1px, #fff 1px 7px); }
  .card.draft { border-style: dashed; }
  .card.orphaned { opacity: 0.8; }
  .meta.warn { color: var(--amber-ink); font-weight: 700; }
  .card.marked { outline: 2px solid var(--ink-rule); outline-offset: 3px; }
  .tag.in-ms { background: var(--ink); border-color: var(--ink); color: #fff; font-weight: 600; }
  .card:hover { box-shadow: var(--shadow-lift); }
  .card.sel { box-shadow: var(--shadow-halo); }
  :global(.score.focused) .card:not(.sel):not(.near) { opacity: 0.45; }
  .r1 { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
  .id { font: 500 10.5px var(--font-mono); color: var(--mute); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .st {
    display: flex; align-items: center; gap: 5px; flex: none; color: var(--tx);
    font-size: 10px; font-weight: 700; letter-spacing: 0.05em; text-transform: uppercase;
  }
  .st::before { content: ""; width: 6px; height: 6px; border-radius: 50%; background: var(--c); }
  .t {
    flex: none; margin: 5px 0 auto; padding-bottom: 1px; font-size: 15px; font-weight: 600; line-height: 1.25; letter-spacing: -0.005em;
    display: -webkit-box; -webkit-line-clamp: 2; line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
  }
  .unplanned .t, .unplanned .meta { align-self: flex-start; margin-left: -3px; padding: 0 3px; background: rgba(255, 255, 255, 0.88); }
  .pg { flex: none; height: 3px; margin: 6px 0 5px; border-radius: 2px; background: rgba(20, 22, 26, 0.08); }
  .pg i { display: block; height: 100%; border-radius: 2px; background: var(--c); }
  .r3 { display: flex; justify-content: space-between; align-items: center; gap: 6px; font-size: 11.5px; color: var(--mute); }
  .tags { display: flex; gap: 4px; white-space: nowrap; }
  .tag { padding: 1px 6px; border: 1px solid rgba(20, 22, 26, 0.18); border-radius: var(--r-tag); background: #fff; color: var(--ink); font-size: 10.5px; }
  .tag.nospec, .tag.marker { border-style: dashed; color: var(--mute); }
  .badge {
    position: absolute; top: -11px; right: 10px; display: flex; align-items: center; gap: 6px;
    padding: 3px 9px 3px 7px; border-radius: var(--r-pill); background: var(--ink); color: #fff;
    font-size: 10.5px; font-weight: 600; white-space: nowrap;
  }
  .badge i { width: 6px; height: 6px; border-radius: 50%; background: var(--amber); }
  .badge.retry { background: #fff; color: var(--ink); border: 1px solid var(--amber); }
  .badge.budget { background: #fff; color: var(--alert); border: 1px solid var(--alert); }
  .badge.budget i { background: var(--alert); }
  .badge.ask { background: var(--amber); color: var(--ink); }
  .badge.ask i { background: var(--ink); }
</style>
