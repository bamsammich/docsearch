// Who the browser is signed in as, and the bearer it sends.
//
// The browser is one more client of the same protected resource as an MCP
// client: it presents the same kind of token on every request, and the
// server keeps no session for it. In token mode that is the server token,
// kept in this tab's sessionStorage. In oidc mode it is an access token from
// the provider, and in password mode one from docsearch's own authorization
// server, both got through authorization code with PKCE.

import { UserManager, WebStorageStateStore, type User } from "oidc-client-ts";
import type { UiConfig } from "./config";
import { forgetClient, registeredClient } from "./register";

const tokenKey = "docsearch.token";

export type SessionStatus = "loading" | "signed-out" | "signed-in" | "unconfigured" | "error";

function readToken(): string {
  try {
    return sessionStorage.getItem(tokenKey) ?? "";
  } catch {
    return "";
  }
}

function writeToken(token: string): void {
  try {
    if (token) sessionStorage.setItem(tokenKey, token);
    else sessionStorage.removeItem(tokenKey);
  } catch {
    // Storage blocked: the token lives for this page load only.
  }
}

class Session {
  status = $state<SessionStatus>("loading");
  /** The name shown for whoever is signed in. */
  who = $state("");
  /** Set when the server answered 401 to a token that used to work. */
  expired = $state(false);
  error = $state("");
  config = $state<UiConfig | null>(null);
  #token = "";
  #oidc: UserManager | null = null;

  get token(): string {
    return this.#token;
  }

  async start(config: UiConfig): Promise<void> {
    this.config = config;
    if (config.auth === "none") {
      this.who = "Local server";
      this.status = "signed-in";
      return;
    }
    if (config.auth === "token") {
      this.#token = readToken();
      this.who = "the server token";
      this.status = this.#token ? "signed-in" : "signed-out";
      return;
    }
    try {
      const clientId = config.auth === "password" ? await registeredClient(config.issuer, location.origin) : config.clientId;
      if (!config.issuer || !clientId) {
        this.status = "unconfigured";
        return;
      }
      // An oidc provider is asked for a name to show; docsearch's own server
      // grants only its one scope.
      const asked = config.auth === "oidc" ? ["openid", "profile", "email"] : ["openid"];
      this.#oidc = new UserManager({
        authority: config.issuer,
        client_id: clientId,
        redirect_uri: `${location.origin}/`,
        response_type: "code",
        scope: [...asked, ...config.scopes.filter((s) => !asked.includes(s))].join(" "),
        userStore: new WebStorageStateStore({ store: sessionStorage }),
        extraQueryParams: config.resource ? { resource: config.resource } : {},
        loadUserInfo: false,
        // A refresh token, when the server issues one, renews the access
        // token before it expires instead of signing the person out.
        automaticSilentRenew: true,
      });
      this.#oidc.events.addUserLoaded((user) => this.#adopt(user));
      const params = new URLSearchParams(location.search);
      if (params.has("code") || params.has("error")) {
        try {
          await this.#oidc.signinRedirectCallback();
        } finally {
          history.replaceState(null, "", `/${location.hash}`);
        }
      }
      const user = await this.#oidc.getUser();
      if (user && !user.expired) {
        this.#adopt(user);
      } else {
        this.status = "signed-out";
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      // A client the server has forgotten, after its database was reset,
      // cannot sign in; registering again can.
      if (config.auth === "password" && /client/i.test(message)) forgetClient(config.issuer, location.origin);
      this.error = message;
      this.status = "error";
    }
  }

  #adopt(user: User): void {
    this.#token = user.access_token;
    const p = user.profile;
    this.who = String(p.email ?? p.preferred_username ?? p.name ?? "your account");
    this.expired = false;
    this.status = "signed-in";
  }

  /**
   * In token mode: try a pasted token with one request, and keep it only if
   * the server accepts it. Nothing changes on screen until it has.
   */
  async tryToken(token: string, check: () => Promise<unknown>): Promise<void> {
    this.#token = token.trim();
    try {
      await check();
    } catch (err) {
      this.#token = "";
      this.expired = false;
      throw err;
    }
    writeToken(this.#token);
    this.expired = false;
    this.status = "signed-in";
  }

  async signIn(): Promise<void> {
    if (this.#oidc) await this.#oidc.signinRedirect();
  }

  async signOut(): Promise<void> {
    this.#token = "";
    this.expired = false;
    writeToken("");
    if (this.#oidc) await this.#oidc.removeUser();
    this.status = this.config?.auth === "none" ? "signed-in" : "signed-out";
  }

  /** Any request answered 401 lands here. */
  rejected(): void {
    if (this.config?.auth === "none") return;
    this.expired = true;
  }
}

export const session = new Session();
