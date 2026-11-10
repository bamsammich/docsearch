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
  /** Puts the keyboard on a confirm button as it appears in place of the one clicked. */
  const focus = (el: HTMLElement) => el.focus();

  let members = $state<Member[]>([]);
  let libraries = $state<Library[]>([]);
  let events = $state<Event[]>([]);
  let loadError = $state("");
  /** The caller can't see inside the group: they aren't in it. */
  let outside = $state(false);
  let loaded = $state(false);
  /** The group slug the store was last read again for, so a missing one is looked for once. */
  let lookedFor = $state("");

  let invitee = $state("");
  let inviteRole = $state<string>("member");
  let forced = $state(false);
  let inviteError = $state("");
  let rowError = $state<Record<string, string>>({});
  let removing = $state("");
  let leaving = $state(false);
  let deleting = $state(false);
  let busy = $state(false);

  /** The provider group this group follows, read only by administrators. */
  let providerGroup = $state("");
  /** Whether this administrator may change the link on this deployment. */
  let canLink = $state(false);
  let linkDraft = $state("");
  let linkError = $state("");
  let unlinking = $state(false);

  const slug = $derived(router.docId);
  const group = $derived(groups.byId(slug));
  const invitation = $derived(groups.invitations.find((i) => i.group === slug));
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
      // Only maintainers read the log; a maintainer who just stepped down no longer does.
      const logged = maintains
        ? await service.events({ group: of }).catch(() => ({ events: [] as Event[] }))
        : { events: [] as Event[] };
      const link = admin.isAdmin
        ? await service.providerLink({ group: of }).catch(() => ({ providerGroup: "", canLink: false }))
        : { providerGroup: "", canLink: false };
      if (of !== router.docId) return;
      outside = false;
      providerGroup = link.providerGroup;
      canLink = link.canLink;
      linkDraft = link.providerGroup;
      members = m.members;
      libraries = l.libraries;
      events = logged.events;
    } catch (err) {
      if (of !== router.docId) return;
      if (err instanceof ConnectError && err.code === Code.NotFound) {
        outside = true;
        members = [];
        libraries = [];
        events = [];
      } else {
        loadError = problem(err);
      }
    } finally {
      loaded = true;
    }
  }

  $effect(() => {
    const of = slug;
    if (!of || !groups.loaded) return;
    // A group made since the store was read: read it again once.
    if (!groups.byId(of) && lookedFor !== of) {
      lookedFor = of;
      void groups.load();
      return;
    }
    loaded = false;
    rowError = {};
    removing = "";
    leaving = false;
    deleting = false;
    forced = false;
    unlinking = false;
    linkError = "";
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
  const plural = (n: number, one: string, many: string) => (n === 1 ? `1 ${one}` : `${n} ${many}`);

  // The log, newest first, and who added each member.
  const story = $derived.by(() => {
    const lines: { what: string; when: string }[] = [];
    const since = new Map<string, string>();
    const inviter = new Map<string, string>();
    for (const e of events) {
      const actor = e.actor || "Someone";
      const subject = e.subject || "someone";
      const self = e.actor !== "" && e.actor === e.subject;
      switch (e.action) {
        case "create":
          if (!e.actor && e.detail) {
            lines.push({ what: `Created for the provider group ${e.detail}`, when: whenOf(e) });
            break;
          }
          lines.push({ what: `${actor} created ${group?.name ?? slug}`, when: whenOf(e) });
          since.set(e.actor, `Created the group ${dayOf(e)}`);
          break;
        case "link":
          lines.push({ what: `${actor} linked it to the provider group ${e.detail}`, when: whenOf(e) });
          break;
        case "unlink":
          lines.push({ what: `${actor} stopped it following the provider group ${e.detail}`, when: whenOf(e) });
          break;
        case "provider_join":
          lines.push({ what: `${subject} joined through the provider group`, when: whenOf(e) });
          since.set(e.subject, `From the provider group ${dayOf(e)}`);
          break;
        case "provider_leave":
          lines.push({
            what: e.actor ? `${subject} left when ${actor} changed the link` : `${subject} left the provider group`,
            when: whenOf(e),
          });
          since.delete(e.subject);
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
        case "force":
          lines.push({ what: `${actor} added ${self ? "themselves" : subject} as ${e.role}, by force`, when: whenOf(e) });
          since.set(e.subject, `Added by force by ${actor} ${dayOf(e)}`);
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
          lines.push({
            what: self
              ? e.role === "member" ? `${actor} stepped down to member` : `${actor} became a maintainer`
              : `${actor} made ${subject} ${article(e.role)}`,
            when: whenOf(e),
          });
          break;
        case "remove":
          lines.push({ what: self ? `${subject} left` : `${actor} removed ${subject}`, when: whenOf(e) });
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
      if (err.code === Code.NotFound && /group/.test(err.rawMessage))
        return "You no longer maintain this group.";
      if (err.code === Code.FailedPrecondition && /maintainer/.test(err.rawMessage))
        return "That would leave the group without a maintainer. Make someone else a maintainer first.";
      if (err.code === Code.FailedPrecondition && /force/.test(err.rawMessage))
        return "You're not in this group. To join it, add yourself without an invitation; the group and its libraries log it.";
      if (err.code === Code.FailedPrecondition) return `${who} is in this group through the provider. Remove them there.`;
    }
    return problem(err);
  }

  /**
   * Reads the groups again after a refusal that may mean the group is gone,
   * or that the caller no longer maintains it. Answers whether it's gone.
   */
  async function lost(err: unknown): Promise<boolean> {
    if (!(err instanceof ConnectError && err.code === Code.NotFound && /group/.test(err.rawMessage))) return false;
    await groups.load();
    if (!groups.byId(slug)) {
      toast.show(`${slug} no longer exists.`);
      return true;
    }
    await load(slug);
    return false;
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
      inviteError = who === me ? "You're already in this group."
        : known.invited ? `${who} is already invited. Change the role in the list.`
        : `${who} is already in the group. Change their role in the list.`;
      return;
    }
    busy = true;
    try {
      if (forced) {
        await service.invite({ group: slug, username: who, role: inviteRole, force: true });
        toast.show(`Added ${who} as ${inviteRole}. The group and its libraries log it.`);
        forced = false;
        if (who === me) await groups.load();
      } else {
        const { invited: asked } = await service.invite({ group: slug, username: who, role: inviteRole });
        toast.show(asked ? `Invited ${who} as ${inviteRole}. They join once they accept.` : `${who} is now ${article(inviteRole)}.`);
      }
      invitee = "";
      await load(slug);
    } catch (err) {
      if (await lost(err)) return;
      inviteError = refusal(err, who);
    } finally {
      busy = false;
    }
  }

  async function changeRole(m: Member, role: string, select: HTMLSelectElement): Promise<void> {
    rowError = {};
    busy = true;
    try {
      await service.invite({ group: slug, username: m.username, role });
      toast.show(m.username === me
        ? role === "member" ? "You stepped down to member." : "You're now a maintainer."
        : m.invited ? `${m.username}'s invitation is now for ${role}.` : `${m.username} is now ${article(role)}.`);
      // The caller's own role decides what the page shows, so read it first.
      await groups.load();
      await load(slug);
    } catch (err) {
      select.value = m.role;
      if (await lost(err)) return;
      rowError = { [m.username]: refusal(err, m.username) };
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
      if (await lost(err)) return;
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
      if (await lost(err)) return;
      rowError = { [`library:${l.library}`]: problem(err) };
    } finally {
      busy = false;
    }
  }

  async function answer(accept: boolean): Promise<void> {
    if (!invitation) return;
    busy = true;
    try {
      if (accept) {
        await groups.accept(slug);
        toast.show(`Joined ${invitation.name}.`);
        await load(slug);
      } else {
        await groups.decline(slug);
        toast.show(`Declined ${invitation.name}.`);
      }
    } catch (err) {
      loadError = problem(err);
      void groups.load();
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
      if (await lost(err)) return;
      loadError = refusal(err, me);
    } finally {
      busy = false;
    }
  }

  async function link(event: SubmitEvent | null, to: string): Promise<void> {
    event?.preventDefault();
    linkError = "";
    const wanted = to.trim();
    if (event && !wanted) {
      linkError = "Name the provider group, as your provider puts it in sign-ins.";
      return;
    }
    busy = true;
    try {
      await service.link({ group: slug, providerGroup: wanted });
      toast.show(wanted ? `${group?.name ?? slug} follows ${wanted}.` : `${group?.name ?? slug} follows no provider group.`);
      unlinking = false;
      await Promise.all([load(slug), groups.load()]);
    } catch (err) {
      if (await lost(err)) return;
      linkError = err instanceof ConnectError && err.code === Code.AlreadyExists
        ? `Another group already follows ${wanted}.`
        : problem(err);
    } finally {
      busy = false;
    }
  }

  async function deleteGroup(): Promise<void> {
    const name = group?.name ?? slug;
    // Where to land is decided by the role held before the group is gone.
    const back = admin.isAdmin && group?.role !== "maintainer" ? "allgroups" : "groups";
    busy = true;
    try {
      await service.delete({ group: slug });
      toast.show(libraries.length ? `Deleted ${name}. Its libraries stay with their own members.` : `Deleted ${name}.`);
      await groups.load();
      router.go(back);
    } catch (err) {
      deleting = false;
      if (await lost(err)) return;
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

{#if groups.loaded && !group && lookedFor === slug}
  <section class="members-solo"><p>No group named <span class="mono">{slug}</span>. <a href="#/groups">See every group.</a></p></section>
{:else if outside}
  <section class="members-solo">
    {#if invitation}
      <p>{invitation.invitedBy ? `${invitation.invitedBy} invited you` : "You're invited"} to join as {invitation.role}. Its libraries reach your search once you accept.</p>
      <div class="row-btns">
        <button class="btn primary" onclick={() => answer(true)} disabled={busy}>Accept</button>
        <button class="btn quiet" onclick={() => answer(false)} disabled={busy}>Decline</button>
      </div>
    {:else}
      <p>You're not in this group. Its maintainers invite people. <a href="#/groups">See every group.</a></p>
    {/if}
  </section>
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
            <button class="btn primary" type="submit" disabled={busy}>{forced ? "Add now" : "Invite"}</button>
          </div>
          {#if admin.isAdmin}
            <label class="setting-check">
              <input type="checkbox" bind:checked={forced} />
              <span>Add now, without an invitation. The group and its libraries log it.</span>
            </label>
          {/if}
          {#if inviteError}<p class="err" role="alert">{inviteError}</p>{/if}
          <p class="hint">Maintainers also invite and remove people, and share libraries they administer with the group.</p>
        </form>
      {/if}

      {#if admin.isAdmin && (canLink || providerGroup)}
        <form class="invite" onsubmit={(e) => link(e, linkDraft)} novalidate>
          <label class="lbl" for="glink">Provider group</label>
          {#if canLink}
            <div class="invite-row">
              <input class="input" id="glink" type="text" placeholder="Group name in sign-ins" autocomplete="off"
                spellcheck="false" bind:value={linkDraft} oninput={() => (linkError = "")}
                aria-invalid={linkError ? "true" : undefined} />
              <button class="btn primary" type="submit" disabled={busy || linkDraft.trim() === providerGroup}>
                {providerGroup ? "Change" : "Link"}
              </button>
            </div>
          {:else}
            <p class="mono">{providerGroup}</p>
          {/if}
          {#if linkError}<p class="err" role="alert">{linkError}</p>{/if}
          {#if canLink}
            <p class="hint">Anyone whose sign-in carries it joins, and leaves when it stops. Members invited by hand stay.</p>
          {:else}
            <p class="hint">This server reads no provider groups, so the link does nothing until --oidc-groups-claim is set.</p>
          {/if}
          {#if canLink && providerGroup}
            <div class="leave">
              {#if unlinking}
                <span><strong>Stop following {providerGroup}?</strong> Members from the provider leave now.</span>
                <span class="row-btns">
                  <button class="btn danger-solid small" type="button" onclick={() => link(null, "")} disabled={busy} {@attach focus}>Stop following</button>
                  <button class="btn quiet small" type="button" onclick={() => (unlinking = false)}>Cancel</button>
                </span>
              {:else}
                <span>Members from the provider leave when the link stops.</span>
                <button class="btn danger small" type="button" onclick={() => (unlinking = true)} disabled={busy}>Stop following</button>
              {/if}
            </div>
          {/if}
        </form>
      {/if}

      <div class="mlist-wrap">
        <h2 class="mlist-head">Members{#if loaded}<span class="muted tnum">{` · ${joined.length}`}</span>{/if}</h2>
        {#if loaded && !joined.length}
          <p class="mrow-none">No maintainer or member. An administrator can add someone without an invitation.</p>
        {:else}
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
                    onchange={(e) => changeRole(m, e.currentTarget.value, e.currentTarget)}>
                    {#each roles as r (r)}<option value={r}>{roleWords[r]}</option>{/each}
                  </select>
                {:else}
                  <span class="mrole-text">{roleWords[m.role] ?? m.role}</span>
                {/if}
                <span class="mact">
                  {#if maintains && !self && !m.fromProvider}
                    {#if removing === m.username}
                      <button class="btn danger-solid small" onclick={() => remove(m)} disabled={busy} {@attach focus}>Remove {m.username}</button>
                      <button class="btn quiet small" onclick={() => (removing = "")}>Cancel</button>
                    {:else}
                      <button class="btn danger small" onclick={() => (removing = m.username)} disabled={busy}>Remove</button>
                    {/if}
                  {/if}
                </span>
              </li>
            {/each}
          </ul>
        {/if}
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
                  onchange={(e) => changeRole(m, e.currentTarget.value, e.currentTarget)}>
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
                      <button class="btn danger-solid small" onclick={() => unshare(l)} disabled={busy} {@attach focus}>Stop sharing</button>
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
              <button class="btn danger-solid small" onclick={leave} disabled={busy} {@attach focus}>Leave</button>
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
              <button class="btn danger-solid small" onclick={deleteGroup} disabled={busy} {@attach focus}>Delete</button>
              <button class="btn quiet small" onclick={() => (deleting = false)}>Cancel</button>
            </span>
          {:else}
            <span>{#if libraries.length}Deleting removes the group from {libraries.length === 1 ? "its library" : `its ${plural(libraries.length, "library", "libraries")}`}. The libraries stay.{:else}Deleting removes the group and its invitations.{/if}</span>
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
