// What the server tells the UI about itself before anyone signs in.

export type AuthMode = "none" | "token" | "oidc" | "password";

export interface UiConfig {
  auth: AuthMode;
  /** Where to sign in: the provider in oidc mode, docsearch itself in password mode. */
  issuer: string;
  /** The public client the browser signs in as, in oidc mode. */
  clientId: string;
  scopes: string[];
  /** docsearch's /mcp URI, which a token's audience has to name. */
  resource: string;
  /** Where the server expects the UI to be opened. */
  publicUrl: string;
  /** Whether the address this page was opened at is one the server accepts. */
  originAllowed: boolean;
  uploadMaxMB: number;
}

interface WireConfig {
  auth?: string;
  issuer?: string;
  client_id?: string;
  scopes?: string[];
  resource?: string;
  public_url?: string;
  origin_allowed?: boolean;
  upload_max_mb?: number;
}

const modes: readonly AuthMode[] = ["none", "token", "oidc", "password"];

export function parseConfig(wire: WireConfig): UiConfig {
  const auth = modes.find((m) => m === wire.auth);
  if (!auth) {
    throw new Error(`the server reported an auth mode this page doesn't know: ${String(wire.auth)}`);
  }
  return {
    auth,
    issuer: wire.issuer ?? "",
    clientId: wire.client_id ?? "",
    scopes: wire.scopes ?? [],
    resource: wire.resource ?? "",
    publicUrl: wire.public_url ?? "",
    // An older server says nothing; assume the address is fine rather than warn.
    originAllowed: wire.origin_allowed ?? true,
    uploadMaxMB: wire.upload_max_mb ?? 0,
  };
}

export async function loadConfig(fetcher: typeof fetch = fetch): Promise<UiConfig> {
  const response = await fetcher("/ui/config", { headers: { Accept: "application/json" } });
  if (!response.ok) {
    throw new Error(`couldn't load the server's settings (${response.status})`);
  }
  return parseConfig((await response.json()) as WireConfig);
}
