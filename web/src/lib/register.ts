// In password mode docsearch is its own authorization server, and a page has
// no client registered for it ahead of time. It registers itself once, as a
// public PKCE client (RFC 7591), and keeps the client id in this browser.
//
// The server answers at more than one origin, and each client carries a
// redirect URI for the origin that registered it. The stored id is keyed by
// origin too, so opening the library at a second origin registers a second
// client instead of reusing one whose redirect URI the server would refuse.

const storageKey = (issuer: string, origin: string) => `docsearch.client:${issuer}:${origin}`;

// Ids stored before the key carried the origin may belong to any origin.
const legacyKey = (issuer: string) => `docsearch.client:${issuer}`;

interface Registration {
  client_id?: string;
}

function remembered(storage: Storage | undefined, issuer: string, origin: string): string {
  try {
    storage?.removeItem(legacyKey(issuer));
    return storage?.getItem(storageKey(issuer, origin)) ?? "";
  } catch {
    return "";
  }
}

export async function registeredClient(
  issuer: string,
  origin: string,
  fetcher: typeof fetch = fetch,
  storage: Storage | undefined = globalThis.localStorage,
): Promise<string> {
  const known = remembered(storage, issuer, origin);
  if (known) return known;
  const response = await fetcher(`${issuer}/register`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify({
      client_name: "docsearch library",
      redirect_uris: [`${origin}/`],
      grant_types: ["authorization_code", "refresh_token"],
      response_types: ["code"],
      token_endpoint_auth_method: "none",
    }),
  });
  if (!response.ok) {
    throw new Error(`docsearch would not register this page for sign-in (${response.status}): ${(await response.text()).trim()}`);
  }
  const id = ((await response.json()) as Registration).client_id;
  if (!id) throw new Error("docsearch registered this page but returned no client ID");
  try {
    storage?.setItem(storageKey(issuer, origin), id);
  } catch {
    // Storage blocked: the page registers again on its next load.
  }
  return id;
}

/** Drop a client the server no longer knows, so the next sign-in registers anew. */
export function forgetClient(
  issuer: string,
  origin: string,
  storage: Storage | undefined = globalThis.localStorage,
): void {
  try {
    storage?.removeItem(storageKey(issuer, origin));
  } catch {
    // Nothing stored, or storage blocked: nothing to forget.
  }
}
