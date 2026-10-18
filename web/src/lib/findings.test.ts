import { describe, expect, it } from "vitest";
import { codeSpans, findingsOf } from "./findings";

const none = { byChapter: 0, unreliable: 0, lost: 0, unit: "pages" as const };

describe("findingsOf", () => {
  it("states two chapter findings once, with one remedy", () => {
    const out = findingsOf({
      coverage: { ...none, unreliable: 67, unit: "chunks" },
      findings: [
        { code: "flat_hierarchy", detail: "All 67 chunks sit at heading depth 1." },
        { code: "unaddressable", detail: "67 chunks share only 1 distinct heading paths." },
      ],
      problems: [],
      warnings: ["67 chunks share only 1 distinct heading paths (0.01 per chunk): boundaries came from the budget"],
      chunkCount: 67,
      headlessChunks: 0,
    });
    expect(out.blocks).toHaveLength(1);
    expect(out.blocks[0]).toMatchObject({ name: "Flat hierarchy and Unaddressable", remedy: "inspect" });
    expect(out.blocks[0]?.evidence).toHaveLength(2);
    expect(out.warnings).toEqual([]);
  });

  it("drops the ingest warning about one section holding most of the document", () => {
    const out = findingsOf({
      coverage: { ...none, unreliable: 30, unit: "chunks" },
      findings: [{ code: "unaddressable", detail: "28 of 30 chunks (93%) share one heading path, 'Paper > Body'." }],
      problems: [],
      warnings: ["28 of 30 chunks (93%) share one heading path, 'Paper > Body': their boundaries came from the token budget"],
      chunkCount: 30,
      headlessChunks: 0,
    });
    expect(out.blocks.map((b) => b.key)).toEqual(["chapters"]);
    expect(out.warnings).toEqual([]);
  });

  it("names headless chunks when no finding does", () => {
    const out = findingsOf({
      coverage: { ...none, unreliable: 10, unit: "chunks" },
      findings: [],
      problems: [],
      warnings: ["10 chunk(s) carry no heading path and cannot be reached"],
      chunkCount: 10,
      headlessChunks: 10,
    });
    expect(out.blocks.map((b) => b.key)).toEqual(["headless"]);
    expect(out.blocks[0]?.evidence).toEqual(["10 of 10 chunks have no heading."]);
    expect(out.warnings).toEqual([]);
  });

  it("turns unclaimed pages into a block with re-ingest, and drops the restated problem", () => {
    const problem = "2 page(s) are claimed by no chunk, starting at 1; text was extracted and never stored";
    const out = findingsOf({
      coverage: { byChapter: 2, unreliable: 0, lost: 2, unit: "pages" },
      findings: [],
      problems: [problem, "chunk ordinals skip at [4]"],
      warnings: [],
      chunkCount: 3,
      headlessChunks: 0,
    });
    expect(out.blocks).toEqual([expect.objectContaining({ key: "lost", remedy: "reingest", evidence: [problem] })]);
    expect(out.problems).toEqual(["chunk ordinals skip at [4]"]);
  });

  it("keeps a finding with no remedy as its own block", () => {
    const out = findingsOf({
      coverage: { ...none, byChapter: 9 },
      findings: [{ code: "oversized", detail: "3 of 9 chunks exceed the cap." }],
      problems: [],
      warnings: [],
      chunkCount: 9,
      headlessChunks: 0,
    });
    expect(out.blocks).toEqual([expect.objectContaining({ key: "oversized", remedy: null, name: "Oversized" })]);
  });
});

describe("codeSpans", () => {
  it("marks backtick spans as code", () => {
    expect(codeSpans("`outline` cannot orient and `section_filter` cannot narrow")).toEqual([
      { text: "outline", code: true },
      { text: " cannot orient and ", code: false },
      { text: "section_filter", code: true },
      { text: " cannot narrow", code: false },
    ]);
  });
});
