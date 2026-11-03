<script lang="ts">
  import { onDestroy, tick } from "svelte";
  import { applyEvent, emptyTranscript, groupTurns, withUserMessages, type ChatBlock, type Transcript as TranscriptState } from "../lib/chat";
  import { can } from "../lib/identity.svelte";
  import type { StoryActions } from "../lib/sync";
  import type { GraphNode } from "../lib/types";
  import { workerStatus, type LastTranscript, type WorkerClient } from "../lib/worker";
  import Transcript from "./Transcript.svelte";

  /** The session of the worker Sortie runs for one story, in the chat pane.
   * Anyone may watch; only an approver steers, takes over, answers, or cancels. */
  let { node, client, actions, onclose }: { node: GraphNode; client: WorkerClient; actions?: StoryActions; onclose: () => void } = $props();

  let transcript: TranscriptState = $state(emptyTranscript());
  let notices: { time: number; text: string }[] = $state([]);
  let messages: { time: number; text: string; by: string; session: string | null }[] = $state([]);
  let run = $state<{ session_id: string; stage: string | null } | null>(null);
  let floor = $state("");
  let ended = $state(false);
  let last: LastTranscript | null = $state(null);
  let down = $state(false);
  let draft = $state("");
  let busy = $state("");
  let error = $state("");
  let log: HTMLElement | undefined = $state();

  const approver = $derived(can("approver"));
  const identifier = $derived(node.tracker?.identifier ?? node.plan_key);
  const stage = $derived(run?.stage ?? node.runtime?.stage ?? null);
  const shown = $derived(messages.filter((m) => !run || !m.session || m.session === run.session_id));
  const items: ChatBlock[] = $derived.by(() => {
    const own = transcript.items;
    const withNotes = [...own, ...notices.map((n, i) => ({ kind: "notice" as const, key: `n${i}`, text: n.text, time: n.time }))];
    return groupTurns(withUserMessages(withNotes, shown), transcript.busy);
  });

  async function loadMessages() {
    try {
      const r = await client.messages();
      messages = r.commands
        .filter((c) => (c.op === "steer" || c.op === "prompt") && c.text)
        .map((c) => ({ time: c.time, text: c.text!, by: c.actor_name, session: c.session_id }));
    } catch {
      // the list refreshes on the next tick
    }
  }

  const note = (text: string) => (notices = [...notices, { time: Date.now(), text }]);

  // svelte-ignore state_referenced_locally
  const unsubscribe = client.subscribe({
    onEvent: async (ev) => {
      if (ev.type === "state" && ev.floor) floor = ev.floor;
      transcript = applyEvent(transcript, ev);
      await tick();
      if (log) log.scrollTop = log.scrollHeight;
    },
    onRun: (r) => {
      if (r.follow) {
        transcript = emptyTranscript();
        notices = [];
        note(`The earlier run ended. Following the new run${r.stage ? ` (${r.stage})` : ""}.`);
      }
      run = { session_id: r.session_id, stage: r.stage };
      ended = false;
    },
    onTranscript: (t) => {
      // Nothing runs now: the agent's last session, as the Sortie that ran it kept it.
      last = t;
      const lines = t.events.map((e) => {
        const at = e.at ? Date.parse(e.at) : NaN;
        return { time: Number.isNaN(at) ? Date.now() : at, text: e.tool_line ? `${e.text} · ${e.tool_line}` : e.text };
      });
      const first = Math.min(Date.now(), ...lines.map((l) => l.time));
      notices = [...notices, { time: first - 1, text: `Nothing runs for this story now. Its last session, as ${t.sortie}'s Sortie kept it:` }, ...lines];
    },
    onEnded: () => {
      ended = true;
      note(last ? "That was the last session. Sortie runs nothing more for this story now." : "The worker ended. Sortie runs nothing more for this story now.");
    },
    onDown: () => (down = true),
  });
  loadMessages();
  const timer = setInterval(loadMessages, 3000);
  onDestroy(() => {
    unsubscribe();
    clearInterval(timer);
  });

  async function send(cmd: Record<string, unknown>, name: string) {
    busy = name;
    error = "";
    try {
      await client.command(cmd);
      await loadMessages();
      return true;
    } catch (err) {
      error = err instanceof Error ? err.message : "The worker did not take the command.";
      return false;
    } finally {
      busy = "";
    }
  }

  async function steer(e?: Event) {
    e?.preventDefault();
    const text = draft.trim();
    if (!text) return;
    draft = "";
    if (!(await send({ op: "steer", text }, "steer"))) draft = text;
  }

  function onkey(e: KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey) steer(e);
  }

  let asking = $state(false);
  async function cancel() {
    if (!actions) return;
    asking = false;
    busy = "cancel";
    error = "";
    try {
      await actions.cancel(node.plan_key);
    } catch (err) {
      error = err instanceof Error ? err.message : "The cancel failed.";
    } finally {
      busy = "";
    }
  }
