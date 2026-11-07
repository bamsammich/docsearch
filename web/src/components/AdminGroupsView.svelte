<script lang="ts">
  import { Code, ConnectError } from "@connectrpc/connect";
  import type { Setting } from "../gen/docsearch/account/v1/account_pb";
  import type { Member } from "../gen/docsearch/group/v1/group_pb";
  import { accounts, groups as service, problem } from "../lib/api";
  import { groupsStore as groups } from "../lib/groups.svelte";
  import { toast } from "../lib/toast.svelte";
  import Icon from "./Icon.svelte";

  const name = "group-creators";
  const choices = [
    { value: "everyone", label: "Everyone signed in" },
    { value: "administrators", label: "Administrators only" },
  ];

  let setting = $state<Setting | null>(null);
  let file = $state("");
  let draft = $state("");
  let settingError = $state("");
  let busy = $state(false);
  let maintainers = $state<Record<string, string[]>>({});
  let deleting = $state("");
  let rowError = $state<Record<string, string>>({});

  const fileName = $derived(file.split("/").pop() || "docsearch.yaml");
  // The value the next start reads: what is saved here, else what runs.
  const baseline = $derived(setting ? (setting.hasStored && setting.source !== "file" ? setting.stored : setting.value) : "");
  const editable = $derived(!!setting?.editable && setting.source !== "file");

  async function loadSetting(): Promise<void> {
    try {
      const out = await accounts.signIn({});
      setting = out.settings.find((s) => s.name === name) ?? null;
      file = out.file;
      draft = baseline;
      settingError = "";
    } catch (err) {
      settingError = problem(err);
    }
  }

  // An administrator sees who maintains each group, and which have none.
  async function loadMaintainers(): Promise<void> {
    const found = await Promise.all(
      groups.all.map(async (g) => {
        try {
          const { members } = await service.members({ group: g.group });
          return [g.group, members.filter((m: Member) => !m.invited && m.role === "maintainer").map((m) => m.username)] as const;
        } catch {
          return [g.group, [] as string[]] as const;
        }
      }),
    );
    maintainers = Object.fromEntries(found);
  }

  $effect(() => {
    void loadSetting();
    void groups.load().then(loadMaintainers);
  });

  async function save(): Promise<void> {
    settingError = "";
    busy = true;
    try {
      await accounts.updateSignIn({ changes: [{ name, value: draft }] });
      toast.show("Saved. The change takes effect when docsearch restarts.");
      await loadSetting();
    } catch (err) {
      settingError = err instanceof ConnectError && err.code === Code.FailedPrecondition
        ? err.rawMessage.replace(/^./, (c) => c.toUpperCase()) + "."
        : problem(err);
    } finally {
      busy = false;
    }
  }

  async function deleteGroup(slug: string, label: string): Promise<void> {
    busy = true;
    try {
      await service.delete({ group: slug });
      toast.show(`Deleted ${label}. Its libraries stay with their other members.`);
      deleting = "";
      await groups.load();
      await loadMaintainers();
    } catch (err) {
      rowError = { [slug]: problem(err) };
    } finally {
      busy = false;
    }
  }

  const running = $derived(choices.find((c) => c.value === setting?.value)?.label ?? setting?.value ?? "");
</script>

<div class="page-head">
  <h1>Groups</h1>
</div>

<div class="groups">
  {#if setting}
    <section class="invite" aria-labelledby="creators-title">
      <h2 id="creators-title" class="lbl">Who can create groups</h2>
      {#if editable}
        <div class="choice-list" role="radiogroup" aria-labelledby="creators-title">
          {#each choices as c (c.value)}
            <label class="setting-check"><input type="radio" name="creators" value={c.value} bind:group={draft} />{c.label}</label>
          {/each}
        </div>
        <div class="row-btns">
          <button class="btn primary" onclick={save} disabled={busy || draft === baseline}>Save</button>
        </div>
        <p class="hint">
          {#if setting.hasStored && setting.stored !== setting.value}Saved · takes effect when docsearch restarts.{:else}A change takes effect when docsearch restarts.{/if}
          Groups that exist stay.
        </p>
      {:else}
        <p class="setting-value">{running}</p>
        {#if setting.source === "file"}
          <p class="hint">From <span class="mono">{setting.key}</span> in {fileName}. Change it there and redeploy.</p>
        {:else if setting.source === "environment"}
          <p class="hint"><Icon name="lock" />Set by <span class="mono">{setting.where}</span> in the environment.</p>
        {:else if setting.source === "flag"}
          <p class="hint"><Icon name="lock" />Set by <span class="mono">{setting.where}</span> on the command line.</p>
        {/if}
      {/if}
      {#if settingError}<p class="err" role="alert">{settingError}</p>{/if}
    </section>
  {:else if settingError}
    <div class="banner" role="alert"><span>{settingError}</span></div>
  {/if}

  {#if groups.error}<div class="banner" role="alert"><span>{groups.error}</span></div>{/if}
  {#if groups.loaded}
    <div class="mlist-wrap">
      <h2 class="mlist-head">Every group<span class="muted tnum">{` · ${groups.all.length}`}</span></h2>
      {#if !groups.all.length}
        <p class="mrow-none">No groups yet.</p>
      {:else}
        <ul class="mlist">
          {#each groups.all as g (g.group)}
            {@const kept = maintainers[g.group]}
            <li class="mrow">
              <span class="gtile" class:muted-tile={kept && !kept.length} aria-hidden="true"><Icon name="group" /></span>
              <span class="mwho">
                <span class="mname"><a href={`#/groups/${encodeURIComponent(g.group)}`}>{g.name}</a></span>
                {#if kept}<span class="msince">{kept.length ? `Maintained by ${kept.join(", ")}` : "No maintainer · administrators manage it"}</span>{/if}
                {#if rowError[g.group]}<span class="err" role="alert">{rowError[g.group]}</span>{/if}
              </span>
              <span class="mrole-text tnum">{g.members === 1n ? "1 member" : `${g.members} members`}</span>
              <span class="mact">
                {#if deleting === g.group}
                  <button class="btn danger-solid small" onclick={() => deleteGroup(g.group, g.name)} disabled={busy}>Delete {g.name}</button>
                  <button class="btn quiet small" onclick={() => (deleting = "")}>Cancel</button>
                {:else}
                  <button class="btn danger small" onclick={() => (deleting = g.group)} disabled={busy}>Delete</button>
                {/if}
              </span>
            </li>
          {/each}
        </ul>
      {/if}
    </div>
  {/if}
</div>
