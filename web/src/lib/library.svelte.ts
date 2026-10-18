// The signed-in account's documents, and the verify reports their coverage
// comes from.

import type { Document, VerifyReport } from "../gen/docsearch/document/v1/document_pb";
import { Quality, Verdict as VerdictEnum } from "../gen/docsearch/type/v1/common_pb";
import { documents, problem } from "./api";
import { type Coverage, coverageOf, type Verdict, verdictOrder } from "./coverage";

function fromReport(report?: VerifyReport): Verdict {
  if (report?.verdict === VerdictEnum.UNUSABLE) return "unusable";
  if (report?.verdict === VerdictEnum.DEGRADED) return "degraded";
  return "good";
}

function fromQuality(doc: Document): Verdict {
  if (doc.quality === Quality.FAILED) return "unusable";
  if (doc.quality === Quality.DEGRADED) return "degraded";
  return "good";
}

/**
 * The worse of the two grades a document carries: the one ingest gave it,
 * and the one verify gives it now. They measure different things, and either
 * can see a defect the other misses.
 */
export function verdictOf(doc: Document, report?: VerifyReport): Verdict {
  const a = fromReport(report);
  const b = fromQuality(doc);
  const graded = verdictOrder[a] <= verdictOrder[b] ? a : b;
  // Graded good, yet pages never reached the index or chapters cannot be
  // told apart: an agent loses part of it, which is what degraded means.
  if (graded === "good" && report && hasGaps(doc, report)) return "degraded";
  return graded;
}

function hasGaps(doc: Document, report: VerifyReport): boolean {
  const c = coverageFor(doc, report);
  return c.lost > 0 || c.unreliable > 0 || report.problems.length > 0;
}

export function coverageFor(doc: Document, report: VerifyReport): Coverage {
  return coverageOf({
    pageCount: doc.pageCount === undefined ? undefined : Number(doc.pageCount),
    chunkCount: Number(report.measurements?.chunkCount ?? doc.chunkCount ?? 0n),
    uncoveredPages: report.measurements?.uncoveredPages.length ?? 0,
    headlessChunks: Number(report.measurements?.headlessChunks ?? 0n),
    findingCodes: report.findings.map((f) => f.code),
  });
}

class Library {
  docs = $state<Document[]>([]);
  loaded = $state(false);
  error = $state("");
  reports = $state<Record<string, VerifyReport>>({});
  reportErrors = $state<Record<string, string>>({});

  async load(): Promise<void> {
    try {
      const { documents: all } = await documents.list({});
      // A document is invisible to search until its ingest completes; the
      // ingest tab shows the rest.
      this.docs = all.filter((d) => d.status === "" || d.status === "ready");
      this.error = "";
      void this.#verifyAll();
    } catch (err) {
      this.error = problem(err);
    } finally {
      this.loaded = true;
    }
  }

  /** Whether a document belongs under "Needs attention". */
  needsAttention(doc: Document): boolean {
    return verdictOf(doc, this.reports[doc.docId]) !== "good";
  }

  /** Coverage needs a verify report per document; a few at a time. */
  async #verifyAll(): Promise<void> {
    // Every document, including ones already measured: a re-ingest replaces what
    // a cached report described.
    const pending = this.docs.map((d) => d.docId);
    const next = async (): Promise<void> => {
      const id = pending.shift();
      if (id === undefined) return;
      await this.verify(id);
      return next();
    };
    await Promise.all(Array.from({ length: 4 }, next));
  }

  async verify(docId: string): Promise<void> {
    try {
      const { report } = await documents.verify({ docId });
      if (report) this.reports[docId] = report;
      delete this.reportErrors[docId];
    } catch (err) {
      this.reportErrors[docId] = problem(err);
    }
  }

  async remove(docId: string): Promise<void> {
    await documents.remove({ docId });
    this.docs = this.docs.filter((d) => d.docId !== docId);
    delete this.reports[docId];
  }

  async rename(docId: string, title: string): Promise<void> {
    await documents.rename({ docId, title });
    this.docs = this.docs.map((d) => (d.docId === docId ? { ...d, title } : d));
  }

  byId(docId: string): Document | undefined {
    return this.docs.find((d) => d.docId === docId);
  }
}

export const library = new Library();
