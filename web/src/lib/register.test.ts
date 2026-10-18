import { describe, expect, it } from "vitest";
import { forgetClient, registeredClient } from "./register";

function memory(): Storage {
  const items = new Map<string, string>();
  return {
    get length() {
      return items.size;
    },
    clear: () => items.clear(),
    getItem: (k) => items.get(k) ?? null,
    key: (i) => [...items.keys()][i] ?? null,
    removeItem: (k) => void items.delete(k),
    setItem: (k, v) => void items.set(k, v),
  };
}

describe("registeredClient", () => {
  it("registers once as a public client and remembers the id", async () => {
    const storage = memory();
    const bodies: unknown[] = [];
    const fetcher = (async (url: string, init?: RequestInit) => {
      expect(url).toBe("https://docs.example.org/register");
      bodies.push(JSON.parse(String(init?.body)));
      return new Response(JSON.stringify({ client_id: "c-1" }), { status: 201 });
    }) as typeof fetch;

    expect(await registeredClient("https://docs.example.org", "https://docs.example.org", fetcher, storage)).toBe("c-1");
    expect(await registeredClient("https://docs.example.org", "https://docs.example.org", fetcher, storage)).toBe("c-1");
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toMatchObject({
      redirect_uris: ["https://docs.example.org/"],
      token_endpoint_auth_method: "none",
    });
  });

  it("registers again after the client is forgotten", async () => {
    const storage = memory();
    let n = 0;
    const fetcher = (async () => new Response(JSON.stringify({ client_id: `c-${++n}` }))) as typeof fetch;
    await registeredClient("https://d", "https://d", fetcher, storage);
    forgetClient("https://d", storage);
    expect(await registeredClient("https://d", "https://d", fetcher, storage)).toBe("c-2");
  });

  it("reports a refused registration with the server's words", async () => {
    const fetcher = (async () => new Response("redirect URI must be https", { status: 400 })) as typeof fetch;
    await expect(registeredClient("https://d", "http://evil", fetcher, memory())).rejects.toThrow(/400.*https/);
  });
});
