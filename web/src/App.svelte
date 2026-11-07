<script lang="ts">
  import { loadConfig } from "./lib/config";
  import { library } from "./lib/library.svelte";
  import { roleChanged } from "./lib/api";
  import { labelOf, libraries, nameOf } from "./lib/libraries.svelte";
  import { queue } from "./lib/queue.svelte";
  import { adminViews, router } from "./lib/router.svelte";
  import { admin } from "./lib/admin.svelte";
  import { groupsStore as groups } from "./lib/groups.svelte";
  import { search } from "./lib/search.svelte";
  import { session } from "./lib/session.svelte";
  import { toast } from "./lib/toast.svelte";
  import AccountMenu from "./components/AccountMenu.svelte";
  import AdminGroupsView from "./components/AdminGroupsView.svelte";
  import GroupView from "./components/GroupView.svelte";
  import GroupsView from "./components/GroupsView.svelte";
  import Brand from "./components/Brand.svelte";
  import Icon from "./components/Icon.svelte";
  import IngestView from "./components/IngestView.svelte";
  import LibraryPicker from "./components/LibraryPicker.svelte";
  import LibraryView from "./components/LibraryView.svelte";
  import MembersView from "./components/MembersView.svelte";
  import PeopleView from "./components/PeopleView.svelte";
  import SignInView from "./components/SignInView.svelte";
  import ReaderView from "./components/ReaderView.svelte";
  import SearchView from "./components/SearchView.svelte";
  import SignIn from "./components/SignIn.svelte";

  let bootError = $state("");
  let field: HTMLInputElement | undefined = $state();

  $effect(() => {
    loadConfig()
      .then((config) => session.start(config))
      .catch((err: unknown) => (bootError = err instanceof Error ? err.message : String(err)));
  });

  // Signing in loads the libraries and starts watching the queue for the counts.
  $effect(() => {
    if (session.status !== "signed-in") return;
    void libraries.load();
    void admin.load();
    return queue.watch();
  });

  // Only password and OIDC sign-in have accounts to put in groups.
  const grouping = $derived(session.config?.auth === "password" || session.config?.auth === "oidc");
  $effect(() => {
    if (session.status === "signed-in" && grouping) void groups.load();
  });
  $effect(() => {
    if (router.view === "groups" && session.config && !grouping) router.go("library");
  });
  // Joining a group adds its libraries.
  let joined = -1;
  $effect(() => {
    const count = groups.mine.length;
    if (joined >= 0 && count !== joined) void libraries.load();
    joined = count;
  });

  // A refusal of what the account's role no longer allows reads its
  // libraries and its administrator role again.
  $effect(() => {
    const reread = () => {
      void libraries.load();
      void admin.load();
    };
    window.addEventListener(roleChanged, reread);
    return () => window.removeEventListener(roleChanged, reread);
  });

  // The pages show one library: choosing another loads its documents, and a
  // search scoped to a document of the last one no longer applies.
  let shownLabel = "";
  $effect(() => {
    const label = libraries.label;
    if (session.status !== "signed-in") return;
    if (shownLabel && shownLabel !== label && labelOf(search.docId) !== label) search.docId = "";
    shownLabel = label;
    void library.load();
  });

  // A link to a document shows the library that holds it, so the reader and
  // the scope find it among that library's documents.
  $effect(() => {
    if (!["search", "read", "library"].includes(router.view) || !router.docId) return;
    const label = labelOf(router.docId);
    if (label !== libraries.label && libraries.all.some((l) => l.library === label)) libraries.choose(label);
  });

  // Administration is for administrators: anyone else lands on the library.
  $effect(() => {
    if (adminViews.includes(router.view) && admin.loaded && !admin.available) router.go("library");
  });

  $effect(() => search.attach(field));

  // A link to #/search/<doc> scopes the search to that document.
  $effect(() => {
    if (router.view === "search" && router.docId) search.docId = router.docId;
  });

  const attention = $derived(library.docs.filter((d) => library.needsAttention(d)).length);

  // A finished ingest changes the library: reload it whichever page is open.
  let finished = -1;
  $effect(() => {
    const done = queue.done.filter((j) => j.status === "done").length;
    if (finished >= 0 && done !== finished) void library.load();
    finished = done;
  });
  const ingestAlert = $derived(queue.failed + queue.stalled);
  const ingestCount = $derived(ingestAlert || queue.active);
  const config = $derived(session.config);
  const titles = { library: "Documents", ingest: "Ingest", members: "Members", people: "People", signin: "Sign-in", search: "Search", read: "Reader", groups: "Groups", allgroups: "Groups" } as const;
  const administering = $derived(adminViews.includes(router.view));
  // The groups pages belong to the account rather than to one library.
  const onGroups = $derived(router.view === "groups");

  const instance = $derived(config?.instanceLabel ?? "");

  $effect(() => {
    const page = session.status !== "signed-in" ? "docsearch"
      : administering ? `${titles[router.view]} · Administration · docsearch`
      : onGroups ? (router.docId ? `${groups.byId(router.docId)?.name ?? router.docId} · Groups · docsearch` : "Groups · docsearch")
      : `${titles[router.view]} · ${libraries.name} · docsearch`;
    document.title = instance ? `${instance} · ${page}` : page;
  });

  function submit(event: SubmitEvent) {
    event.preventDefault();
    if (!search.query.trim()) {
      field?.focus();
      return;
    }
    search.submit();
    if (router.view !== "search") router.go("search");
  }

  // "/" puts the cursor in the search field from anywhere but a text field.
  function shortcut(event: KeyboardEvent) {
    if (event.key !== "/" || event.metaKey || event.ctrlKey || event.altKey) return;
    const el = event.target as HTMLElement | null;
    if (el?.closest("input, textarea, select, [contenteditable]")) return;
    if (session.status !== "signed-in") return;
    event.preventDefault();
    search.focus();
  }
