<script lang="ts">
  import type { Setting } from "../gen/docsearch/account/v1/account_pb";
  import { accounts, problem } from "../lib/api";
  import Icon from "./Icon.svelte";

  let settings = $state<Setting[]>([]);
  let file = $state("");
  let error = $state("");
  let loaded = $state(false);

  $effect(() => {
    accounts
      .signIn({})
      .then((out) => {
        settings = out.settings;
        file = out.file;
      })
      .catch((err: unknown) => (error = problem(err)))
      .finally(() => (loaded = true));
  });

  const fileName = $derived(file.split("/").pop() || "docsearch.yaml");
</script>

<div class="page-head">
  <h1>Sign-in</h1>
  {#if file}
    <p><span class="mono">{file}</span> holds these settings, and the environment overrides it. Change them there, then restart docsearch.</p>
  {:else}
    <p>Change these in the server's environment or a docsearch.yaml, then restart docsearch.</p>
  {/if}
</div>

{#if error}<div class="banner" role="alert"><span>{error}</span></div>{/if}
{#if loaded && settings.length}
  <dl class="settings">
    {#each settings as s (s.label)}
      <div class="setting">
        <dt>{s.label}</dt>
        <dd>
          <span class="setting-value">{s.value || "Not set"}</span>
          {#if s.source === "file"}
            <span class="setting-source"><Icon name="lock" />From <span class="mono">{s.key}</span> in {fileName}</span>
          {:else if s.source === "environment"}
            <span class="setting-source"><Icon name="lock" />Set by <span class="mono">{s.where}</span> in the environment</span>
          {:else if s.source === "flag"}
            <span class="setting-source"><Icon name="lock" />Set by <span class="mono">{s.where}</span> on the command line</span>
          {:else if s.source === "default"}
            <span class="setting-source">Default · set it with <span class="mono">{s.key}</span> in docsearch.yaml or <span class="mono">{s.env}</span></span>
          {/if}
        </dd>
      </div>
    {/each}
  </dl>
{/if}
