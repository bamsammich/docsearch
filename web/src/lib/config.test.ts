import { describe, expect, it } from "vitest";
import { loadConfig, parseConfig } from "./config";

describe("parseConfig", () => {
  it("reads the server's snake_case description", () => {
    expect(
      parseConfig({ auth: "oidc", issuer: "https://id.example", client_id: "ui", scopes: ["openid"], resource: "https://d.example/mcp", upload_max_mb: 100 }),
    ).toEqual({
      auth: "oidc",
      issuer: "https://id.example",
      clientId: "ui",
      scopes: ["openid"],
      resource: "https://d.example/mcp",
      publicUrl: "",
      originAllowed: true,
      uploadMaxMB: 100,
    });
  });

  it("reads whether this page's address is accepted", () => {
    expect(parseConfig({ auth: "token", public_url: "http://d:1", origin_allowed: false })).toMatchObject({
      publicUrl: "http://d:1",
      originAllowed: false,
    });
  });

  it("refuses a mode it does not know rather than guessing", () => {
    expect(() => parseConfig({ auth: "kerberos" })).toThrow(/auth mode/);
  });
});

describe("loadConfig", () => {
  it("reports a server that would not answer", async () => {
    const fetcher = (async () => new Response("", { status: 503 })) as typeof fetch;
    await expect(loadConfig(fetcher)).rejects.toThrow(/503/);
  });
});
