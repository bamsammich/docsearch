// How a library is named: to a person, and to the agents that type it.

import type { Library } from "../gen/docsearch/library/v1/library_pb";

/** The label of the account's own library. */
export const own = "personal";

/** The name agents type for a library called name: lower-case words joined by hyphens. */
export function slugOf(name: string): string {
  return name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+/, "")
    .slice(0, 63)
    .replace(/-+$/, "");
}

/** The label of the library a document id names: the part before a slash, or the account's own. */
export function labelOf(docId: string): string {
  const at = docId.indexOf("/");
  return at > 0 ? docId.slice(0, at) : own;
}

/** The letter a tile shows for a name: its first letter or digit, in any script. */
export function initialOf(name: string): string {
  return (name.match(/[\p{L}\p{N}]/u)?.[0] ?? "?").toUpperCase();
}

/** What a person reads a library as. */
export function nameOf(lib: Library | undefined): string {
  if (!lib) return "";
  if (lib.library === own) return "Personal";
  return lib.name || lib.library;
}
