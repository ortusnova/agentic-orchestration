<script lang="ts">
  import { onDestroy, tick } from "svelte";
  import { applyEvent, composeMessage, emptyTranscript, groupTurns, withUserMessages, type ChatBlock, type Transcript as TranscriptState } from "../lib/chat";
  import { GraphModel } from "../lib/model";
  import type { SessionClient } from "../lib/session";
  import type { StoryActions } from "../lib/sync";
  import type { GraphNode, MergedGraph, ViewState } from "../lib/types";
  import type { WorkerClient } from "../lib/worker";
  import type { ConfigCategory, ConfigOption } from "../lib/api";
  import ContextCard from "./ContextCard.svelte";
  import type { SpecReader } from "../lib/spec";
  import Icon from "./Icon.svelte";
  import ModelPicker from "./ModelPicker.svelte";
  import Transcript from "./Transcript.svelte";
  import WorkerPane from "./WorkerPane.svelte";

  let {
    doc, view, session, onselect, actions, worker = null, onwatch, onunwatch, specs,
  }: {
    doc: MergedGraph;
    view: ViewState;
    session: SessionClient;
    onselect: (key: string | null) => void;
    actions?: StoryActions;
    /** The worker a person watches, shown in place of the planning session. */
    worker?: { node: GraphNode; client: WorkerClient } | null;
    onwatch?: (node: GraphNode) => void;
    onunwatch?: () => void;
    /** Reads the requirements a selected story covers. */
    specs?: SpecReader;
  } = $props();

  const model = $derived(new GraphModel(doc));
  let active = $state(false);
  let starting = $state(false);
  let error = $state("");
  let transcript: TranscriptState = $state(emptyTranscript());
  let messages: { time: number; text: string }[] = $state([]);
  let draft = $state("");
  let attached: string[] = $state([]);
  let log: HTMLElement | undefined = $state();
  let unsubscribe: (() => void) | null = null;
  let config: ConfigOption[] = $state([]);
  let configReady = $state(false);
  let configBusy = $state(false);
  let configUnavailable = $state(false);
  let canConfigure = $state(true);
  let configTimer: ReturnType<typeof setTimeout> | undefined;

  const modelOption = $derived(config.find((o) => o.category === "model"));
  const levelOption = $derived(config.find((o) => o.category === "thought_level"));

  // The agent reports its options once the session is open and its model
  // selected; until then, ask again shortly.
  async function loadConfig(tries = 60) {
    clearTimeout(configTimer);
    try {
      const c = await session.config();
      config = c.options;
      configReady = c.ready;
      if (c.ready) return;
    } catch {
      // the session is still starting, or has ended
    }
    if (active && tries > 0) configTimer = setTimeout(() => loadConfig(tries - 1), 500);
    else configUnavailable = true;
  }

  async function choose(category: ConfigCategory, value: string) {
    configBusy = true;
    error = "";
    try {
      config = (await session.setConfig(category, value)).options;
    } catch (err) {
      error = err instanceof Error ? err.message : "The agent did not take the change.";
    } finally {
      configBusy = false;
    }
  }

  const items: ChatBlock[] = $derived(groupTurns(withUserMessages(transcript.items, messages), transcript.busy));
  const attachedNodes = $derived(attached.map((k) => model.node(k)).filter((n) => n !== undefined));

  // A newly selected node is offered to the chat as context.
  let lastSelected: string | null = null;
  $effect(() => {
    const sel = view.selected;
    if (sel && sel !== lastSelected && !attached.includes(sel)) attached = [...attached, sel];
    lastSelected = sel;
  });

  // A selected story whose worker is running opens that worker's session, once
  // per selection, so going back to planning sticks until the selection changes.
  let autoWatched: string | null = null;
  $effect(() => {
    const node = view.selected ? model.node(view.selected) : undefined;
    const id = node?.kind === "story" && node.runtime?.watchable ? (node.tracker?.identifier ?? null) : null;
    const key = id && `${node!.plan_key}|${id}`;
    if (key && key !== autoWatched && worker?.node.tracker?.identifier !== id) onwatch?.(node!);
    autoWatched = key;
  });

  async function refresh() {
    try {
      const status = await session.status();
      active = status.active;
      messages = status.messages ?? [];
      error = status.error ?? "";
      if (active && !unsubscribe) connect();
      canConfigure = status.can_configure !== false;
      if (active && canConfigure && !configReady) loadConfig();
      if (!active) {
        config = [];
        configReady = false;
        configUnavailable = false;
      }
    } catch {
      error = "The planning session is unavailable.";
    }
  }

  function connect() {
    transcript = emptyTranscript();
    unsubscribe = session.subscribe(
      async (ev) => {
        transcript = applyEvent(transcript, ev);
        await tick();
        if (log) log.scrollTop = log.scrollHeight;
      },
      () => {
        unsubscribe = null;
        active = false;
      },
    );
  }

  /** The first message starts the session; it is also the agent's first turn. */
  async function start(message: string, typed: string) {
    starting = true;
    error = "";
    messages = [{ time: Date.now() / 1000, text: message }];
    try {
      await session.start(message);
      attached = [];
      await refresh();
    } catch (err) {
      messages = [];
      draft = typed;
      error = err instanceof Error ? err.message : "Could not start the session.";
    } finally {
      starting = false;
    }
  }

  async function stop() {
    unsubscribe?.();
    unsubscribe = null;
    clearTimeout(configTimer);
    await session.stop();
    active = false;
    config = [];
    configReady = false;
    configUnavailable = false;
  }

  async function send(e?: Event) {
    e?.preventDefault();
    const text = draft.trim();
    if (!text || starting) return;
    const message = composeMessage(text, attachedNodes);
    draft = "";
    if (!active) return start(message, text);
    try {
      await session.command({ op: "steer", text: message });
      messages = [...messages, { time: Date.now() / 1000, text: message }];
      attached = [];
    } catch (err) {
      draft = text;
      error = err instanceof Error ? err.message : "The message was not sent.";
    }
  }

  async function permit(id: string, option: string) {
    try {
      await session.command({ op: "permit", id, option });
    } catch (err) {
      error = err instanceof Error ? err.message : "The answer was not sent.";
    }
  }

  function onkey(e: KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey) send(e);
  }

  refresh();
  onDestroy(() => {
    unsubscribe?.();
    clearTimeout(configTimer);
  });
