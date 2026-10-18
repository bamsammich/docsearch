import { describe, expect, it } from "vitest";
import { costOf, coverageOf, findingName, headline } from "./coverage";

describe("coverageOf", () => {
  it("counts pages no chunk claims as lost", () => {
    const c = coverageOf({ pageCount: 240, chunkCount: 31, uncoveredPages: 212, headlessChunks: 0, findingCodes: [] });
    expect(c).toEqual({ byChapter: 28, unreliable: 0, lost: 212, unit: "pages" });
    expect(headline(c)).toBe("Agents reach 28 of 240 pages.");
  });

  it("marks every reachable page unreliable when chapters cannot be told apart", () => {
    const c = coverageOf({ pageCount: 70, chunkCount: 35, uncoveredPages: 0, headlessChunks: 4, findingCodes: ["unaddressable"] });
    expect(c).toEqual({ byChapter: 0, unreliable: 70, lost: 0, unit: "pages" });
    expect(headline(c)).toBe("Agents reach all 70 pages, but can't narrow a search to one chapter.");
  });

  it("falls back to chunks for a document without pages", () => {
    const c = coverageOf({ chunkCount: 1872, uncoveredPages: 0, headlessChunks: 0, findingCodes: ["oversized"] });
    expect(c).toEqual({ byChapter: 1872, unreliable: 0, lost: 0, unit: "chunks" });
    expect(headline(c)).toBe("Agents reach all 1,872 chunks by chapter.");
  });

  it("never reports more pages lost than the document has", () => {
    const c = coverageOf({ pageCount: 3, chunkCount: 0, uncoveredPages: 9, headlessChunks: 0, findingCodes: [] });
    expect(c.lost).toBe(3);
    expect(headline(c)).toBe("Agents reach 0 of 3 pages.");
  });

  it("counts headless chunks as unreliable in a document measured in chunks", () => {
    const c = coverageOf({ chunkCount: 12, uncoveredPages: 0, headlessChunks: 3, findingCodes: [] });
    expect(c).toEqual({ byChapter: 9, unreliable: 3, lost: 0, unit: "chunks" });
  });

  it("marks a paginated document unreliable only when no chunk has a heading", () => {
    const some = coverageOf({ pageCount: 20, chunkCount: 10, uncoveredPages: 0, headlessChunks: 4, findingCodes: [] });
    expect(some.unreliable).toBe(0);
    const all = coverageOf({ pageCount: 20, chunkCount: 10, uncoveredPages: 2, headlessChunks: 10, findingCodes: [] });
    expect(all).toEqual({ byChapter: 0, unreliable: 18, lost: 2, unit: "pages" });
  });

  it("says so when nothing reached the index", () => {
    expect(headline(coverageOf({ pageCount: 0, chunkCount: 0, uncoveredPages: 0, headlessChunks: 0, findingCodes: [] }))).toBe(
      "Nothing from this document reached the index.",
    );
  });
});

describe("finding copy", () => {
  it("names a code and states its cost", () => {
    expect(findingName("flat_hierarchy")).toBe("Flat hierarchy");
    expect(costOf("unaddressable")).toContain("section_filter");
    expect(costOf("a_code_from_a_newer_server")).toBe("");
  });
});
