<script lang="ts">
  import { Code, ConnectError } from "@connectrpc/connect";
  import { documents, forbiddenOrigin, problem } from "../lib/api";
  import { session } from "../lib/session.svelte";
  import Brand from "./Brand.svelte";
  import Icon from "./Icon.svelte";

  interface Notice {
    title: string;
    body: string;
  }

  let token = $state("");
  let reveal = $state(false);
  let notice = $state<Notice | null>(null);
  let checking = $state(false);
  let leaving = $state(false);
  let field: HTMLInputElement | undefined = $state();

  const config = $derived(session.config);
  const issuerHost = $derived(config?.issuer ? new URL(config.issuer).host : "");
  const publicHref = $derived(config?.publicUrl || "");
  const publicHost = $derived(publicHref ? new URL(publicHref).host : "");
  const modeLabel = $derived(
    { none: "no sign-in", token: "token sign-in", oidc: `sign-in at ${issuerHost}`, password: "docsearch accounts" }[
      config?.auth ?? "token"
    ],
  );

  $effect(() => {
    if (config?.auth === "token" && matchMedia("(pointer: fine)").matches) field?.focus();
  });

  function explain(err: unknown): Notice {
    // A refused credential gets one answer, whatever was wrong with it: the
    // page says nothing about the token tried or how the server holds its own.
    if (err instanceof ConnectError && err.code === Code.Unauthenticated) {
      return { title: "Sign-in failed", body: "The token was not accepted. Check it and try again." };
    }
    if (err instanceof ConnectError && err.code === Code.PermissionDenied && config?.originAllowed === false) {
      return { title: "This address is not accepted", body: forbiddenOrigin() };
    }
    if (err instanceof ConnectError && err.code === Code.Unavailable) {
      return { title: "docsearch isn't answering", body: `Check that the server at ${location.host} is running.` };
    }
    return { title: "Sign-in failed", body: problem(err) };
  }

  async function useToken(event: SubmitEvent) {
    event.preventDefault();
    notice = null;
    if (!token.trim()) {
      notice = { title: "Paste the token first", body: "" };
      field?.focus();
      return;
    }
    checking = true;
    try {
      await session.tryToken(token, () => documents.list({}));
    } catch (err) {
      notice = explain(err);
      field?.select();
    } finally {
      checking = false;
    }
  }

  async function signIn() {
    leaving = true;
    notice = null;
    try {
      await session.signIn();
    } catch (err) {
      leaving = false;
      notice = explain(err);
    }
  }
</script>

{#snippet addressWarning()}
  {#if config && !config.originAllowed}
    <div class="alert warn" role="alert">
      <Icon name="alert" />
      <div>
        <strong>This address is not accepted</strong>
        <p>
          This server only accepts sign-ins from
          {#if publicHost}<code class="mono">{publicHost}</code>{:else}its public address{/if}.
        </p>
        {#if publicHref}<a class="btn primary block" href={publicHref}>Open {publicHost}</a>{/if}
        <p class="hint">Or add <code class="mono">{location.origin}</code> to <code class="mono">DOCSEARCH_ALLOWED_ORIGINS</code>.</p>
      </div>
    </div>
  {/if}
{/snippet}

<div class="signin">
  <div class="signin-brand">
    <Brand />
    <p>The documentation library your agents search.</p>
  </div>

  <section class="signin-card" aria-labelledby="signin-title">
    <h1 id="signin-title">Sign in</h1>


    {#if session.status === "unconfigured"}
      <p class="signin-lead">
        This server signs people in at <strong>{issuerHost || "an oidc provider"}</strong>, but has no browser client set up for it.
      </p>
      <div class="alert" role="note">
        <Icon name="key" />
        <div>
          <strong>For the operator</strong>
          <p>
            Register a public client at the provider with <code class="mono">{location.origin}/</code> as its redirect URI, then set
            <code class="mono">DOCSEARCH_UI_CLIENT_ID</code> to its client ID.
          </p>
        </div>
      </div>
    {:else if config?.auth === "token"}
      <p class="signin-lead">Use the server token your MCP client sends.</p>
      {@render addressWarning()}
      <form onsubmit={useToken} novalidate>
        <label for="tok">Server token</label>
        <div class="secret">
          <input
            class="input mono"
            id="tok"
            type={reveal ? "text" : "password"}
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            bind:value={token}
            bind:this={field}
            aria-invalid={notice ? "true" : undefined}
            aria-describedby={notice ? "signin-notice" : undefined}
          />
          <button type="button" class="reveal" onclick={() => (reveal = !reveal)} aria-label={reveal ? "Hide token" : "Show token"} aria-pressed={reveal}>
            <Icon name={reveal ? "eye-off" : "eye"} />
          </button>
        </div>
        {#if notice}
          <div class="alert bad" id="signin-notice" role="alert">
            <Icon name="alert" />
            <div><strong>{notice.title}</strong>{#if notice.body}<p>{notice.body}</p>{/if}</div>
          </div>
        {/if}
        <button class="btn primary block" type="submit" disabled={checking}>{checking ? "Checking…" : "Sign in"}</button>
      </form>
      <details class="help">
        <summary><Icon name="chevron" />Where do I find the token?</summary>
        <p>It's the token your MCP client uses for this server. If you don't have it, ask whoever runs the server.</p>
      </details>
    {:else if config?.auth === "oidc" || config?.auth === "password"}
      <p class="signin-lead">
        {#if config.auth === "oidc"}
          Continue to <strong>{issuerHost}</strong> with the account your MCP client uses.
        {:else}
          Continue with your docsearch account, the one your MCP client uses.
        {/if}
      </p>
      {@render addressWarning()}
      {#if notice || session.status === "error"}
        <div class="alert bad" role="alert">
          <Icon name="alert" />
          <div>
            <strong>{notice?.title ?? "Sign-in failed"}</strong>
            <p>{notice?.body ?? session.error}</p>
          </div>
        </div>
      {/if}
      <button class="btn primary block" onclick={signIn} disabled={leaving}>
        {leaving ? "Opening sign-in…" : config.auth === "oidc" ? `Continue to ${issuerHost}` : "Continue to sign in"}
      </button>
    {/if}
  </section>

  <p class="signin-foot"><span class="mono">{location.host}</span> · {modeLabel}</p>
</div>
