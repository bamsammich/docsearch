// Search preview calls the MCP tools themselves, over the same /mcp endpoint
// and with the same bearer an agent uses, so what it shows is what an agent
// gets. The server is stateless, so one POST per call needs no session.

import { authHeaders, refusal } from "./api";
import { session } from "./session.svelte";

interface ToolResult {
  content?: { type: string; text?: string }[];
  structuredContent?: unknown;
  isError?: boolean;
}

interface RpcReply {
  id?: number;
  result?: ToolResult;
  error?: { code: number; message: string };
}

let nextId = 1;

/** The JSON-RPC reply in a body that is either JSON or an event stream. */
export function replyFrom(body: string, contentType: string, id: number): RpcReply {
  if (!contentType.includes("text/event-stream")) {
    return JSON.parse(body) as RpcReply;
  }
  for (const event of body.split(/\r?\n\r?\n/)) {
    const data = event
      .split(/\r?\n/)
      .filter((line) => line.startsWith("data:"))
      .map((line) => line.slice(5).trimStart())
      .join("\n");
    if (!data) continue;
    const reply = JSON.parse(data) as RpcReply;
    if (reply.id === id) return reply;
  }
  throw new Error("the server sent no reply to the call");
}

/** The tool's own output, from a tools/call result. */
export function outputOf<T>(result: ToolResult): T {
  const text = result.content?.find((c) => c.type === "text")?.text ?? "";
  if (result.isError) throw new Error(text || "the tool reported an error");
  if (result.structuredContent !== undefined) return result.structuredContent as T;
  return JSON.parse(text) as T;
}

export async function callTool<T>(name: string, args: Record<string, unknown>): Promise<T> {
  const id = nextId++;
  const response = await fetch("/mcp", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "application/json, text/event-stream",
      ...authHeaders(),
    },
    body: JSON.stringify({ jsonrpc: "2.0", id, method: "tools/call", params: { name, arguments: args } }),
  });
  if (response.status === 401) {
    session.rejected();
    throw new Error("Sign-in failed.");
  }
  if (response.status === 403) throw new Error(refusal((await response.text()).trim()));
  if (!response.ok) {
    throw new Error(`${name} failed: ${response.status} ${(await response.text()).trim()}`);
  }
  const reply = replyFrom(await response.text(), response.headers.get("Content-Type") ?? "", id);
  if (reply.error) throw new Error(reply.error.message);
  if (!reply.result) throw new Error(`${name} returned nothing`);
  return outputOf<T>(reply.result);
}

/** The search tool's output, as pgstore.SearchResult serialises. */
export interface SearchHit {
  doc_id: string;
  title: string;
  heading_path: string;
  section?: string;
  page_start?: number;
  page_end?: number;
  url?: string;
  fragment?: string;
  text: string;
  chunk_id: number;
  relevance: number;
  rank: number;
}

export interface SearchOutput {
  results: SearchHit[];
  note?: string;
  figure_dominated_results?: number;
}

export interface ContextChunk {
  chunk_id: number;
  heading_path: string;
  text: string;
  page_start?: number;
}

export interface ContextOutput {
  chunks?: ContextChunk[];
  note?: string;
}

/** A job as ingest_status reports it, which includes whether it stalled. */
export interface JobView {
  job_id: number;
  source_path: string;
  title?: string;
  doc_id?: string;
  status: string;
  phase?: string;
  progress_current?: number;
  progress_total?: number;
  disposition?: string;
  error?: string;
  quality?: string;
  created_at: string;
  updated_at: string;
  elapsed: string;
  stalled?: boolean;
  stalled_note?: string;
  warnings?: string[];
  attempts: number;
  permanent?: boolean;
}
