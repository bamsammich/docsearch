// What a document page says is wrong: one block per problem, each with its
// evidence, what an agent loses, and the remedy, if any. Built from verify's
// findings, and from coverage when no finding names the gap.

import { costOf, type Coverage, findingName } from "./coverage";

// Every cost reads after "Agents lose:", so each is the loss itself.

export type Remedy = "inspect" | "reingest";

export interface Block {
  key: string;
  name: string;
  /** The pages the problem sits on, such as "pp. 131–138", or "". */
  pages: string;
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
  /** The pages no chunk claims, when verify listed them. */
  uncoveredPages?: readonly number[];
}

/** Page numbers as runs: [1, 2, 5, 6, 7, 9] reads "1–2, 5–7, 9". */
export function pageRuns(pages: readonly number[]): string {
  const sorted = [...new Set(pages)].sort((a, b) => a - b);
  const runs: string[] = [];
  for (let i = 0; i < sorted.length; ) {
    let j = i;
    while (j + 1 < sorted.length && sorted[j + 1] === sorted[j]! + 1) j++;
    runs.push(i === j ? `${sorted[i]}` : `${sorted[i]}–${sorted[j]}`);
    i = j + 1;
  }
  return runs.join(", ");
}

/** "p. 4" for one page, "pp. 1–2" for more. */
export function pagesLabel(pages: readonly number[]): string {
  if (!pages.length) return "";
  return `${new Set(pages).size === 1 ? "p." : "pp."} ${pageRuns(pages)}`;
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
const aboutHeadings = /distinct heading path|no heading path|share one heading path/;
const aboutUnclaimedPages = /claimed by no chunk/;

export function findingsOf(input: FindingsInput): Findings {
  const blocks: Block[] = [];
  const structural = input.findings.filter((f) => chapterless.has(f.code));
  if (structural.length) {
    blocks.push({
      key: "chapters",
      // One block for both structural codes, named for what they share.
      name: "No usable chapters",
      pages: "",
      evidence: structural.map((f) => f.detail),
      cost: chapterCost,
      remedy: "inspect",
      hint: "",
    });
  } else if (input.headlessChunks > 0 && input.coverage.unreliable > 0) {
    blocks.push({
      key: "headless",
      name: "Chunks without headings",
      pages: "",
      evidence: [`${input.headlessChunks} of ${input.chunkCount} chunks have no heading.`],
      cost: chapterCost,
      remedy: "inspect",
      hint: "",
    });
  }
  for (const f of input.findings.filter((f) => !chapterless.has(f.code))) {
    blocks.push({ key: f.code, name: findingName(f.code), pages: "", evidence: [f.detail], cost: costOf(f.code), remedy: null, hint: "" });
  }
  let problems = [...input.problems];
  if (input.coverage.lost > 0) {
    // The block states the server's unclaimed-pages problem in its own words,
    // so the problem list leaves it out.
    problems = problems.filter((p) => !aboutUnclaimedPages.test(p));
    const all = input.coverage.byChapter + input.coverage.unreliable + input.coverage.lost;
    const pages = input.uncoveredPages ?? [];
    blocks.push({
      key: "lost",
      name: "Pages not indexed",
      pages: pagesLabel(pages),
      evidence: [`${input.coverage.lost} of ${all} pages produced no stored text, so no search can return them.`],
      cost: pages.length ? `anything on ${pagesLabel(pages).replace(/^pp?\. /, pages.length === 1 ? "page " : "pages ")}.` : "anything on those pages.",
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