</script>

<div class="session-bar" data-worker={identifier}>
  <div class="who">
    <span class="state" class:on={!ended && !down}><i></i>Watching {identifier}</span>
    <span class="sub">{[node.plan_key, stage, workerStatus(node), floor && `floor: ${floor}`].filter(Boolean).join(" · ")}</span>
  </div>
  <button type="button" class="ghost" data-act="back-to-planning" onclick={onclose}>Back to planning</button>
</div>

<Transcript bind:el={log} {items} onpermit={approver ? (id, option) => send({ op: "permit", id, option }, "permit") : undefined}
  empty={down ? "The worker's session is not available." : "Waiting for the worker's session…"} />

{#if error}<p class="error" role="alert">{error}</p>{/if}

{#if approver}
  <form class="composer" onsubmit={steer}>
    <textarea bind:value={draft} onkeydown={onkey} rows="2" placeholder="Steer the agent" disabled={ended} aria-label="Steer the agent"></textarea>
    <div class="controls">
      <button type="button" class="ghost" data-act="interrupt" disabled={!!busy || ended} onclick={() => send({ op: "interrupt" }, "interrupt")}>Take over</button>
      <button type="button" class="ghost" data-act="handback" disabled={!!busy || ended} onclick={() => send({ op: "handback" }, "handback")}>Hand back</button>
      {#if actions && node.tracker && !node.tracker.terminal}
        <button type="button" class="ghost danger" data-act="cancel-worker" disabled={!!busy} onclick={() => (asking = true)}>Cancel story</button>
      {/if}
      <button type="submit" class="primary" disabled={!!busy || ended || !draft.trim()}>Send</button>
    </div>
    {#if asking}
      <div class="confirm" role="group" aria-label="Cancel story" data-confirm="cancel">
        <p>Cancel {node.plan_key}? Its worker stops, its issue is cancelled, and the stories that depend on it need re-review.</p>
        <div>
          <button type="button" class="ghost danger" data-act="confirm" onclick={cancel}>Cancel story</button>
          <button type="button" class="ghost" data-act="keep" onclick={() => (asking = false)}>Keep it</button>
        </div>
      </div>
    {/if}
  </form>
{:else}
  <p class="readonly" data-note="watch-only">You can watch this worker. Steering it needs the approver role.</p>
{/if}

<style>
  .session-bar { flex: none; display: flex; align-items: center; justify-content: space-between; gap: var(--s-md); padding: 10px var(--s-panel); border-bottom: 1px solid var(--hair); }
  .who { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
  .state { display: flex; align-items: center; gap: 8px; font-size: 12px; font-weight: 700; color: var(--mute); }
  .state i { width: 8px; height: 8px; border-radius: 50%; background: var(--slate); }
  .state.on { color: var(--ink); }
  .state.on i { background: var(--amber); box-shadow: var(--shadow-pulse); }
  .sub { font: 500 11.5px var(--font-mono); color: var(--mute); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .primary, .ghost { height: 36px; padding: 0 14px; border-radius: var(--r-button); font-weight: 700; font-size: 13px; }
  .primary { background: var(--ink); color: #fff; }
  .primary:hover:not(:disabled) { background: #000; }
  .primary:disabled, .ghost:disabled { opacity: 0.45; cursor: default; }
  .ghost { background: #fff; border: 1px solid var(--line); }
  .ghost:hover:not(:disabled) { border-color: var(--ink); }
  .danger { color: var(--alert); }
  .error { flex: none; padding: 6px var(--s-panel); color: var(--alert); font-size: 12px; }
  .composer { flex: none; display: flex; flex-direction: column; gap: 8px; padding: var(--s-md) var(--s-panel) var(--s-panel); border-top: 1px solid var(--line); }
  .controls { display: flex; gap: 8px; flex-wrap: wrap; justify-content: flex-end; }
  textarea { width: 100%; resize: none; min-height: 52px; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--r-button); background: #fff; line-height: 1.45; }
  textarea:disabled { background: var(--strip); }
  .confirm { display: flex; flex-direction: column; gap: 8px; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--r-card); background: var(--panel); font-size: 12.5px; }
  .confirm div { display: flex; gap: 6px; justify-content: flex-end; }
  .readonly { flex: none; padding: var(--s-md) var(--s-panel) var(--s-panel); border-top: 1px solid var(--line); font-size: 12.5px; color: var(--mute); }
</style>
