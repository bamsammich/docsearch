<script lang="ts">
  import { codeSpans } from "../lib/findings";

  // The server's own sentences, with its `backtick` identifiers set as code.
  let { text }: { text: string } = $props();
</script>

{#each codeSpans(text) as part, i (i)}{#if part.code}<code class="mono">{part.text}</code>{:else}{part.text}{/if}{/each}