</script>

<svelte:window onkeydown={shortcut} />

{#if instance}
  <div class="instance" role="note">
    <Icon name="alert" /><strong>{instance}</strong><span class="mono">{location.host}</span>
  </div>
{/if}
{#if bootError}
  <div class="signin">
    <div class="signin-brand"><Brand /></div>
    <section class="signin-card" role="alert">
      <h1>docsearch isn't answering</h1>
      <p class="signin-lead">{bootError}</p>
    </section>
  </div>
{:else if session.status === "loading"}
  <div class="signin"><div class="signin-brand"><Brand /></div></div>
{:else if session.status !== "signed-in"}
  <SignIn />
{:else}
  <div class="app">
    <aside class="side">
      <a class="side-brand" href="#/library" aria-label="docsearch library"><Brand /></a>
      {#if administering}
        <a class="side-back" href="#/library"><Icon name="back" /><span>{libraries.name}</span></a>
        <nav class="sections" aria-label="Administration">
          <span class="sections-label">Administration</span>
          <a class="section" href="#/people" aria-current={router.view === "people" ? "page" : undefined}>
            <Icon name="person" /><span class="section-name">People</span>
          </a>
          <a class="section" href="#/allgroups" aria-current={router.view === "allgroups" ? "page" : undefined}>
            <Icon name="group" /><span class="section-name">Groups</span>
          </a>
          <a class="section" href="#/signin" aria-current={router.view === "signin" ? "page" : undefined}>
            <Icon name="lock" /><span class="section-name">Sign-in</span>
          </a>
        </nav>
      {:else if onGroups}
        <a class="side-back" href="#/library"><Icon name="back" /><span>{libraries.name}</span></a>
        <nav class="sections" aria-label="Groups">
          <a class="section" href="#/groups" aria-current={router.docId ? undefined : "page"}>
            <Icon name="group" /><span class="section-name">Groups</span>
            {#if groups.invitations.length}<span class="count hot" aria-label={`${groups.invitations.length} invitations`}>{groups.invitations.length}</span>{/if}
          </a>
          {#each groups.mine as g (g.group)}
            <a class="section section-sub" href={`#/groups/${encodeURIComponent(g.group)}`} aria-current={router.docId === g.group ? "page" : undefined}>
              <span class="section-name">{g.name}</span>
            </a>
          {/each}
        </nav>
      {:else}
        <div class="side-picker"><LibraryPicker id="library-picker" where="side" /></div>
        <nav class="sections" aria-label={libraries.name}>
          <a class="section" href="#/library" aria-current={router.view === "library" || router.view === "read" ? "page" : undefined}>
            <Icon name="library" /><span class="section-name">Documents</span>
            {#if attention}<span class="count hot" aria-label={`${attention} need attention`}>{attention}</span>{/if}
          </a>
          <a class="section" href="#/ingest" aria-current={router.view === "ingest" ? "page" : undefined}>
            <Icon name="inbox" /><span class="section-name">Ingest</span>
            {#if ingestCount}
              <span class="count" class:hot={ingestAlert > 0}
                aria-label={ingestAlert ? `${ingestAlert} need attention` : `${queue.active} active`}>{ingestAlert || queue.active}{#if !ingestAlert}<span class="count-word"> running</span>{/if}</span>
            {/if}
          </a>
          <a class="section" href="#/members" aria-current={router.view === "members" ? "page" : undefined}>
            <Icon name="members" /><span class="section-name">Members</span>
          </a>
        </nav>
      {/if}
      <div class="side-foot"><AccountMenu /></div>
    </aside>

    <header class="topbar">
      {#if !administering && !onGroups}<div class="top-picker"><LibraryPicker id="library-picker-top" where="top" /></div>{/if}
      <form class="gsearch" role="search" onsubmit={submit} novalidate>
        <Icon name="search" />
        <label class="sr-only" for="g-q">Search every library you can read</label>
        <input id="g-q" type="search" placeholder={search.library || search.docId ? "Search" : "Search every library you can read"} autocomplete="off"
          bind:value={search.query} bind:this={field} />
        <label class="sr-only" for="g-scope">Search in</label>
        <select id="g-scope" class="scope" bind:value={search.scope}>
          <option value="">All libraries</option>
          {#if libraries.all.length > 1}
            <optgroup label="One library">
              {#each libraries.all as l (l.library)}<option value={`library:${l.library}`}>{nameOf(l)}</option>{/each}
            </optgroup>
          {/if}
          {#if library.docs.length}
            <optgroup label={`In ${libraries.name}`}>
              {#each library.docs as d (d.docId)}<option value={d.docId}>{d.title}</option>{/each}
            </optgroup>
          {/if}
        </select>
        <kbd class="key" aria-hidden="true">/</kbd>
      </form>
      <div class="top-end">
        {#if router.view !== "ingest" && !administering && !onGroups && libraries.canEdit}
          <a class="btn on-frame add" href="#/ingest"><Icon name="plus" /><span>Add document</span></a>
        {/if}
      </div>
    </header>

    <main class="view">
      {#if session.expired}
        <div class="banner" role="alert">
          <span><strong>Your sign-in expired.</strong> Queued jobs keep running.</span>
          <button class="btn primary" onclick={() => session.signOut()}>Sign in again</button>
        </div>
      {/if}
      {#if libraries.error}<div class="banner" role="alert"><span>{libraries.error}</span></div>{/if}
      {#if router.view === "people"}<PeopleView />{:else if router.view === "allgroups"}<AdminGroupsView />{:else if router.view === "groups"}{#if router.docId}<GroupView />{:else}<GroupsView />{/if}{:else if router.view === "signin"}<SignInView />{:else if router.view === "ingest"}<IngestView />{:else if router.view === "members"}<MembersView />{:else if router.view === "search"}<SearchView />{:else if router.view === "read"}<ReaderView />{:else}<LibraryView />{/if}
    </main>
  </div>
{/if}
{#if toast.message}<div class="toast" role="status">{toast.message}</div>{/if}
