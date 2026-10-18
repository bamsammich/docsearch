import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("./api", () => ({ authHeaders: () => ({}), refusal: () => "" }));
vi.mock("./session.svelte", () => ({ session: { rejected: () => undefined } }));

const { blockedTab, openOriginal } = await import("./original");

afterEach(() => vi.unstubAllGlobals());

describe("openOriginal", () => {
  it("says so when the browser blocks the tab, and fetches nothing", async () => {
    const fetcher = vi.fn();
    vi.stubGlobal("window", { open: () => null });
    vi.stubGlobal("fetch", fetcher);

    await expect(openOriginal("manual")).rejects.toThrow(blockedTab);
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("closes the tab it opened when the file can't be read", async () => {
    const tab = { close: vi.fn(), opener: {}, location: { href: "" } };
    vi.stubGlobal("window", { open: () => tab });
    vi.stubGlobal("fetch", async () => new Response("no such document", { status: 404 }));

    await expect(openOriginal("gone")).rejects.toThrow("no such document");
    expect(tab.close).toHaveBeenCalled();
  });
});
