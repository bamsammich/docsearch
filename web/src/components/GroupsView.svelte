<script lang="ts">
  import { Code, ConnectError } from "@connectrpc/connect";
  import type { Invitation } from "../gen/docsearch/group/v1/group_pb";
  import { problem } from "../lib/api";
  import { groupsStore as groups } from "../lib/groups.svelte";
  import { slugOf } from "../lib/names";
  import { router } from "../lib/router.svelte";
  import { toast } from "../lib/toast.svelte";
  import Icon from "./Icon.svelte";

  let creating = $state(false);
  let name = $state("");
  let error = $state("");
  let busy = $state(false);
  let answering = $state("");
  let nameField: HTMLInputElement | undefined = $state();

  const others = $derived(groups.all.filter((g) => g.role === ""));
  const roleWords: Record<string, string> = { maintainer: "Maintainer", member: "Member" };
  const count = (n: bigint) => (n === 1n ? "1 member" : `${n} members`);

  $effect(() => {
    void groups.load();
  });

  function startCreating(): void {
    creating = true;
    error = "";
    queueMicrotask(() => nameField?.focus());
  }

  async function create(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    error = "";
    const title = name.trim();
    const slug = slugOf(title);
    if (!title) {
      error = "Type a name for the group.";
      return;
    }
    if (!slug) {
      error = "The name needs a letter or a digit.";
      return;
    }
    busy = true;
    try {
      await groups.create(slug, title);
      toast.show(`Created ${title}. You maintain it.`);
      creating = false;
      name = "";
      router.go("groups", slug);
    } catch (err) {
      error = err instanceof ConnectError && err.code === Code.AlreadyExists
        ? `Another group is already called ${slug}.`
        : problem(err);
    } finally {
      busy = false;
    }
  }

  async function answer(i: Invitation, accept: boolean): Promise<void> {
    answering = i.group;
    try {
      if (accept) {
        await groups.accept(i.group);
        toast.show(`Joined ${i.name}.`);
      } else {
        await groups.decline(i.group);
        toast.show(`Declined ${i.name}.`);
      }
    } catch (err) {
      toast.show(problem(err));
      void groups.load();
    } finally {
      answering = "";
    }
  }
</script>

<div class="page-head">
  <h1>Groups</h1>
  {#if groups.canCreate && !creating}
    <button class="btn primary" onclick={startCreating}><Icon name="plus" /> New group</button>
  {/if}
</div>

{#if groups.error}<div class="banner" role="alert"><span>{groups.error}</span></div>{/if}

<div class="groups">
  {#if creating}
    <form class="invite" onsubmit={create} novalidate>
      <label class="lbl" for="group-name">New group</label>
      <div class="invite-row">
        <input class="input" id="group-name" type="text" maxlength="120" placeholder="Platform team" autocomplete="off"
          bind:value={name} bind:this={nameField} oninput={() => (error = "")} aria-invalid={error ? "true" : undefined} />
        <button class="btn primary" type="submit" disabled={busy}>Create group</button>
        <button class="btn quiet" type="button" onclick={() => (creating = false)}>Cancel</button>
      </div>
      {#if name.trim() && slugOf(name)}<p class="hint">Libraries name it <span class="mono">{slugOf(name)}</span></p>{/if}
      {#if error}<p class="err" role="alert">{error}</p>{/if}
    </form>
  {/if}

  {#if groups.invitations.length}
    <section class="mlist-wrap" aria-labelledby="ginv-title">
      <h2 id="ginv-title" class="mlist-head">Invitations</h2>
      <ul class="mlist">
        {#each groups.invitations as i (i.group)}
          <li class="mrow grow-invite">
            <span class="gtile" aria-hidden="true"><Icon name="group" /></span>
            <span class="mwho">
              <span class="mname">{i.name}</span>
              <span class="msince">{i.invitedBy ? `${i.invitedBy} invited you as ${i.role}` : `Invited as ${i.role}`} · Its libraries reach your search once you accept</span>
            </span>
            <span class="mact">
              <button class="btn primary small" onclick={() => answer(i, true)} disabled={answering !== ""}>Accept</button>
              <button class="btn quiet small" onclick={() => answer(i, false)} disabled={answering !== ""}>Decline</button>
            </span>
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  <section class="mlist-wrap" aria-labelledby="gmine-title">
    <h2 id="gmine-title" class="mlist-head">Your groups{#if groups.loaded}<span class="muted tnum">{` · ${groups.mine.length}`}</span>{/if}</h2>
    {#if groups.loaded && !groups.mine.length}
      <p class="mrow-none">You're in no group yet.</p>
    {:else}
      <ul class="mlist">
        {#each groups.mine as g (g.group)}
          <li><a class="mrow grow-link" href={`#/groups/${encodeURIComponent(g.group)}`}>
            <span class="gtile" aria-hidden="true"><Icon name="group" /></span>
            <span class="mwho"><span class="mname">{g.name}</span><span class="msince mono">{g.group}</span></span>
            <span class="mrole-text">{roleWords[g.role] ?? g.role}</span>
            <span class="msince tnum">{count(g.members)}</span>
          </a></li>
        {/each}
      </ul>
    {/if}
  </section>

  {#if others.length}
    <section class="mlist-wrap" aria-labelledby="gother-title">
      <h2 id="gother-title" class="mlist-head">Other groups<span class="muted tnum">{` · ${others.length}`}</span></h2>
      <ul class="mlist">
        {#each others as g (g.group)}
          <li class="mrow">
            <span class="gtile muted-tile" aria-hidden="true"><Icon name="group" /></span>
            <span class="mwho"><span class="mname">{g.name}</span><span class="msince mono">{g.group}</span></span>
            <span></span>
            <span class="msince tnum">{count(g.members)}</span>
          </li>
        {/each}
      </ul>
      <p class="hint">A group's maintainers invite people. Ask one of them to join.</p>
    </section>
  {/if}
</div>
