// Coverage: how much of a document an agent can reach, the one figure the
// library UI leads with. Derived from a verify report, which already knows
// which pages no chunk claims and whether chapters can be told apart.

export type Verdict = "good" | "degraded" | "unusable";

export interface Coverage {
  /** Reachable, and narrowable to a chapter. */
  byChapter: number;
  /** Reachable, but section_filter and the outline cannot separate chapters. */
  unreliable: number;
  /** Never reached the index. */
  lost: number;
  unit: "pages" | "chunks";
}

export interface CoverageInput {
  pageCount?: number;
  chunkCount: number;
  uncoveredPages: number;
  /** Chunks no heading reaches. */
  headlessChunks: number;
  findingCodes: readonly string[];
}

/** Findings that leave every reachable page without usable chapters. */
const chapterless = new Set(["unaddressable", "flat_hierarchy"]);

export function coverageOf(input: CoverageInput): Coverage {
  const paginated = input.pageCount !== undefined && input.pageCount > 0;
  const total = paginated ? (input.pageCount ?? 0) : input.chunkCount;
  const lost = paginated ? Math.min(input.uncoveredPages, total) : 0;
  const reachable = total - lost;
  const everyChunkHeadless = input.chunkCount > 0 && input.headlessChunks >= input.chunkCount;
  let unreliable = 0;
  if (input.findingCodes.some((c) => chapterless.has(c)) || everyChunkHeadless) {
    unreliable = reachable;
  } else if (!paginated) {
    // Counted in chunks, a headless chunk is exactly one unreliable unit. A
    // paginated document cannot say which pages its headless chunks cover.
    unreliable = Math.min(input.headlessChunks, reachable);
  }
  return {
    byChapter: reachable - unreliable,
    unreliable,
    lost,
    unit: paginated ? "pages" : "chunks",
  };
}

export function total(c: Coverage): number {
  return c.byChapter + c.unreliable + c.lost;
}

const number = new Intl.NumberFormat("en-US");

/** The sentence a document page opens with: what an agent gets. */
export function headline(c: Coverage): string {
  const all = total(c);
  const reach = c.byChapter + c.unreliable;
  if (all === 0) {
    return "Nothing from this document reached the index.";
  }
  const of = c.lost > 0 ? `${number.format(reach)} of ${number.format(all)}` : `all ${number.format(all)}`;
  if (c.unreliable > 0) {
    return `Agents reach ${of} ${c.unit}, but can't narrow a search to one chapter.`;
  }
  if (c.lost > 0) {
    return `Agents reach ${of} ${c.unit}.`;
  }
  return `Agents reach ${of} ${c.unit} by chapter.`;
}

/** What each grade finding costs an agent, in one line. */
const costs: Record<string, string> = {
  unaddressable: "Agents can't narrow a search with section_filter, and the outline won't guide them.",
  flat_hierarchy: "Agents can't narrow a search with section_filter, and the outline won't guide them.",
  oversized: "Agents get whole chapters back instead of passages.",
  fragmented: "Chunks are too small to answer a question on their own.",
  figure_dominated: "Agents get the captions but not what the figures show.",
  boilerplate: "Repeated headers and footers match every query and crowd out real results.",
};

export function costOf(code: string): string {
  return costs[code] ?? "";
}

/** Readable name for a finding code. */
export function findingName(code: string): string {
  const name = code.replaceAll("_", " ");
  return name.charAt(0).toUpperCase() + name.slice(1);
}

export const verdictWord: Record<Verdict, string> = {
  good: "Good",
  degraded: "Degraded",
  unusable: "Unusable",
};

export const verdictOrder: Record<Verdict, number> = { unusable: 0, degraded: 1, good: 2 };
