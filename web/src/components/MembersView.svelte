<script lang="ts">
  import { Code, ConnectError } from "@connectrpc/connect";
  import type { Event, Member } from "../gen/docsearch/library/v1/library_pb";
  import { problem, shelves } from "../lib/api";
  import { library } from "../lib/library.svelte";
  import { libraries } from "../lib/libraries.svelte";
  import { router } from "../lib/router.svelte";
  import { toast } from "../lib/toast.svelte";

  const roles = ["reader", "editor", "admin"] as const;
  const roleWords: Record<string, string> = { reader: "Reader", editor: "Editor", admin: "Admin" };

  let members = $state<Member[]>([]);
  let events = $state<Event[]>([]);
  let loadError = $state("");
  let loaded = $state(false);

  let invitee = $state("");
  let inviteRole = $state<string>("editor");
  let inviteError = $state("");
  let inviting = $state(false);
  let inviteField: HTMLInputElement | undefined = $state();

  let rowError = $state<Record<string, string>>({});
  let removing = $state("");
  let leaving = $state(false);
  let busy = $state(false);

  const label = $derived(libraries.label);
  const admins = $derived(members.filter((m) => m.role === "admin").length);
  const me = $derived(libraries.username);
  const onlyAdmin = $derived(libraries.isAdmin && admins <= 1);

  async function load(of: string): Promise<void> {
    loadError = "";
    try {
      const [listed, logged] = await Promise.all([
        shelves.members({ library: of }),
        libraries.isAdmin ? shelves.events({ library: of }) : Promise.resolve({ events: [] as Event[] }),
      ]);
      if (of !== libraries.label) return;
      members = listed.members;
      events = logged.events;
    } catch (err) {
      if (lostLibrary(err)) return void (await lost());
      loadError = problem(err);
    } finally {
      loaded = true;
    }
  }

  $effect(() => {
    if (libraries.isOwn || !libraries.current) return;
    loaded = false;
    members = [];
    events = [];
    rowError = {};
    removing = "";
    leaving = false;
    void load(label);
  });

  const day = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" });
  const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" });
  function dayOf(e: Event): string {
    const at = e.at ? new Date(Number(e.at.seconds) * 1000) : null;
    if (!at) return "";
    return at.toDateString() === new Date().toDateString() ? "today" : `on ${day.format(at)}`;
  }
  function whenOf(e: Event): string {
    const at = e.at ? new Date(Number(e.at.seconds) * 1000) : null;
    if (!at) return "";
    return at.toDateString() === new Date().toDateString() ? `Today ${clock.format(at)}` : `${day.format(at)} ${clock.format(at)}`;
  }
  const article = (role: string) => (role === "admin" || role === "editor" ? `an ${role}` : `a ${role}`);

  // The log reads in order: a grant to someone with no role yet adds them,
  // and one to someone who has a role changes it.
  const story = $derived.by(() => {
    const held = new Map<string, string>();
    const lines: { what: string; when: string }[] = [];
    const since = new Map<string, string>();
    let created = "";
    for (const e of events) {
      const actor = e.actor || "Someone";
      const subject = e.subject || "someone";
      if (e.action === "create") {
        created = actor;
        since.set(e.actor, `Created the library ${dayOf(e)}`);
        lines.push({ what: `${actor} created ${libraries.name}`, when: whenOf(e) });
      } else if (e.action === "grant") {
        // Creating a library makes its creator admin; the create line says so.
        if (e.subject === created && e.actor === created && !held.has(e.subject)) {
          held.set(e.subject, e.role);
          continue;
        }
        lines.push({
          what: held.has(e.subject) ? `${actor} made ${subject} ${article(e.role)}` : `${actor} added ${subject} as ${e.role}`,
          when: whenOf(e),
        });
        if (!held.has(e.subject)) since.set(e.subject, `Added by ${actor} ${dayOf(e)}`);
        held.set(e.subject, e.role);
      } else if (e.action === "revoke") {
        lines.push({ what: e.actor === e.subject ? `${subject} left` : `${actor} removed ${subject}`, when: whenOf(e) });
        held.delete(e.subject);
        since.delete(e.subject);
      }
    }
    return { lines: lines.reverse(), since };
  });

  // The server answers a library this account no longer changes, or no
  // longer reads, as one it has no library by.
  const lostLibrary = (err: unknown) =>
    err instanceof ConnectError && err.code === Code.NotFound && /^no library/.test(err.rawMessage);

  /** Reads the libraries again after a refusal that says the role changed, and says what changed. */
  async function lost(): Promise<void> {
    const name = libraries.name;
    const of = label;
    await libraries.load();
    toast.show(libraries.all.some((l) => l.library === of)
      ? `You're no longer an admin of ${name}.`
      : `You're no longer a member of ${name}.`);
  }

  function refusal(err: unknown, who: string): string {
    if (err instanceof ConnectError && err.code === Code.NotFound)
      return `No account named ${who} yet. They need to sign in once before you can invite them.`;
    if (err instanceof ConnectError && err.code === Code.FailedPrecondition)
      return "That would leave the library without an admin. Make someone else an admin first.";
    return problem(err);
  }

  async function invite(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    inviteError = "";
    const who = invitee.trim().toLowerCase();
    if (!who) {
      inviteError = "Type the username they sign in with.";
      inviteField?.focus();
      return;
    }
    if (members.some((m) => m.username === who)) {
      inviteError = `${who} is already a member. Change their role in the list.`;
      return;
    }
    inviting = true;
    try {
      await shelves.grant({ library: label, username: who, role: inviteRole });
      toast.show(`Added ${who} as ${inviteRole}.`);
      invitee = "";
      await load(label);
    } catch (err) {
      if (lostLibrary(err)) return void (await lost());
      inviteError = refusal(err, who);
      inviteField?.focus();
    } finally {
      inviting = false;
    }
  }

  async function changeRole(m: Member, role: string): Promise<void> {
    const before = m.role;
    rowError = { ...rowError, [m.username]: "" };
    m.role = role;
    busy = true;
    try {
      await shelves.grant({ library: label, username: m.username, role });
      if (m.username === me) await libraries.load();
      toast.show(`${m.username} is now ${article(role)}.`);
      await load(label);
    } catch (err) {
      m.role = before;
      if (lostLibrary(err)) return void (await lost());
      rowError = { ...rowError, [m.username]: refusal(err, m.username) };
    } finally {
      busy = false;
    }
  }

  async function remove(m: Member): Promise<void> {
    busy = true;
    try {
      await shelves.revoke({ library: label, username: m.username });
      toast.show(`Removed ${m.username}.`);
      removing = "";
      await load(label);
    } catch (err) {
      if (lostLibrary(err)) return void (await lost());
      rowError = { ...rowError, [m.username]: refusal(err, m.username) };
    } finally {
      busy = false;
    }
  }

  async function leave(): Promise<void> {
    const left = libraries.name;
    busy = true;
    try {
      await libraries.leave();
      toast.show(`You left ${left}.`);
      router.go("library");
    } catch (err) {
      leaving = false;
      if (lostLibrary(err)) return void (await lost());
      loadError = err instanceof ConnectError && err.code === Code.FailedPrecondition
        ? "You're its only admin. Make someone else an admin before you leave."
        : problem(err);
    } finally {
      busy = false;
    }
  }

  const initial = (name: string) => (name.match(/[a-z0-9]/i)?.[0] ?? "?").toUpperCase();
