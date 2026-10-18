// Hash routes, so the server needs no fallback for client paths and a
// sign-in redirect back to / lands on the same page.
//   #/library            #/library/<doc_id>
//   #/ingest             #/search             #/search/<doc_id>
//   #/read/<doc_id>      #/read/<doc_id>/<chunk_id>

export type View = "library" | "ingest" | "search" | "read";

interface Route {
  view: View;
  docId: string;
  chunkId: number | null;
}

function parse(hash: string): Route {
  const [, view = "", ...rest] = hash.replace(/^#/, "").split("/");
  if (view === "ingest") return { view, docId: "", chunkId: null };
  if (view === "read") {
    const chunk = Number(rest[1]);
    return { view, docId: decodeURIComponent(rest[0] ?? ""), chunkId: Number.isInteger(chunk) && rest[1] ? chunk : null };
  }
  const docId = decodeURIComponent(rest.join("/"));
  if (view === "search") return { view, docId, chunkId: null };
  return { view: "library", docId, chunkId: null };
}

class Router {
  view = $state<View>("library");
  docId = $state("");
  chunkId = $state<number | null>(null);

  constructor() {
    this.#read();
    addEventListener("hashchange", () => this.#read());
  }

  #read(): void {
    const route = parse(location.hash);
    this.view = route.view;
    this.docId = route.docId;
    this.chunkId = route.chunkId;
  }

  go(view: View, docId = ""): void {
    location.hash = docId ? `#/${view}/${encodeURIComponent(docId)}` : `#/${view}`;
  }
}

/** The reader's address for a document, opened at a chunk when one is named. */
export function readerHref(docId: string, chunkId?: number | bigint): string {
  return `#/read/${encodeURIComponent(docId)}${chunkId !== undefined ? `/${chunkId}` : ""}`;
}

export const router = new Router();
