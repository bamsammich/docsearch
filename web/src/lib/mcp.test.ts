import { describe, expect, it, vi } from "vitest";

vi.mock("./api", () => ({ authHeaders: () => ({}), refusal: () => "" }));
vi.mock("./session.svelte", () => ({ session: { rejected: () => undefined } }));

const { outputOf, replyFrom } = await import("./mcp");

describe("replyFrom", () => {
  it("reads a plain JSON reply", () => {
    expect(replyFrom('{"id":3,"result":{}}', "application/json", 3)).toEqual({ id: 3, result: {} });
  });

  it("finds the reply to this call in an event stream", () => {
    const body = 'event: message\ndata: {"id":1,"method":"notifications/progress"}\n\nevent: message\ndata: {"id":2,"result":{"isError":false}}\n\n';
    expect(replyFrom(body, "text/event-stream", 2)).toEqual({ id: 2, result: { isError: false } });
  });

  it("says so when the stream never answers", () => {
    expect(() => replyFrom("data: {\"id\":9}\n\n", "text/event-stream", 2)).toThrow(/no reply/);
  });
});

describe("outputOf", () => {
  it("prefers structured content", () => {
    expect(outputOf({ structuredContent: { results: [] }, content: [{ type: "text", text: "{}" }] })).toEqual({ results: [] });
  });

  it("parses the text content when that is all there is", () => {
    expect(outputOf({ content: [{ type: "text", text: '{"note":"x"}' }] })).toEqual({ note: "x" });
  });

  it("raises a tool error with the tool's own words", () => {
    expect(() => outputOf({ isError: true, content: [{ type: "text", text: "unknown doc_id" }] })).toThrow("unknown doc_id");
  });
});
