<script lang="ts">
  import { Code, ConnectError } from "@connectrpc/connect";
  import type { Event, Library, Member } from "../gen/docsearch/group/v1/group_pb";
  import { groups as service, problem, shelves } from "../lib/api";
  import { admin } from "../lib/admin.svelte";
  import { groupsStore as groups } from "../lib/groups.svelte";
  import { initialOf } from "../lib/names";
  import { router } from "../lib/router.svelte";
  import { toast } from "../lib/toast.svelte";

  const roles = ["member", "maintainer"] as const;
  const roleWords: Record<string, string> = { member: "Member", maintainer: "Maintainer" };
  const libRoleWords: Record<string, string> = { reader: "Reader", editor: "Editor", admin: "Admin" };

  let members = $state<Member[]>([]);
  let libraries = $state<Library[]>([]);
  let events = $state<Event[]>([]);
  let loadError = $state("");
  let loaded = $state(false);

  let invitee = $state("");
  let inviteRole = $state<string>("member");
  let inviteError = $state("");
  let rowError = $state<Record<string, string>>({});
  let removing = $state("");
  let leaving = $state(false);
  let deleting = $state(false);
  let busy = $state(false);

  const slug = $derived(router.docId);
  const group = $derived(groups.byId(slug));
  const me = $derived(admin.username);
  const joined = $derived(members.filter((m) => !m.invited));
  const invited = $derived(members.filter((m) => m.invited));
  const maintainers = $derived(joined.filter((m) => m.role === "maintainer").length);
  const maintains = $derived(group?.role === "maintainer" || admin.isAdmin);
  const onlyMaintainer = $derived(group?.role === "maintainer" && maintainers <= 1);
  const creator = $derived(events.find((e) => e.action === "create")?.actor ?? "");

  async function load(of: string): Promise<void> {
    loadError = "";
    try {
      const [m, l] = await Promise.all([service.members({ group: of }), service.libraries({ group: of })]);
      const logged = maintains ? await service.events({ group: of }) : { events: [] as Event[] };
      if (of !== router.docId) return;
      members = m.members;
      libraries = l.libraries;
      events = logged.events;
    } catch (err) {
      loadError = err instanceof ConnectError && err.code === Code.NotFound
        ? "You're not in this group. Its maintainers invite people."
        : problem(err);
    } finally {
      loaded = true;
    }
  }

  $effect(() => {
    const of = slug;
    if (!of || !groups.loaded) return;
    loaded = false;
    rowError = {};
    removing = "";
    leaving = false;
    deleting = false;
    void load(of);
  });

  const day = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" });
  const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });
  const whenOf = (e: Event) => {
    const at = e.at ? new Date(Number(e.at.seconds) * 1000) : null;
    if (!at) return "";
    return at.toDateString() === new Date().toDateString() ? `Today ${clock.format(at)}` : `${day.format(at)} ${clock.format(at)}`;
  };
  const dayOf = (e: Event) => {
    const at = e.at ? new Date(Number(e.at.seconds) * 1000) : null;
    if (!at) return "";
    return at.toDateString() === new Date().toDateString() ? "today" : `on ${day.format(at)}`;
  };
  const article = (role: string) => (role === "member" ? "a member" : "a maintainer");

  // The log, newest first, and who added each member.
  const story = $derived.by(() => {
    const lines: { what: string; when: string }[] = [];
    const since = new Map<string, string>();
    const inviter = new Map<string, string>();
    for (const e of events) {
      const actor = e.actor || "Someone";
      const subject = e.subject || "someone";
      switch (e.action) {
        case "create":
          lines.push({ what: `${actor} created ${group?.name ?? slug}`, when: whenOf(e) });
          since.set(e.actor, `Created the group ${dayOf(e)}`);
          break;
        case "rename":
          lines.push({ what: e.detail ? `${actor} renamed it from ${e.detail}` : `${actor} renamed it`, when: whenOf(e) });
          break;
        case "invite":
          lines.push({ what: `${actor} invited ${subject} as ${e.role}`, when: whenOf(e) });
          inviter.set(e.subject, actor);
          since.set(e.subject, `Invited by ${actor} ${dayOf(e)}`);
          break;
        case "accept":
          lines.push({ what: `${subject} joined as ${e.role}`, when: whenOf(e) });
          since.set(e.subject, `Added by ${inviter.get(e.subject) ?? "a maintainer"} ${dayOf(e)}`);
          break;
        case "decline":
          lines.push({ what: `${subject} declined`, when: whenOf(e) });
          since.delete(e.subject);
          break;
        case "withdraw":
          lines.push({ what: `${actor} withdrew ${subject}'s invitation`, when: whenOf(e) });
          since.delete(e.subject);
          break;
        case "role":
          lines.push({ what: `${actor} made ${subject} ${article(e.role)}`, when: whenOf(e) });
          break;
        case "remove":
          lines.push({ what: e.actor && e.actor === e.subject ? `${subject} left` : `${actor} removed ${subject}`, when: whenOf(e) });
          since.delete(e.subject);
          break;
      }
    }
    return { lines: lines.reverse(), since };
  });

  function refusal(err: unknown, who: string): string {
    if (err instanceof ConnectError) {
      if (err.code === Code.NotFound && /account/.test(err.rawMessage))
        return admin.passwords
          ? `No account named ${who}. An administrator adds accounts on the People page.`
          : `No account named ${who} yet. They need to sign in once before you can invite them.`;
      if (err.code === Code.FailedPrecondition && /maintainer/.test(err.rawMessage))
        return "That would leave the group without a maintainer. Make someone else a maintainer first.";
      if (err.code === Code.FailedPrecondition) return `${who} is in this group through the provider. Remove them there.`;
    }
    return problem(err);
  }

  async function invite(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    inviteError = "";
    const who = invitee.trim().toLowerCase();
    if (!who) {
      inviteError = "Type the username they sign in with.";
      return;
    }
    const known = members.find((m) => m.username === who);
    if (known) {
      inviteError = known.invited ? `${who} is already invited. Change the role in the list.` : `${who} is already in the group. Change their role in the list.`;
      return;
    }
    busy = true;
    try {
      const { invited: asked } = await service.invite({ group: slug, username: who, role: inviteRole });
      toast.show(asked ? `Invited ${who} as ${inviteRole}. They join once they accept.` : `${who} is now ${article(inviteRole)}.`);
      invitee = "";
      await load(slug);
    } catch (err) {
      inviteError = refusal(err, who);
    } finally {
      busy = false;
    }
  }

  async function changeRole(m: Member, role: string): Promise<void> {
    rowError = {};
    busy = true;
    try {
      await service.invite({ group: slug, username: m.username, role });
      toast.show(m.invited ? `${m.username}'s invitation is now for ${role}.` : `${m.username} is now ${article(role)}.`);
      await Promise.all([load(slug), groups.load()]);
    } catch (err) {
      rowError = { [m.username]: refusal(err, m.username) };
      await load(slug);
    } finally {
      busy = false;
    }
  }

  async function remove(m: Member): Promise<void> {
    busy = true;
    try {
      await service.remove({ group: slug, username: m.username });
      toast.show(m.invited ? `Withdrew ${m.username}'s invitation.` : `Removed ${m.username}.`);
      removing = "";
      await Promise.all([load(slug), groups.load()]);
    } catch (err) {
      rowError = { [m.username]: refusal(err, m.username) };
    } finally {
      busy = false;
    }
  }

  async function unshare(l: Library): Promise<void> {
    busy = true;
    try {
      await shelves.revokeGroup({ library: l.library, group: slug });
      toast.show(`${l.name || l.library} is no longer shared with ${group?.name ?? slug}.`);
      removing = "";
      await load(slug);
    } catch (err) {
      rowError = { [`library:${l.library}`]: problem(err) };
    } finally {
      busy = false;
    }
  }

  async function leave(): Promise<void> {
    busy = true;
    try {
      await service.remove({ group: slug, username: me });
      toast.show(`You left ${group?.name ?? slug}.`);
      await groups.load();
      router.go("groups");
    } catch (err) {
      leaving = false;
      loadError = refusal(err, me);
    } finally {
      busy = false;
    }
  }

  async function deleteGroup(): Promise<void> {
    const name = group?.name ?? slug;
    busy = true;
    try {
      await service.delete({ group: slug });
      toast.show(`Deleted ${name}. Its libraries stay with their other members.`);
      await groups.load();
      router.go(admin.isAdmin && group?.role !== "maintainer" ? "allgroups" : "groups");
    } catch (err) {
      deleting = false;
      loadError = problem(err);
    } finally {
      busy = false;
    }
  }
