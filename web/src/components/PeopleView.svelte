<script lang="ts">
  import { Code, ConnectError } from "@connectrpc/connect";
  import type { Account } from "../gen/docsearch/account/v1/account_pb";
  import { accounts, problem } from "../lib/api";
  import { admin } from "../lib/admin.svelte";
  import { initialOf } from "../lib/names";
  import { toast } from "../lib/toast.svelte";
  import Icon from "./Icon.svelte";

  let people = $state<Account[]>([]);
  let loaded = $state(false);
  let loadError = $state("");
  let find = $state("");

  let newName = $state("");
  let addError = $state("");
  let adding = $state(false);
  let nameField: HTMLInputElement | undefined = $state();

  /** A password shown once, and whose it is. */
  let issued = $state<{ username: string; password: string } | null>(null);
  let removing = $state("");
  let resetting = $state("");
  let rowError = $state<Record<string, string>>({});
  let busy = $state(false);
  /** The provider group administrators come from, when the server names one. */
  let adminGroup = $state("");

  const wanted = $derived(find.trim().toLowerCase());
  const shown = $derived(people.filter((p) => !wanted || p.username.includes(wanted)));
  const admins = $derived(people.filter((p) => p.admin).length);

  const day = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short", year: "numeric" });
  const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });
  const dateOf = (t?: { seconds: bigint }) => (t ? new Date(Number(t.seconds) * 1000) : null);
  const locked = (p: Account) => !!p.lockedUntil;

  async function load(): Promise<void> {
    try {
      const out = await accounts.list({});
      people = out.accounts;
      adminGroup = out.adminGroup;
      loadError = "";
    } catch (err) {
      loadError = problem(err);
    } finally {
      loaded = true;
    }
  }

  $effect(() => {
    void load();
  });

  function refusal(err: unknown, who: string): string {
    if (err instanceof ConnectError) {
      if (err.code === Code.AlreadyExists) return `An account named ${who} already exists.`;
      if (err.code === Code.InvalidArgument) return "A username takes 1 to 64 lower-case letters, digits, dots, hyphens or underscores.";
      if (err.code === Code.FailedPrecondition) return err.rawMessage.replace(/^./, (c) => c.toUpperCase()) + ".";
      if (err.code === Code.NotFound) return `No account named ${who}.`;
    }
    return problem(err);
  }

  async function add(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    addError = "";
    const who = newName.trim().toLowerCase();
    if (!who) {
      addError = "Type the username they will sign in with.";
      nameField?.focus();
      return;
    }
    adding = true;
    try {
      const { password } = await accounts.add({ username: who });
      issued = { username: who, password };
      newName = "";
      await load();
    } catch (err) {
      addError = refusal(err, who);
      nameField?.focus();
    } finally {
      adding = false;
    }
  }

  async function act(p: Account, run: () => Promise<unknown>, done: string): Promise<void> {
    document.getElementById(`acct-${p.username}`)?.hidePopover();
    busy = true;
    // A new action replaces whatever the last one left on any row.
    rowError = {};
    removing = "";
    resetting = "";
    try {
      await run();
      toast.show(done);
      // An administrator who gave up the role leaves Administration.
      if (admin.available) await load();
    } catch (err) {
      rowError = { ...rowError, [p.username]: refusal(err, p.username) };
    } finally {
      busy = false;
    }
  }

  const reset = (p: Account) =>
    act(p, async () => {
      const { password } = await accounts.reset({ username: p.username });
      issued = { username: p.username, password };
    }, `Reset the password for ${p.username}.`);
  const unlock = (p: Account) => act(p, () => accounts.unlock({ username: p.username }), `Unlocked ${p.username}.`);
  const promote = (p: Account) =>
    act(p, async () => {
      await accounts.setAdmin({ username: p.username, admin: !p.admin });
      if (p.username === admin.username) await admin.load();
    },
      p.admin ? `${p.username} is no longer an administrator.` : `${p.username} is now an administrator.`);
  const remove = (p: Account) =>
    act(p, async () => {
      await accounts.remove({ username: p.username });
      removing = "";
    }, `Removed ${p.username}.`);

  async function copy(): Promise<void> {
    if (!issued) return;
    try {
      await navigator.clipboard.writeText(issued.password);
      toast.show("Copied the password.");
    } catch {
      toast.show("Couldn't copy: select the password and copy it.");
    }
  }

  // Each row's menu opens under its button, right-aligned to it.
  function placing(event: ToggleEvent, button: HTMLElement | null): void {
    const menu = event.currentTarget as HTMLElement;
    if (event.newState !== "open" || !button) return;
    const at = button.getBoundingClientRect();
    menu.style.top = `${Math.round(at.bottom + 4)}px`;
    menu.style.left = `${Math.round(Math.max(12, at.right - 210))}px`;
  }

</script>

<div class="page-head"><h1>People</h1></div>

