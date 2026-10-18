// What a document page says is wrong: one block per problem, each with its
// evidence, what an agent loses, and the remedy, if any. Built from verify's
// findings, and from coverage when no finding names the gap.

import { costOf, type Coverage, findingName } from "./coverage";

export type Remedy = "inspect" | "reingest";

export interface Block {
  key: string;
  name: string;
  evidence: string[];
  cost: string;
  remedy: Remedy | null;
  hint: string;
}

export interface FindingsInput {
  coverage: Coverage;
  findings: readonly { code: string; detail: string }[];
  problems: readonly string[];
  warnings: readonly string[];
  chunkCount: number;
  headlessChunks: number;
}

export interface Findings {
  blocks: Block[];
  /** Index problems no block already states. */
  problems: string[];
  /** Ingest warnings no block already states. */
  warnings: string[];
}

const chapterless = new Set(["unaddressable", "flat_hierarchy"]);
const chapterCost = costOf("flat_hierarchy");

// Ingest warnings and index problems that restate a block. The wording is
// the server's: StructureReport.Notes in internal/domain/structure.go, and
// the problems in internal/service/document/document.go.
const aboutHeadings = /distinct heading path|no heading path/;
const aboutUnclaimedPages = /claimed by no chunk/;

export function findingsOf(input: FindingsInput): Findings {
  const blocks: Block[] = [];
  const structural = input.findings.filter((f) => chapterless.has(f.code));
  if (structural.length) {
    blocks.push({
      key: "chapters",
      name: structural.map((f) => findingName(f.code)).join(" and "),
      evidence: structural.map((f) => f.detail),
      cost: chapterCost,
      remedy: "inspect",
      hint: "",
    });
  } else if (input.headlessChunks > 0 && input.coverage.unreliable > 0) {
    blocks.push({
      key: "headless",
      name: "Chunks without headings",
      evidence: [`${input.headlessChunks} of ${input.chunkCount} chunks have no heading.`],
      cost: chapterCost,
      remedy: "inspect",
      hint: "",
    });
  }
  for (const f of input.findings.filter((f) => !chapterless.has(f.code))) {
    blocks.push({ key: f.code, name: findingName(f.code), evidence: [f.detail], cost: costOf(f.code), remedy: null, hint: "" });
  }
  let problems = [...input.problems];
  if (input.coverage.lost > 0) {
    const stated = problems.filter((p) => aboutUnclaimedPages.test(p));
    problems = problems.filter((p) => !aboutUnclaimedPages.test(p));
    const all = input.coverage.byChapter + input.coverage.unreliable + input.coverage.lost;
    blocks.push({
      key: "lost",
      name: "Pages not indexed",
      evidence: stated.length ? stated : [`${input.coverage.lost} of ${all} pages never reached the index.`],
      cost: "Agents can't search those pages.",
      remedy: "reingest",
      hint: "If it's a scan, run OCR on it first. docsearch reads only the text layer.",
    });
  }
  const headings = blocks.some((b) => b.key === "chapters" || b.key === "headless");
  const warnings = input.warnings.filter((w) => !(headings && aboutHeadings.test(w)));
  return { blocks, problems, warnings };
}

/** Text split at `backtick` spans, which the server uses for identifiers. */
export function codeSpans(text: string): { text: string; code: boolean }[] {
  return text
    .split(/`([^`]+)`/)
    .map((part, i) => ({ text: part, code: i % 2 === 1 }))
    .filter((p) => p.text);
}
