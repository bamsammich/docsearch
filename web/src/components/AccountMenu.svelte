<script lang="ts">
  import { session } from "../lib/session.svelte";
  import Icon from "./Icon.svelte";

  const config = $derived(session.config);
  const issuerHost = $derived(config?.issuer ? new URL(config.issuer).host : "");
  // One letter for the button: the account's own name, or the mode's.
  const initial = $derived(
    config?.auth === "token" ? "T" : (session.who.match(/[a-z0-9]/i)?.[0] ?? "?").toUpperCase(),
  );
  const who = $derived(
    config?.auth === "token" ? "Server token" : config?.auth === "none" ? "Local server" : session.who,
  );
  const how = $derived(
    {
      none: "No sign-in. Local access only.",
      token: "Signed in with the server token.",
      oidc: `Signed in through ${issuerHost}.`,
      password: "Signed in with a docsearch account.",
    }[config?.auth ?? "token"],
  );
</script>

<button class="account-btn" popovertarget="account-menu" aria-label={`Account: ${who}`}>
  <span class="avatar" aria-hidden="true">{initial}</span>
  <span class="account-name">{who}</span>
  <Icon name="chevron" />
</button>
<div id="account-menu" popover class="account-pop">
  <p class="account-who">{who}</p>
  <p class="account-how">{how}</p>
  <p class="account-where mono">{location.host}</p>
  {#if config?.auth !== "none"}
    <button class="btn block" onclick={() => session.signOut()}>Sign out</button>
  {/if}
</div>
