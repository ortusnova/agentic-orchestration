<script lang="ts">
  import SyncStatus from "../src/components/SyncStatus.svelte";
  import type { SyncStatus as Status } from "../src/lib/types";
  let { status, onretry }: { status: Status | null; onretry: () => Promise<unknown> } = $props();
  // svelte-ignore state_referenced_locally
  let current = $state(status);
  export function update(next: Status | null) { current = next; }
</script>

<SyncStatus status={current} {onretry} />
