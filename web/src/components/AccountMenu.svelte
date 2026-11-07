<script lang="ts">
  import { admin } from "../lib/admin.svelte";
  import { groupsStore as groups } from "../lib/groups.svelte";
  import { initialOf } from "../lib/names";
  import { session } from "../lib/session.svelte";
  import Icon from "./Icon.svelte";

  const config = $derived(session.config);
  const issuerHost = $derived(config?.issuer ? new URL(config.issuer).host : "");
  // The account's username once docsearch says it: a password sign-in's
  // token carries no name of its own.
  const who = $derived(
    config?.auth === "token" ? "Server token"
      : config?.auth === "none" ? "Local server"
      : admin.username || session.who,
  );
  // One letter for the button: the account's own name, or the mode's.
  const initial = $derived(config?.auth === "token" ? "T" : initialOf(who));
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
  {#if config?.auth === "password" || config?.auth === "oidc"}
    <a class="btn block" href="#/groups" onclick={() => document.getElementById("account-menu")?.hidePopover()}><Icon name="group" /> Groups
      {#if groups.invitations.length}<span class="menu-count" aria-label={`${groups.invitations.length} invitations`}>{groups.invitations.length}</span>{/if}
    </a>
  {/if}
  {#if admin.available}
    <a class="btn block" href="#/people" onclick={() => document.getElementById("account-menu")?.hidePopover()}><Icon name="shield" /> Administration</a>
  {/if}
  {#if config?.auth !== "none"}
    <button class="btn block" onclick={() => session.signOut()}>Sign out</button>
  {/if}
</div>
