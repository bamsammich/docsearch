<script lang="ts">
  import type { Document } from "../gen/docsearch/document/v1/document_pb";
  import { SourceKind } from "../gen/docsearch/type/v1/common_pb";
  import { problem } from "../lib/api";
  import { downloadOriginal, openOriginal } from "../lib/original";
  import { toast } from "../lib/toast.svelte";
  import Icon from "./Icon.svelte";

  let { doc, small = false }: { doc: Document; small?: boolean } = $props();

  let opening = $state(false);

  async function open() {
    opening = true;
    try {
      // A browser can't show a Word file, so it downloads instead.
      if (doc.format === "docx") await downloadOriginal(doc.docId, `${doc.title}.docx`);
      else await openOriginal(doc.docId);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : problem(err));
    } finally {
      opening = false;
    }
  }
</script>

{#if doc.sourceKind === SourceKind.SITE}
  <a class="btn" class:small href={doc.source} target="_blank" rel="noopener noreferrer"><Icon name="external" /> Original</a>
{:else}
  <button class="btn" class:small onclick={open} disabled={opening}>
    <Icon name="external" /> {opening ? "Opening…" : "Original"}
  </button>
{/if}
