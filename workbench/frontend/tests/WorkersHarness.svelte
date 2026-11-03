<script lang="ts">
  import WorkersPage from "../src/components/WorkersPage.svelte";
  import type { Fleet } from "../src/lib/fleet";

  let { initial }: { initial: Fleet } = $props();
  // svelte-ignore state_referenced_locally
  let fleet = $state(initial);
  export function setFleet(next: Fleet) { fleet = next; }
</script>

<WorkersPage {fleet} slug="homelab" />
