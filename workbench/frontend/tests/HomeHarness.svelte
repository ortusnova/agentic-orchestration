<script lang="ts">
  import HomePage from "../src/components/HomePage.svelte";
  import type { MergedGraph } from "../src/lib/types";

  let {
    initial, onapprove = async () => null, onanswer = async () => null, onreplace, onsessions, onseen,
  }: {
    initial: MergedGraph;
    onapprove?: (keys: string[]) => Promise<string | null>;
    onanswer?: (key: string, text: string) => Promise<string | null>;
    onreplace?: (replacement: string) => Promise<string | null>;
    onsessions?: (key: string, sessions: number) => Promise<string | null>;
    onseen?: (ids: number[]) => Promise<string | null>;
  } = $props();
  // svelte-ignore state_referenced_locally
  let doc = $state(initial);
  let marks: Set<string> = $state(new Set());

  export function setDoc(next: MergedGraph) { doc = next; }
  export function mark(keys: string[]) { marks = new Set(keys); }
</script>

<div style="display:flex;flex-direction:column;height:900px">
  <HomePage {doc} slug="homelab" projectName="Homelab" {marks} {onapprove} {onanswer} {onreplace} {onsessions} {onseen}
    tabs={[{ label: "Home", href: "#/p/homelab", current: true }, { label: "Graph", href: "#/p/homelab/graph", current: false }]} />
</div>
