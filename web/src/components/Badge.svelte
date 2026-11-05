<script lang="ts">
  import { type Verdict, verdictWord } from "../lib/coverage";
  import Icon from "./Icon.svelte";

  // Plain drops the tint, for a column of verdicts where tinted chips on
  // every row would weigh healthy documents as much as broken ones.
  let { verdict, plain = false }: { verdict: Verdict; plain?: boolean } = $props();
  const icons = { good: "check", degraded: "warn", unusable: "stop" } as const;
</script>

<span class="badge v-{verdict}" class:plain><Icon name={icons[verdict]} />{verdictWord[verdict]}</span>
