<script lang="ts">
  import { queue } from "../lib/queue.svelte";
  import { router } from "../lib/router.svelte";
  import Icon from "./Icon.svelte";
</script>

<div class="empty panel">
  <h1>No documents yet</h1>
  <p>Add a documentation site or a file. It shows up here once ingest finishes.</p>
  {#if queue.active}
    <p><button class="link-btn" onclick={() => router.go("ingest")}>{queue.active === 1 ? "1 document is" : `${queue.active} documents are`} ingesting.</button></p>
  {/if}
  <p><button class="btn primary" onclick={() => router.go("ingest")}><Icon name="plus" /> Add document</button></p>
  <p class="muted">Agents can add documents too, with the <code class="mono">add_document</code> tool.</p>
</div>
