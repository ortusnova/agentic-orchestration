<script lang="ts">
  import { questionsLabel } from "../lib/questions";

  /** The open questions of the project, in its bar; nothing while there are none. */
  let { count, href }: { count: number; href: string } = $props();
</script>

{#if count > 0}
  <a class="questions" {href} data-questions={count} title="Agents wait on an answer: answer on the home">
    <i aria-hidden="true"></i>{questionsLabel(count)}
  </a>
{/if}

<style>
  .questions { display: inline-flex; align-items: center; gap: 7px; height: 26px; padding: 0 10px; border-radius: var(--r-pill); background: var(--amber); color: var(--ink); font-size: 12px; font-weight: 700; white-space: nowrap; text-decoration: none; }
  .questions:hover { background: #dda036; }
  .questions:focus-visible { outline: 2px solid #fff; outline-offset: 2px; }
  i { width: 7px; height: 7px; border-radius: 50%; background: var(--ink); }
</style>
