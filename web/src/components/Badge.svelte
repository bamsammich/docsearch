<script lang="ts">
  import { type Verdict, verdictWord } from "../lib/coverage";
  import Icon from "./Icon.svelte";

  let { verdict }: { verdict: Verdict } = $props();
  const icons = { good: "check", degraded: "warn", unusable: "stop" } as const;
</script>

<span class="badge v-{verdict}"><Icon name={icons[verdict]} />{verdictWord[verdict]}</span>