<div class="people">
  {#if loadError}<div class="banner" role="alert"><span>{loadError}</span></div>{/if}

  {#if admin.passwords}
    <form class="invite" onsubmit={add} novalidate>
      <label class="lbl" for="new-account">Add someone</label>
      <div class="invite-row">
        <input class="input" id="new-account" type="text" placeholder="Username" autocomplete="off" spellcheck="false"
          bind:value={newName} bind:this={nameField} aria-invalid={addError ? "true" : undefined}
          aria-describedby={addError ? "new-account-err" : undefined} />
        <button class="btn primary" type="submit" disabled={adding}>Add with a new password</button>
      </div>
      {#if addError}<p class="err" id="new-account-err" role="alert">{addError}</p>{/if}
    </form>
  {/if}

  {#if issued}
    <div class="issued" role="status">
      <span class="issued-what">Password for <strong>{issued.username}</strong>, shown once</span>
      <code class="mono">{issued.password}</code>
      <span class="row-btns">
        <button class="btn small" onclick={copy}><Icon name="copy" /> Copy</button>
        <button class="btn quiet small" onclick={() => (issued = null)}>Done</button>
      </span>
    </div>
  {/if}

  <div class="mlist-wrap">
    <div class="people-head">
      <h2 class="mlist-head">People{#if loaded}<span class="muted tnum">{` · ${people.length}`}</span>{/if}</h2>
      <label class="find">
        <span class="sr-only">Find someone</span><Icon name="search" />
        <input type="search" placeholder="Find someone" bind:value={find} />
      </label>
    </div>
    <ul class="mlist">
      {#each shown as p (p.username)}
        {@const self = p.username === admin.username}
        {@const since = dateOf(p.createdAt)}
        {@const until = dateOf(p.lockedUntil)}
        {@const canPromote = !adminGroup && !(p.admin && admins === 1)}
        <li class="mrow prow">
          <span class="mavatar" aria-hidden="true">{initialOf(p.username || "?")}</span>
          <span class="mwho">
            <span class="mname">{p.username || "(no username yet)"}{#if self}<span class="muted">{" · you"}</span>{/if}{#if locked(p)}<span class="tag">Locked</span>{/if}</span>
            {#if until}
              <span class="msince">Locked until {clock.format(until)}</span>
            {:else if since}
              <span class="msince">{admin.passwords ? "Added" : "First signed in"} on {day.format(since)}{#if p.admin && adminGroup}{` · administrator through ${adminGroup}`}{/if}</span>
            {/if}
            {#if rowError[p.username]}<span class="err" role="alert">{rowError[p.username]}</span>{/if}
          </span>
          <span class="prole">{#if p.admin}<span class="role-badge">Administrator</span>{/if}</span>
          <span class="mact">
            {#if removing === p.username}
              <button class="btn danger-solid small" onclick={() => remove(p)} disabled={busy}>Remove {p.username}</button>
              <button class="btn quiet small" onclick={() => (removing = "")}>Cancel</button>
            {:else if resetting === p.username}
              <button class="btn primary small" onclick={() => reset(p)} disabled={busy}>Reset {p.username}'s password</button>
              <button class="btn quiet small" onclick={() => (resetting = "")}>Cancel</button>
            {:else if p.username && (p.password || canPromote)}
              <button class="btn quiet small icon" id={`acct-btn-${p.username}`} popovertarget={`acct-${p.username}`}
                aria-label={`Actions for ${p.username}`} disabled={busy}>
                <svg class="i" viewBox="0 0 24 24" aria-hidden="true"><circle cx="5.5" cy="12" r="1.6" fill="currentColor" stroke="none"/><circle cx="12" cy="12" r="1.6" fill="currentColor" stroke="none"/><circle cx="18.5" cy="12" r="1.6" fill="currentColor" stroke="none"/></svg>
              </button>
              <div id={`acct-${p.username}`} popover class="row-menu" role="menu" aria-label={`Actions for ${p.username}`}
                onbeforetoggle={(e) => placing(e, document.getElementById(`acct-btn-${p.username}`))}>
                {#if p.password && locked(p)}<button role="menuitem" onclick={() => unlock(p)}>Unlock</button>{/if}
                {#if p.password}<button role="menuitem" onclick={() => { document.getElementById(`acct-${p.username}`)?.hidePopover(); removing = ""; resetting = p.username; }}>Reset password</button>{/if}
                {#if canPromote}
                  <button role="menuitem" onclick={() => promote(p)}>{p.admin ? "Remove administrator" : "Make administrator"}</button>
                {/if}
                {#if p.password && !self}
                  <hr />
                  <button role="menuitem" class="danger" onclick={() => { document.getElementById(`acct-${p.username}`)?.hidePopover(); resetting = ""; removing = p.username; }}>Remove from docsearch</button>
                {/if}
              </div>
            {/if}
          </span>
        </li>
      {/each}
      {#if loaded && !shown.length}<li class="mrow-none">{wanted ? `Nobody matches “${find.trim()}”.` : "No accounts yet."}</li>{/if}
    </ul>
    {#if admin.passwords}
      <p class="hint">Resetting a password or removing someone signs them out everywhere at once. A removed account's own library stays, and a shared library only they administer passes to you.</p>
    {:else}
      <p class="hint">Someone appears here the first time they sign in. To remove someone, remove them at your provider.</p>
    {/if}
    {#if adminGroup}
      <p class="hint">Administrators are the members of the provider group <span class="mono">{adminGroup}</span>, set at each sign-in. Change who administers at your provider.</p>
    {/if}
  </div>
</div>