</script>

<ContextCard {model} {view} {attached} {actions} {onwatch} {specs} watching={worker?.node.tracker?.identifier ?? null} onattach={(k) => (attached = [...attached, k])} onselect={(k) => onselect(k)} />

{#if worker}
  {#key worker.node.tracker?.identifier}
    <WorkerPane node={worker.node} client={worker.client} {actions} onclose={() => onunwatch?.()} />
  {/key}
{:else}
<div class="session-bar">
  <span class="state" class:on={active || starting}><i></i>{starting ? "Starting session" : active ? (transcript.busy ? "Agent working" : "Planning session") : "No session"}</span>
  {#if active}
    <button type="button" class="ghost" onclick={stop}>End session</button>
  {/if}
</div>

<Transcript bind:el={log} {items} onpermit={permit}
  empty="Say what to plan, or select a node in the graph and point at it. Your first message starts a session with the planning agent. It works in the spec store and writes only under openspec/." />

{#if error}<p class="error" role="alert">{error}</p>{/if}

<form class="composer" onsubmit={send}>
  {#if attachedNodes.length}
    <div class="attached" aria-label="Attached to the next message">
      {#each attachedNodes as n (n.plan_key)}
        <span class="att" data-attached={n.plan_key}>{n.plan_key}<button type="button" aria-label="Remove {n.plan_key}" onclick={() => (attached = attached.filter((k) => k !== n.plan_key))}><Icon name="close" size={12} /></button></span>
      {/each}
    </div>
  {/if}
  <textarea bind:value={draft} onkeydown={onkey} rows="3" placeholder={active ? "Message the planning agent" : "What should we plan?"} disabled={starting} aria-label="Message"></textarea>
  <div class="settings" aria-label="Agent settings">
    {#if active && !canConfigure}
      <p class="note" data-note="not-configurable">This session started before model choice existed, so its model can't change. End it; your next message starts a session where you can choose.</p>
    {:else if modelOption}
      <ModelPicker option={modelOption} disabled={!configReady || configBusy} onpick={(v) => choose("model", v)} />
    {:else if active}
      <p class="note">{configReady ? "The agent offers no model choice." : configUnavailable ? "The model choice did not load. End the session and start a new one to choose a model." : "Model: selecting…"}</p>
    {/if}
    {#if levelOption}
      <label class="level">
        <span>{levelOption.category === "thought_level" ? "Thinking" : levelOption.name}</span>
        <select
          value={levelOption.currentValue} disabled={!configReady || configBusy} data-config="thought_level"
          onchange={(e) => choose("thought_level", e.currentTarget.value)}
        >
          {#each levelOption.options as o (o.value)}<option value={o.value}>{o.name}</option>{/each}
        </select>
      </label>
    {/if}
  </div>
  <button type="submit" class="primary" disabled={starting || !draft.trim()}>{starting ? "Starting…" : "Send"}</button>
</form>
{/if}

<style>
  .session-bar { flex: none; display: flex; align-items: center; justify-content: space-between; gap: var(--s-md); padding: 10px var(--s-panel); border-bottom: 1px solid var(--hair); }
  .state { display: flex; align-items: center; gap: 8px; font-size: 12px; font-weight: 600; color: var(--mute); }
  .state i { width: 8px; height: 8px; border-radius: 50%; background: var(--slate); }
  .state.on { color: var(--ink); }
  .state.on i { background: var(--amber); box-shadow: var(--shadow-pulse); }
  .primary, .ghost { height: 42px; padding: 0 16px; border-radius: var(--r-button); font-weight: 700; font-size: 13px; }
  .primary { background: var(--ink); color: #fff; }
  .primary:hover:not(:disabled) { background: #000; }
  .primary:disabled { opacity: 0.45; cursor: default; }
  .ghost { background: #fff; border: 1px solid var(--line); }
  .ghost:hover { border-color: var(--ink); }
  .error { flex: none; padding: 6px var(--s-panel); color: var(--alert); font-size: 12px; }
  .composer button[type="submit"] { align-self: end; }
  .composer { flex: none; display: grid; grid-template-columns: 1fr auto; gap: 8px; align-items: stretch; padding: var(--s-md) var(--s-panel) var(--s-panel); border-top: 1px solid var(--line); }
  .attached { grid-column: 1 / -1; display: flex; flex-wrap: wrap; gap: 6px; }
  .att { display: inline-flex; align-items: center; gap: 2px; height: 28px; padding: 0 2px 0 10px; border: 1px solid var(--amber); border-radius: var(--r-pill); background: var(--amber-tint); font: 500 11.5px var(--font-mono); }
  .att button { width: 24px; height: 24px; display: grid; place-items: center; border-radius: 50%; color: var(--amber-ink); }
  .att button:hover { background: rgba(201, 138, 26, 0.2); }
  .settings { min-width: 0; display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
  .note { margin: 0; font-size: 12px; line-height: 1.4; color: var(--mute); max-width: 52ch; }
  .level { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 4px 0 10px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; font-size: 12px; color: var(--mute); }
  .level:hover:has(select:not(:disabled)) { border-color: var(--ink); }
  .level select { height: 30px; border: 0; background: transparent; font: 600 12px var(--font-sans); color: var(--ink); cursor: pointer; }
  .level select:disabled { opacity: 0.55; cursor: default; }
  textarea { grid-column: 1 / -1; width: 100%; resize: none; min-height: 64px; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; line-height: 1.45; }
  textarea:disabled { background: var(--strip); }
</style>
