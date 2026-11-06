// The search the top bar holds. The field lives in the app shell so it is on
// every page; the results page reads it and runs each search it is sent.

class Search {
  query = $state("");
  /** The document the search is scoped to, or "" for more than one. */
  docId = $state("");
  /** The label of the one library the search is scoped to, or "" for every library. */
  library = $state("");
  /** Counts submits, so the results page reruns a search asked twice. */
  runs = $state(0);
  #field: HTMLInputElement | undefined;

  attach(field: HTMLInputElement | undefined): void {
    this.#field = field;
  }

  focus(): void {
    this.#field?.focus();
    this.#field?.select();
  }

  /**
   * The scope picker's value: "" for every library, "library:<label>" for
   * one library, or a doc_id.
   */
  get scope(): string {
    return this.docId || (this.library ? `library:${this.library}` : "");
  }

  set scope(value: string) {
    const label = value.startsWith("library:") ? value.slice("library:".length) : "";
    this.library = label;
    this.docId = label ? "" : value;
  }

  /** Scopes the search to one document and puts the cursor in the field. */
  within(docId: string): void {
    this.library = "";
    this.docId = docId;
    this.focus();
  }

  submit(): void {
    this.runs++;
  }
}

export const search = new Search();
