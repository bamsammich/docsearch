<script lang="ts">
  import type { Setting } from "../gen/docsearch/account/v1/account_pb";
  import { accounts, problem } from "../lib/api";
  import Icon from "./Icon.svelte";

  let settings = $state<Setting[]>([]);
  let error = $state("");
  let loaded = $state(false);

  $effect(() => {
    accounts
      .signIn({})
      .then((out) => (settings = out.settings))
      .catch((err: unknown) => (error = problem(err)))
      .finally(() => (loaded = true));
  });
</script>

<div class="page-head">
  <h1>Sign-in</h1>
  <p>Change these in the server's configuration, then restart it.</p>
</div>

{#if error}<div class="banner" role="alert"><span>{error}</span></div>{/if}
{#if loaded && settings.length}
  <dl class="settings">
    {#each settings as s (s.label)}
      <div class="setting">
        <dt>{s.label}</dt>
        <dd>
          <span class="setting-value">{s.value || "Not set"}</span>
          {#if s.flag || s.env}
            <span class="setting-source"><Icon name="lock" />Set by <span class="mono">{s.flag}</span>{#if s.env}{" or "}<span class="mono">{s.env}</span>{/if}</span>
          {/if}
        </dd>
      </div>
    {/each}
  </dl>
{/if}
