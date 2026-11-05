<script lang="ts">
  import { loadConfig } from "./lib/config";
  import { library } from "./lib/library.svelte";
  import { queue } from "./lib/queue.svelte";
  import { router } from "./lib/router.svelte";
  import { search } from "./lib/search.svelte";
  import { session } from "./lib/session.svelte";
  import { toast } from "./lib/toast.svelte";
  import AccountMenu from "./components/AccountMenu.svelte";
  import Brand from "./components/Brand.svelte";
  import Icon from "./components/Icon.svelte";
  import IngestView from "./components/IngestView.svelte";
  import LibraryView from "./components/LibraryView.svelte";
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

  // Signing in loads the library and starts watching the queue for the counts.
  $effect(() => {
    if (session.status !== "signed-in") return;
    void library.load();
    return queue.watch();
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
  const titles = { library: "Library", ingest: "Ingest", search: "Search", read: "Reader" } as const;

  const instance = $derived(config?.instanceLabel ?? "");

  $effect(() => {
    const page = session.status === "signed-in" ? `${titles[router.view]} · docsearch` : "docsearch";
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
      <nav class="sections" aria-label="Sections">
        <a class="section" href="#/library" aria-current={router.view === "library" || router.view === "read" ? "page" : undefined}>
          <Icon name="library" /><span class="section-name">Library</span>
          {#if attention}<span class="count hot" aria-label={`${attention} need attention`}>{attention}</span>{/if}
        </a>
        <a class="section" href="#/ingest" aria-current={router.view === "ingest" ? "page" : undefined}>
          <Icon name="inbox" /><span class="section-name">Ingest</span>
          {#if ingestCount}
            <span class="count" class:hot={ingestAlert > 0}
              aria-label={ingestAlert ? `${ingestAlert} need attention` : `${queue.active} active`}>{ingestAlert ? ingestAlert : `${queue.active} running`}</span>
          {/if}
        </a>
      </nav>
      <div class="side-foot"><AccountMenu /></div>
    </aside>

    <header class="topbar">
      <a class="top-brand" href="#/library" aria-label="docsearch library"><Brand /></a>
      <form class="gsearch" role="search" onsubmit={submit} novalidate>
        <Icon name="search" />
        <label class="sr-only" for="g-q">Search the library</label>
        <input id="g-q" type="search" placeholder="Search the library" autocomplete="off"
          bind:value={search.query} bind:this={field} />
        <label class="sr-only" for="g-scope">Search in</label>
        <select id="g-scope" class="scope" bind:value={search.docId}>
          <option value="">All documents</option>
          {#each library.docs as d (d.docId)}<option value={d.docId}>{d.title}</option>{/each}
        </select>
        <kbd class="key" aria-hidden="true">/</kbd>
      </form>
      <div class="top-end">
        {#if router.view !== "ingest"}
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
      {#if router.view === "ingest"}<IngestView />{:else if router.view === "search"}<SearchView />{:else if router.view === "read"}<ReaderView />{:else}<LibraryView />{/if}
    </main>
  </div>
{/if}
{#if toast.message}<div class="toast" role="status">{toast.message}</div>{/if}
