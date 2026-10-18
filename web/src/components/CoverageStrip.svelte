<script lang="ts">
  import { type Coverage, total } from "../lib/coverage";

  let { coverage, thin = false, keyed = false }: { coverage: Coverage; thin?: boolean; keyed?: boolean } = $props();
  const n = new Intl.NumberFormat("en-US");
  const label = $derived(
    `${n.format(coverage.byChapter)} ${coverage.unit} reachable by chapter, ` +
      `${n.format(coverage.unreliable)} reachable with unreliable chapters, ` +
      `${n.format(coverage.lost)} not indexed, of ${n.format(total(coverage))}`,
  );
</script>

<div class="cov" class:thin role="img" aria-label={label}>
  {#if coverage.byChapter}<i class="c-ok" style:flex={coverage.byChapter}></i>{/if}
  {#if coverage.unreliable}<i class="c-part" style:flex={coverage.unreliable}></i>{/if}
  {#if coverage.lost}<i class="c-lost" style:flex={coverage.lost}></i>{/if}
</div>
{#if keyed}
  <div class="cov-key tnum" aria-hidden="true">
    {#if coverage.byChapter}<span><i class="c-ok"></i>{n.format(coverage.byChapter)} by chapter</span>{/if}
    {#if coverage.unreliable}<span><i class="c-part"></i>{n.format(coverage.unreliable)} chapters unreliable</span>{/if}
    {#if coverage.lost}<span><i class="c-lost"></i>{n.format(coverage.lost)} not indexed</span>{/if}
  </div>
{/if}
