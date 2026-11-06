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
    .replace(/^-+|-+$/g, "")
    .slice(0, 63);
}

/** What a person reads a library as. */
export function nameOf(lib: Library | undefined): string {
  if (!lib) return "";
  if (lib.library === own) return "Personal";
  return lib.name || lib.library;
}
