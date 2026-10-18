// Ingest jobs, polled through ingest_status: the same view an agent gets,
// which unlike ListJobs says when a job has stalled.

import { jobs } from "./api";
import { callTool, type JobView } from "./mcp";

export const phases = ["discover", "fetch", "extract", "chunk", "index"] as const;

const busy = new Set(["queued", "running", "cancelling"]);
const finished = new Set(["done", "cancelled"]);

/** Failures first, then stalled and running work, then the queue. */
function rank(j: JobView): number {
  if (j.status === "failed") return 0;
  if (j.stalled) return 1;
  if (j.status === "running" || j.status === "cancelling") return 2;
  return 3;
}

class Queue {
  all = $state<JobView[]>([]);
  error = $state("");
  loaded = $state(false);
  #timer: ReturnType<typeof setTimeout> | undefined;
  #watchers = 0;

  get open(): JobView[] {
    return this.all.filter((j) => !finished.has(j.status)).sort((a, b) => rank(a) - rank(b) || a.job_id - b.job_id);
  }

  get done(): JobView[] {
    return this.all.filter((j) => finished.has(j.status)).sort((a, b) => b.job_id - a.job_id);
  }

  get active(): number {
    return this.all.filter((j) => busy.has(j.status)).length;
  }

  get failed(): number {
    return this.all.filter((j) => j.status === "failed").length;
  }

  get stalled(): number {
    return this.all.filter((j) => j.stalled).length;
  }

  async refresh(): Promise<void> {
    try {
      const out = await callTool<{ jobs: JobView[] | null }>("ingest_status", { include_completed: true });
      this.all = out.jobs ?? [];
      this.error = "";
    } catch (err) {
      this.error = err instanceof Error ? err.message : String(err);
    } finally {
      this.loaded = true;
    }
  }

  /** Poll while anyone is watching: quickly while work is moving. */
  watch(): () => void {
    this.#watchers++;
    if (this.#watchers === 1) void this.#tick();
    return () => {
      this.#watchers--;
      if (this.#watchers === 0) clearTimeout(this.#timer);
    };
  }

  async #tick(): Promise<void> {
    await this.refresh();
    if (this.#watchers === 0) return;
    this.#timer = setTimeout(() => void this.#tick(), this.active > 0 ? 2000 : 15000);
  }

  /**
   * Queues a source. Forced, it is read again even where the index already
   * holds its bytes, which is what re-ingesting an unchanged file means.
   */
  async enqueue(source: string, title: string, force = false): Promise<number> {
    const { jobs: queued } = await jobs.enqueue({ source, title, force });
    void this.refresh();
    return Number(queued[0]?.queuePosition ?? 0n);
  }

  async cancel(jobId: number): Promise<void> {
    await jobs.cancelJob({ jobId: BigInt(jobId) });
    void this.refresh();
  }

  dismiss(jobId: number): void {
    this.all = this.all.filter((j) => j.job_id !== jobId);
  }
}

export const queue = new Queue();