</script>

<div class="page-head">
  <div>
    <a class="crumb" href="#/groups">Groups</a>
    <h1>{group?.name ?? slug}</h1>
    {#if group}<p>Libraries name it <span class="mono">{slug}</span>{#if creator}{` · created by ${creator}`}{/if}</p>{/if}
  </div>
</div>

{#if groups.loaded && !group}
  <section class="members-solo"><p>No group named <span class="mono">{slug}</span>. <a href="#/groups">See every group.</a></p></section>
{:else}
  <div class="members">
    <section class="members-main" aria-label="Members">
      {#if loadError}<div class="banner" role="alert"><span>{loadError}</span></div>{/if}

      {#if maintains}
        <form class="invite" onsubmit={invite} novalidate>
          <label class="lbl" for="ginvite-user">Invite someone</label>
          <div class="invite-row">
            <input class="input" id="ginvite-user" type="text" placeholder="Username" autocomplete="off" spellcheck="false"
              bind:value={invitee} oninput={() => (inviteError = "")} aria-invalid={inviteError ? "true" : undefined} />
            <label class="role-pick"><span>as</span>
              <select aria-label="Group role" bind:value={inviteRole}>
                {#each roles as r (r)}<option value={r}>{roleWords[r]}</option>{/each}
              </select>
            </label>
            <button class="btn primary" type="submit" disabled={busy}>Invite</button>
          </div>
          {#if inviteError}<p class="err" role="alert">{inviteError}</p>{/if}
          <p class="hint">Maintainers also invite and remove people, and share libraries they administer with the group.</p>
        </form>
      {/if}

      <div class="mlist-wrap">
        <h2 class="mlist-head">Members{#if loaded}<span class="muted tnum">{` · ${joined.length}`}</span>{/if}</h2>
        <ul class="mlist">
          {#each joined as m (m.username)}
            {@const self = m.username === me}
            {@const since = story.since.get(m.username)}
            <li class="mrow">
              <span class="mavatar" aria-hidden="true">{initialOf(m.username || "?")}</span>
              <span class="mwho">
                <span class="mname">{m.username || "(no username yet)"}{#if self}<span class="muted">{" · you"}</span>{/if}{#if m.fromProvider}<span class="tag">From the provider</span>{/if}{#if m.role === "maintainer" && maintainers === 1}<span class="tag">Only maintainer</span>{/if}</span>
                {#if since}<span class="msince">{since}</span>{/if}
                {#if rowError[m.username]}<span class="err" role="alert">{rowError[m.username]}</span>{/if}
              </span>
              {#if maintains && !(self && onlyMaintainer)}
                <select class="mrole" aria-label={`Group role for ${m.username}`} value={m.role} disabled={busy}
                  onchange={(e) => changeRole(m, e.currentTarget.value)}>
                  {#each roles as r (r)}<option value={r}>{roleWords[r]}</option>{/each}
                </select>
              {:else}
                <span class="mrole-text">{roleWords[m.role] ?? m.role}</span>
              {/if}
              <span class="mact">
                {#if maintains && !self && !m.fromProvider}
                  {#if removing === m.username}
                    <button class="btn danger-solid small" onclick={() => remove(m)} disabled={busy}>Remove {m.username}</button>
                    <button class="btn quiet small" onclick={() => (removing = "")}>Cancel</button>
                  {:else}
                    <button class="btn danger small" onclick={() => (removing = m.username)} disabled={busy}>Remove</button>
                  {/if}
                {/if}
              </span>
            </li>
          {/each}
        </ul>
      </div>

      {#if invited.length}
        <div class="mlist-wrap">
          <h2 class="mlist-head">Invited<span class="muted tnum">{` · ${invited.length}`}</span></h2>
          <ul class="mlist">
            {#each invited as m (m.username)}
              {@const since = story.since.get(m.username)}
              <li class="mrow">
                <span class="mavatar" aria-hidden="true">{initialOf(m.username || "?")}</span>
                <span class="mwho">
                  <span class="mname">{m.username}</span>
                  {#if since}<span class="msince">{since}</span>{/if}
                  {#if rowError[m.username]}<span class="err" role="alert">{rowError[m.username]}</span>{/if}
                </span>
                <select class="mrole" aria-label={`Role offered to ${m.username}`} value={m.role} disabled={busy}
                  onchange={(e) => changeRole(m, e.currentTarget.value)}>
                  {#each roles as r (r)}<option value={r}>{roleWords[r]}</option>{/each}
                </select>
                <span class="mact"><button class="btn quiet small" onclick={() => remove(m)} disabled={busy}>Withdraw</button></span>
              </li>
            {/each}
          </ul>
        </div>
      {/if}

      <div class="mlist-wrap">
        <h2 class="mlist-head">Libraries shared with this group{#if loaded}<span class="muted tnum">{` · ${libraries.length}`}</span>{/if}</h2>
        {#if loaded && !libraries.length}
          <p class="mrow-none">None yet.</p>
        {:else}
          <ul class="mlist">
            {#each libraries as l (l.library)}
              {@const key = `library:${l.library}`}
              <li class="mrow">
                <span class="gtile muted-tile" aria-hidden="true">{initialOf(l.name || l.library)}</span>
                <span class="mwho">
                  <span class="mname">{l.name || l.library}</span><span class="msince mono">{l.library}</span>
                  {#if rowError[key]}<span class="err" role="alert">{rowError[key]}</span>{/if}
                </span>
                <span class="mrole-text">{libRoleWords[l.role] ?? l.role}</span>
                <span class="mact">
                  {#if maintains}
                    {#if removing === key}
                      <button class="btn danger-solid small" onclick={() => unshare(l)} disabled={busy}>Stop sharing</button>
                      <button class="btn quiet small" onclick={() => (removing = "")}>Cancel</button>
                    {:else}
                      <button class="btn danger small" onclick={() => (removing = key)} disabled={busy}>Remove</button>
                    {/if}
                  {/if}
                </span>
              </li>
            {/each}
          </ul>
        {/if}
        {#if maintains}<p class="hint">Share a library from its Members page.</p>{/if}
      </div>

      {#if group?.role}
        <div class="leave">
          {#if onlyMaintainer}
            <span>You are the only maintainer. Make someone else a maintainer before you leave.</span>
            <button class="btn danger small" disabled>Leave group</button>
          {:else if leaving}
            <span><strong>Leave {group.name}?</strong> Its libraries leave your search, unless you hold them yourself.</span>
            <span class="row-btns">
              <button class="btn danger-solid small" onclick={leave} disabled={busy}>Leave</button>
              <button class="btn quiet small" onclick={() => (leaving = false)}>Cancel</button>
            </span>
          {:else}
            <span>Leaving takes the group's libraries out of your search, unless you hold them yourself.</span>
            <button class="btn danger small" onclick={() => (leaving = true)} disabled={busy || !me}>Leave group</button>
          {/if}
        </div>
      {/if}
      {#if maintains}
        <div class="leave">
          {#if deleting}
            <span><strong>Delete {group?.name ?? slug}?</strong>{#if libraries.length} Its members lose {libraries.length === 1 ? "its library" : `its ${libraries.length} libraries`} at once, unless they hold them themselves.{:else} This can't be undone.{/if}</span>
            <span class="row-btns">
              <button class="btn danger-solid small" onclick={deleteGroup} disabled={busy}>Delete</button>
              <button class="btn quiet small" onclick={() => (deleting = false)}>Cancel</button>
            </span>
          {:else}
            <span>{#if libraries.length}Deleting removes the group from {libraries.length === 1 ? "its library" : `its ${libraries.length} libraries`}. The libraries stay.{:else}Deleting removes the group and its invitations.{/if}</span>
            <button class="btn danger small" onclick={() => (deleting = true)} disabled={busy}>Delete group</button>
          {/if}
        </div>
      {/if}
    </section>

    {#if maintains}
      <aside class="mlog" aria-labelledby="glog-title">
        <h2 id="glog-title" class="mlist-head">Changes</h2>
        {#if story.lines.length}
          <ol class="mlog-list">
            {#each story.lines as line, i (i)}<li><span>{line.what}</span><span class="msince tnum">{line.when}</span></li>{/each}
          </ol>
        {/if}
      </aside>
    {/if}
  </div>
{/if}