</script>

<div class="page-head">
  <h1>{libraries.name}</h1>
  {#if !libraries.isOwn}
    <p>Agents name this library <span class="mono">{label}</span> · {library.docs.length === 1 ? "1 document" : `${library.docs.length} documents`}</p>
  {/if}
</div>

{#if libraries.isOwn}
  <section class="members-solo">
    <p>Only you can use your personal library. To work with others, create a shared library from the library menu.</p>
  </section>
{:else}
  <div class="members">
    <section class="members-main" aria-label="Members">
      {#if loadError}<div class="banner" role="alert"><span>{loadError}</span></div>{/if}

      {#if libraries.isAdmin}
        <form class="invite" onsubmit={invite} novalidate>
          <label class="lbl" for="invite-user">Invite someone</label>
          <div class="invite-row">
            <input class="input" id="invite-user" type="text" placeholder="Username" autocomplete="off" spellcheck="false"
              bind:value={invitee} bind:this={inviteField} aria-invalid={inviteError ? "true" : undefined}
              aria-describedby={inviteError ? "invite-err invite-hint" : "invite-hint"} />
            <label class="role-pick"><span>as</span>
              <select aria-label="Role" bind:value={inviteRole}>
                {#each roles as r (r)}<option value={r}>{roleWords[r]}</option>{/each}
              </select>
            </label>
            <button class="btn primary" type="submit" disabled={inviting}>Invite</button>
          </div>
          {#if inviteError}<p class="err" id="invite-err" role="alert">{inviteError}</p>{/if}
          <p class="hint" id="invite-hint">Readers search and read. Editors also add and remove documents. Admins also manage members.</p>
        </form>
      {/if}

      <div class="mlist-wrap">
        <h2 class="mlist-head">Members{#if loaded}<span class="muted tnum">{` · ${members.length}`}</span>{/if}</h2>
        <ul class="mlist">
          {#each members as m (m.username)}
            {@const self = m.username === me}
            {@const since = story.since.get(m.username)}
            <li class="mrow">
              <span class="mavatar" aria-hidden="true">{initial(m.username || "?")}</span>
              <span class="mwho">
                <span class="mname">{m.username || "(no username yet)"}{#if self}<span class="muted">{" · you"}</span>{/if}{#if m.role === "admin" && admins === 1}<span class="tag">Only admin</span>{/if}</span>
                {#if since}<span class="msince">{since}</span>{/if}
                {#if rowError[m.username]}<span class="err" role="alert">{rowError[m.username]}</span>{/if}
              </span>
              {#if libraries.isAdmin && !(self && onlyAdmin)}
                <select class="mrole" aria-label={`Role for ${m.username}`} value={m.role} disabled={busy}
                  onchange={(e) => changeRole(m, e.currentTarget.value)}>
                  {#each roles as r (r)}<option value={r}>{roleWords[r]}</option>{/each}
                </select>
              {:else}
                <span class="mrole-text">{roleWords[m.role] ?? m.role}</span>
              {/if}
              <span class="mact">
                {#if libraries.isAdmin && !self}
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

      <div class="leave">
        {#if onlyAdmin}
          <span>You are the only admin. Make someone else an admin before you leave.</span>
          <button class="btn danger small" disabled>Leave library</button>
        {:else if leaving}
          <span><strong>Leave {libraries.name}?</strong> You lose access to its documents until an admin adds you again.</span>
          <span class="row-btns">
            <button class="btn danger-solid small" onclick={leave} disabled={busy}>Leave</button>
            <button class="btn quiet small" onclick={() => (leaving = false)}>Cancel</button>
          </span>
        {:else}
          <span>Leaving removes your access to its documents.</span>
          <button class="btn danger small" onclick={() => (leaving = true)} disabled={busy || !me}>Leave library</button>
        {/if}
      </div>
    </section>

    {#if libraries.isAdmin}
      <aside class="mlog" aria-labelledby="mlog-title">
        <h2 id="mlog-title" class="mlist-head">Changes</h2>
        {#if story.lines.length}
          <ol class="mlog-list">
            {#each story.lines as line, i (i)}<li><span>{line.what}</span><span class="msince tnum">{line.when}</span></li>{/each}
          </ol>
        {/if}
      </aside>
    {/if}
  </div>
{/if}
