<script lang="ts">
  import { loadConfig } from "./lib/config";
  import { library } from "./lib/library.svelte";
  import { queue } from "./lib/queue.svelte";
  import { router } from "./lib/router.svelte";
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

  $effect(() => {
    loadConfig()
      .then((config) => session.start(config))
      .catch((err: unknown) => (bootError = err instanceof Error ? err.message : String(err)));
  });

  // Signing in loads the library and starts watching the queue for the tab counts.
  $effect(() => {
    if (session.status !== "signed-in") return;
    void library.load();
    return queue.watch();
  });

  const attention = $derived(library.docs.filter((d) => library.needsAttention(d)).length);

  // A finished ingest changes the library: reload it whichever tab is open.
  let finished = -1;
  $effect(() => {
    const done = queue.done.filter((j) => j.status === "done").length;
    if (finished >= 0 && done !== finished) void library.load();
    finished = done;
  });
  const ingestAlert = $derived(queue.failed + queue.stalled);
  const ingestCount = $derived(ingestAlert || queue.active);
  const config = $derived(session.config);
  const titles = { library: "Library", ingest: "Ingest", search: "Search preview", read: "Reader" } as const;

  const instance = $derived(config?.instanceLabel ?? "");

  $effect(() => {
    const page = session.status === "signed-in" ? `${titles[router.view]} · docsearch` : "docsearch";
    document.title = instance ? `${instance} · ${page}` : page;
  });
</script>

{#if instance}
  <div class="instance" role="note">
    <Icon name="alert" /><strong>{instance}</strong><span class="mono">{location.host}</span>
  </div>
{/if}
<div class="shell">
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
    <header class="appbar">
      <div class="appbar-in">
        <a class="appbar-brand" href="#/library" aria-label="docsearch library"><Brand /></a>
        <nav class="sections" aria-label="Sections">
          <a class="section" href="#/library" aria-current={router.view === "library" || router.view === "read" ? "page" : undefined}>
            <Icon name="library" /><span>Library</span>
            {#if attention}<span class="count alert" aria-label={`${attention} need attention`}>{attention}</span>{/if}
          </a>
          <a class="section" href="#/ingest" aria-current={router.view === "ingest" ? "page" : undefined}>
            <Icon name="inbox" /><span>Ingest</span>
            {#if ingestCount}
              <span class="count" class:alert={ingestAlert > 0}
                aria-label={ingestAlert ? `${ingestAlert} need attention` : `${queue.active} active`}>{ingestCount}</span>
            {/if}
          </a>
          <a class="section" href="#/search" aria-current={router.view === "search" ? "page" : undefined}>
            <Icon name="search" /><span>Search</span>
          </a>
        </nav>
        <div class="appbar-end">
          {#if router.view !== "ingest"}
            <a class="btn primary small add" href="#/ingest" aria-label="Add document"><Icon name="plus" /><span>Add document</span></a>
          {/if}
          <AccountMenu />
        </div>
      </div>
    </header>
    {#if session.expired}
      <div class="banner" role="alert">
        <span><strong>Your sign-in expired.</strong> Queued jobs keep running.</span>
        <button class="btn primary" onclick={() => session.signOut()}>Sign in again</button>
      </div>
    {/if}
    <main class="view">
      {#if router.view === "ingest"}<IngestView />{:else if router.view === "search"}<SearchView />{:else if router.view === "read"}<ReaderView />{:else}<LibraryView />{/if}
    </main>
  {/if}
</div>
{#if toast.message}<div class="toast" role="status">{toast.message}</div>{/if}
