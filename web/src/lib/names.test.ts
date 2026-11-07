import { describe, expect, it } from "vitest";
import { initialOf, labelOf, nameOf, own, slugOf } from "./names";

describe("slugOf", () => {
  it("joins lower-case words with hyphens", () => {
    expect(slugOf("Platform docs")).toBe("platform-docs");
  });

  it("drops what agents cannot type", () => {
    expect(slugOf("  Field & Ops — 2026! ")).toBe("field-ops-2026");
    expect(slugOf("Café notes")).toBe("cafe-notes");
  });

  it("keeps an accented letter as its base letter", () => {
    expect(slugOf("Équipe Ünïcode")).toBe("equipe-unicode");
  });

  it("is empty for a name with no letter or digit", () => {
    expect(slugOf("—!—")).toBe("");
  });

  it("is cut to the 40 characters the server takes, ending on a letter or digit", () => {
    expect(slugOf(`${"a".repeat(39)} b`)).toBe("a".repeat(39));
    expect(slugOf("x".repeat(50))).toBe("x".repeat(40));
  });
});

describe("initialOf", () => {
  it("is the first letter or digit, upper-cased", () => {
    expect(initialOf("  runbooks")).toBe("R");
    expect(initialOf("2026 plans")).toBe("2");
  });

  it("reads letters in any script", () => {
    expect(initialOf("運用手順")).toBe("運");
  });

  it("is a question mark when there is none", () => {
    expect(initialOf("—!—")).toBe("?");
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
