<script lang="ts">
  import { Code, ConnectError } from "@connectrpc/connect";
  import type { Library } from "../gen/docsearch/library/v1/library_pb";
  import { problem } from "../lib/api";
  import { libraries, nameOf, own, slugOf } from "../lib/libraries.svelte";
  import { router } from "../lib/router.svelte";
  import { toast } from "../lib/toast.svelte";
  import Icon from "./Icon.svelte";

  // Two pickers share the libraries: one heads the side navigation, and the
  // other takes the top bar on a phone, where the side becomes a tab bar.
  let { id, where }: { id: string; where: "side" | "top" } = $props();

  let pop: HTMLElement | undefined = $state();
  let trigger: HTMLButtonElement | undefined = $state();
  let findField: HTMLInputElement | undefined = $state();
  let nameField: HTMLInputElement | undefined = $state();
  let find = $state("");
  let creating = $state(false);
  let name = $state("");
  let slug = $state("");
  let editingSlug = $state(false);
  let error = $state("");
  let busy = $state(false);

  // Beyond a handful, typing beats scrolling.
  const findable = $derived(libraries.all.length > 6);
  const wanted = $derived(find.trim().toLowerCase());
  const shown = $derived(
    libraries.all.filter((l) => !wanted || nameOf(l).toLowerCase().includes(wanted) || l.library.includes(wanted)),
  );
  const ownShown = $derived(shown.filter((l) => l.library === own));
  const shared = $derived(shown.filter((l) => l.library !== own));
  const agentName = $derived(editingSlug ? slug.trim().toLowerCase() : slugOf(name));

  const roles: Record<string, string> = { admin: "Admin", editor: "Editor", reader: "Reader" };
  function roleOf(lib: Library | undefined): string {
    if (!lib || lib.library === own) return "Only you";
    return roles[lib.role] ?? lib.role;
  }

  function initial(lib: Library | undefined): string {
    return (nameOf(lib).match(/[a-z0-9]/i)?.[0] ?? "?").toUpperCase();
  }

  function reset(): void {
    find = "";
    creating = false;
    name = "";
    slug = "";
    editingSlug = false;
    error = "";
  }

  // The menu opens under its button, wherever the button sits.
  function placing(event: ToggleEvent): void {
    if (event.newState !== "open" || !trigger || !pop) return;
    const at = trigger.getBoundingClientRect();
    pop.style.top = `${Math.round(at.bottom + 6)}px`;
    if (where === "side") pop.style.left = `${Math.round(at.left)}px`;
  }

  function toggled(event: ToggleEvent): void {
    if (event.newState === "open") {
      if (findable) findField?.focus();
    } else {
      reset();
    }
  }

  function pick(label: string): void {
    const moved = label !== libraries.label;
    libraries.choose(label);
    pop?.hidePopover();
    // A document or a search belongs to the library it came from.
    if (moved && (router.view === "read" || router.docId)) router.go("library");
  }

  function startCreating(): void {
    creating = true;
    queueMicrotask(() => nameField?.focus());
  }

  function changeSlug(): void {
    slug = agentName;
    editingSlug = true;
  }

  function refusal(err: unknown, label: string): string {
    if (err instanceof ConnectError && err.code === Code.AlreadyExists)
      return `Another library is already called ${label}. Change the name agents use.`;
    if (err instanceof ConnectError && err.code === Code.InvalidArgument)
      return "The name agents use takes lower-case letters, digits and hyphens, and can't be personal.";
    return problem(err);
  }

  async function create(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    error = "";
    const label = agentName;
    if (!name.trim()) {
      error = "Type a name for the library.";
      nameField?.focus();
      return;
    }
    if (!label) {
      error = "The name agents use needs a letter or a digit.";
      editingSlug = true;
      return;
    }
    busy = true;
    try {
      await libraries.create(label, name.trim());
      toast.show(`Created ${name.trim()}. You're its admin.`);
      pop?.hidePopover();
      router.go("members");
    } catch (err) {
      error = refusal(err, label);
    } finally {
      busy = false;
    }
  }
</script>

<button class="picker at-{where}" popovertarget={id} bind:this={trigger} aria-label={`Library: ${libraries.name}. Switch library`}>
  <span class="tile current" aria-hidden="true">{initial(libraries.current)}</span>
  <span class="picker-text">
    <span class="picker-name">{libraries.name}</span>
    <span class="picker-role">{roleOf(libraries.current)}</span>
  </span>
  <Icon name="selector" />
</button>
<div {id} popover class="picker-pop at-{where}" bind:this={pop} onbeforetoggle={placing} ontoggle={toggled}>
  {#if findable}
    <label class="picker-find">
      <Icon name="search" /><span class="sr-only">Find a library</span>
      <input type="search" placeholder="Find a library" autocomplete="off" bind:value={find} bind:this={findField} />
    </label>
  {/if}
  <ul class="picker-list" aria-label="Libraries">
    {#each ownShown as lib (lib.library)}{@render option(lib)}{/each}
    {#if shared.length}<li class="picker-group" aria-hidden="true">Shared with you</li>{/if}
    {#each shared as lib (lib.library)}{@render option(lib)}{/each}
    {#if !shown.length}<li class="picker-none">No library matches “{find.trim()}”.</li>{/if}
  </ul>
  <div class="picker-foot">
    {#if creating}
      <form class="picker-new" onsubmit={create} novalidate>
        <label class="lbl" for={`${id}-name`}>New library</label>
        <input class="input" id={`${id}-name`} type="text" maxlength="120" placeholder="Platform docs" autocomplete="off"
          bind:value={name} bind:this={nameField} oninput={() => (error = "")} aria-invalid={error ? "true" : undefined}
          aria-describedby={error ? `${id}-err` : undefined} />
        {#if editingSlug}
          <label class="lbl" for={`${id}-slug`}>Name agents use</label>
          <input class="input mono" id={`${id}-slug`} type="text" maxlength="63" autocomplete="off" spellcheck="false"
            bind:value={slug} oninput={() => (error = "")} />
        {:else if agentName}
          <p class="hint">Agents name it <span class="mono">{agentName}</span> · <button type="button" class="linkish" onclick={changeSlug}>Change</button></p>
        {:else if name.trim()}
          <p class="hint"><button type="button" class="linkish" onclick={changeSlug}>Choose the name agents use</button></p>
        {/if}
        {#if error}<p class="err" id={`${id}-err`} role="alert">{error}</p>{/if}
        <div class="row-btns">
          <button class="btn primary small" type="submit" disabled={busy}>Create library</button>
          <button class="btn quiet small" type="button" onclick={() => ((creating = false), (error = ""))}>Cancel</button>
        </div>
      </form>
    {:else}
      <button class="picker-action" onclick={startCreating}><Icon name="plus" />New library</button>
    {/if}
  </div>
</div>

{#snippet option(lib: Library)}
  <li>
    <button class="picker-opt" aria-current={lib.library === libraries.label ? "true" : undefined} onclick={() => pick(lib.library)}>
      <span class="tile" class:current={lib.library === libraries.label} aria-hidden="true">{initial(lib)}</span>
      <span class="opt-name">{nameOf(lib)}</span>
      <span class="opt-role">{roleOf(lib)}</span>
    </button>
  </li>
{/snippet}
