<script lang="ts">
  import { renderMarkdown } from "../lib/markdown";
  import { stoppedAt } from "../lib/questions";
  import type { Question } from "../lib/types";

  let {
    question, story, canAnswer, onanswer,
  }: {
    question: Question;
    /** The story's plan key, which names the form. */
    story: string;
    /** Only an approver answers; the backend checks the role too. */
    canAnswer: boolean;
    /** Send the answer; resolves to an error message, or null. */
    onanswer: (text: string) => Promise<string | null>;
  } = $props();

  let text = $state("");
  let busy = $state(false);
  let error = $state("");
  let sent = $state(false);
  const when = $derived(stoppedAt(question.parked_at));

  async function send(e: SubmitEvent) {
    e.preventDefault();
    if (!text.trim() || busy) return;
    busy = true;
    error = "";
    const err = await onanswer(text.trim());
    busy = false;
    if (err) {
      error = err;
      return;
    }
    text = "";
    sent = true;
  }
</script>

<div class="question" data-question={story}>
  <div class="statement">
    {#if question.statement}{@html renderMarkdown(question.statement)}{:else}<p class="none">The agent stopped for a person without saying why.</p>{/if}
  </div>
  <p class="when">
    Stopped {#if when}<time datetime={when.iso}>{when.text}</time>{:else}at a time Sortie does not report{/if}{#if question.reason} · {question.reason}{/if}
  </p>
  {#if sent}
    <p class="sent" role="status">Answer sent. The story resumes once Sortie releases it.</p>
  {:else if canAnswer}
    <form onsubmit={send}>
      <label for="answer-{story}">Your answer</label>
      <textarea id="answer-{story}" rows="3" bind:value={text} disabled={busy} placeholder="Sortie posts it on the issue in your name and resumes the story."></textarea>
      <button type="submit" class="send" disabled={busy || !text.trim()}>{busy ? "Sending…" : "Send answer"}</button>
    </form>
  {:else}
    <p class="none" data-note="approver">Answering needs the approver role.</p>
  {/if}
  {#if error}<p class="err" role="alert">{error}</p>{/if}
</div>

<style>
  .question { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
  .statement { font-size: 12.5px; line-height: 1.5; color: var(--ink); overflow-wrap: anywhere; }
  .statement :global(p + p) { margin-top: 6px; }
  .statement :global(code) { font: 500 11.5px var(--font-mono); padding: 1px 4px; border-radius: var(--r-tag); background: var(--strip); }
  .statement :global(ul), .statement :global(ol) { margin: 4px 0 0 18px; }
  .when { font-size: 11.5px; color: var(--mute); }
  form { display: flex; flex-direction: column; gap: 6px; }
  label { font-size: 11px; font-weight: 700; letter-spacing: 0.06em; text-transform: uppercase; color: var(--mute); }
  textarea { width: 100%; min-height: 64px; padding: 7px 9px; border: 1px solid var(--line); border-radius: var(--r-card); background: #fff; color: var(--ink); font: inherit; font-size: 12.5px; resize: vertical; }
  textarea:focus-visible { outline: 2px solid var(--ink); outline-offset: 1px; }
  .send { align-self: flex-start; height: 28px; padding: 0 12px; border: 1px solid var(--ink); border-radius: 7px; background: var(--ink); color: #fff; font-size: 12px; font-weight: 700; }
  .send:hover:not(:disabled) { background: #000; }
  .send:disabled { opacity: 0.6; cursor: default; }
  .none { font-size: 12px; color: var(--mute); }
  .sent { font-size: 12px; color: var(--done); }
  .err { font-size: 11.5px; color: #8f241d; }
</style>
