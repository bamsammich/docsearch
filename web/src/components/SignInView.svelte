<script lang="ts">
  import { Code, ConnectError } from "@connectrpc/connect";
  import type { Setting } from "../gen/docsearch/account/v1/account_pb";
  import { accounts, problem } from "../lib/api";
  import { toast } from "../lib/toast.svelte";
  import Icon from "./Icon.svelte";

  let settings = $state<Setting[]>([]);
  let file = $state("");
  let error = $state("");
  let loaded = $state(false);
  let draft = $state<Record<string, string>>({});
  let busy = $state(false);
  let updateError = $state("");
  /** docsearch.yaml with the changes made, and the lines they touched. */
  let updated = $state<{ text: string; changed: Set<number> } | null>(null);

  const fileName = $derived(file.split("/").pop() || "docsearch.yaml");
  // A setting docsearch.yaml holds changes in the file, and any other is
  // kept here, beneath the file.
  const inFile = (s: Setting) => s.source === "file";
  // What a field starts from: the value kept for the next start when that is
  // where it changes, and the running value otherwise.
  const baseline = (s: Setting) => (s.hasStored && !inFile(s) ? s.stored : s.value);
  const changed = $derived(
    settings.filter((s) => s.editable && draft[s.name] !== undefined && draft[s.name] !== baseline(s)),
  );
  const changes = $derived(changed.map((s) => ({ name: s.name, value: draft[s.name] ?? "" })));
  const touchesFile = $derived(changed.some(inFile));
  const touchesStore = $derived(changed.some((s) => !inFile(s)));
  const action = $derived(
    touchesFile && touchesStore ? `Save, and show ${fileName}`
      : touchesFile ? `Show ${fileName} with this change`
      : "Save",
  );

  async function load(): Promise<void> {
    try {
      const out = await accounts.signIn({});
      // Who creates groups is set on the Groups page.
      settings = out.settings.filter((s) => s.name !== "group-creators");
      file = out.file;
      draft = Object.fromEntries(settings.filter((s) => s.editable).map((s) => [s.name, baseline(s)]));
      error = "";
    } catch (err) {
      error = problem(err);
    } finally {
      loaded = true;
    }
  }

  $effect(() => {
    void load();
  });

  async function apply(): Promise<void> {
    updateError = "";
    busy = true;
    try {
      const out = await accounts.updateSignIn({ changes });
      updated = out.file ? { text: out.file, changed: new Set(out.changedLines) } : null;
      if (out.saved) {
        toast.show("Saved. The change takes effect when docsearch restarts.");
        await load();
      }
    } catch (err) {
      updateError = err instanceof ConnectError && err.code === Code.FailedPrecondition
        ? err.rawMessage.replace(/^./, (c) => c.toUpperCase()) + "."
        : problem(err);
    } finally {
      busy = false;
    }
  }

  function undo(): void {
    draft = Object.fromEntries(settings.filter((s) => s.editable).map((s) => [s.name, baseline(s)]));
    updated = null;
    updateError = "";
  }

  async function copy(): Promise<void> {
    if (!updated) return;
    try {
      await navigator.clipboard.writeText(updated.text);
      toast.show(`Copied ${fileName}.`);
    } catch {
      toast.show("Couldn't copy: select the file and copy it.");
    }
  }

  function download(): void {
    if (!updated) return;
    const url = URL.createObjectURL(new Blob([updated.text], { type: "application/yaml" }));
    const a = Object.assign(document.createElement("a"), { href: url, download: fileName });
    a.click();
    URL.revokeObjectURL(url);
  }

  const lines = $derived(updated ? updated.text.replace(/\n$/, "").split("\n") : []);
</script>

<div class="page-head">
  <h1>Sign-in</h1>
  {#if file}
    <p>A setting <span class="mono">{fileName}</span> holds changes in that file: copy the updated file into your deployment and redeploy. Any other change is saved here and takes effect when docsearch restarts.</p>
  {:else}
    <p>A change saved here takes effect when docsearch restarts. The environment and the command line override it.</p>
  {/if}
</div>

{#if error}<div class="banner" role="alert"><span>{error}</span></div>{/if}
{#if loaded && settings.length}
  <div class="signin-edit">
    <section class="signin-main">
      <dl class="settings">
        {#each settings as s (s.label)}
          <div class="setting">
            <dt>{#if s.editable && !s.boolean}<label for={`set-${s.name}`}>{s.label}</label>{:else}{s.label}{/if}</dt>
            <dd>
              {#if s.editable && s.boolean}
                <label class="setting-check">
                  <input type="checkbox" checked={draft[s.name] === "true"}
                    onchange={(e) => { draft[s.name] = e.currentTarget.checked ? "true" : "false"; updated = null; }} />
                  <span>{draft[s.name] === "true" ? "Yes" : "No"}</span>
                </label>
              {:else if s.editable}
                <input class="input mono" id={`set-${s.name}`} type="text" autocomplete="off" spellcheck="false"
                  bind:value={draft[s.name]} oninput={() => (updated = null)} />
              {:else}
                <span class="setting-value">{s.value || "Not set"}</span>
              {/if}
              {#if s.hasStored && s.stored !== s.value && (s.source === "default" || s.source === "database")}
                <span class="setting-source">Saved · takes effect when docsearch restarts</span>
              {/if}
              {#if s.source === "file"}
                <span class="setting-source">From <span class="mono">{s.key}</span> in {fileName}</span>
              {:else if s.source === "database"}
                <span class="setting-source">Saved here</span>
              {:else if s.source === "environment"}
                <span class="setting-source"><Icon name="lock" />Set by <span class="mono">{s.where}</span> in the environment</span>
              {:else if s.source === "flag"}
                <span class="setting-source"><Icon name="lock" />Set by <span class="mono">{s.where}</span> on the command line</span>
              {:else if s.source === "default" && !s.editable}
                <span class="setting-source">Default · set it with <span class="mono">{s.key}</span> in docsearch.yaml or <span class="mono">{s.env}</span></span>
              {/if}
            </dd>
          </div>
        {/each}
      </dl>
      {#if settings.some((s) => s.editable)}
        <div class="row-btns signin-actions">
          <button class="btn primary" onclick={apply} disabled={busy || !changes.length}>{action}</button>
          <button class="btn quiet" onclick={undo} disabled={busy || (!changes.length && !updated)}>Undo changes</button>
        </div>
        {#if updateError}<p class="err" role="alert">{updateError}</p>{/if}
      {/if}
    </section>

    {#if updated}
      <aside class="yaml-card" aria-labelledby="yaml-title">
        <div class="yaml-head">
          <Icon name="read" />
          <h2 id="yaml-title" class="mono">{fileName}</h2>
          <span class="yaml-count">{updated.changed.size === 1 ? "1 change" : `${updated.changed.size} changes`}</span>
          <span class="spacer"></span>
          <button class="btn quiet small icon on-dark" onclick={copy} aria-label="Copy file" title="Copy file"><Icon name="copy" /></button>
          <button class="btn quiet small icon on-dark" onclick={download} aria-label="Download file" title="Download file"><Icon name="download" /></button>
        </div>
        <ol class="yaml-lines mono">
          {#each lines as line, i (i)}
            <li class:changed={updated.changed.has(i + 1)}><span class="yaml-n">{i + 1}</span><span class="yaml-text">{line}</span></li>
          {/each}
        </ol>
      </aside>
    {/if}
  </div>
{/if}
