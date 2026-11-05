import { describe, expect, it } from "vitest";
import { labelOf, nameOf, own, slugOf } from "./names";

describe("slugOf", () => {
  it("joins lower-case words with hyphens", () => {
    expect(slugOf("Platform docs")).toBe("platform-docs");
  });

  it("drops what agents cannot type", () => {
    expect(slugOf("  Field & Ops — 2026! ")).toBe("field-ops-2026");
    expect(slugOf("Café notes")).toBe("cafe-notes");
  });

  it("is empty for a name with no letter or digit", () => {
    expect(slugOf("—!—")).toBe("");
  });

  it("ends on a letter or digit when a long name is cut", () => {
    const slug = slugOf(`${"a".repeat(62)} b`);
    expect(slug).toBe("a".repeat(62));
  });
});

describe("labelOf", () => {
  it("is the library a qualified document id names", () => {
    expect(labelOf("runbooks/abc123")).toBe("runbooks");
  });

  it("is the account's own library for a bare id", () => {
    expect(labelOf("abc123")).toBe(own);
    expect(labelOf("")).toBe(own);
  });
});

describe("nameOf", () => {
  it("calls the account's own library Personal", () => {
    expect(nameOf({ library: own, name: "", role: "admin" } as never)).toBe("Personal");
  });

  it("falls back to the label when a library has no name", () => {
    expect(nameOf({ library: "runbooks", name: "", role: "reader" } as never)).toBe("runbooks");
  });
});
