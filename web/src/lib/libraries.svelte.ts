// The libraries the signed-in account reads, and the one the pages show.

import type { Invitation, Library } from "../gen/docsearch/library/v1/library_pb";
import { problem, shelves } from "./api";
import { nameOf, own } from "./names";

export { labelOf, nameOf, own, slugOf } from "./names";

const remember = "docsearch.library";

function recalled(): string {
  try {
    return localStorage.getItem(remember) || own;
  } catch {
    return own;
  }
}

class Libraries {
  all = $state<Library[]>([]);
  /** Libraries this account is asked to join, newest first. */
  invitations = $state<Invitation[]>([]);
  error = $state("");
  loaded = $state(false);
  /** The label of the library the pages show. */
  label = $state(recalled());
  /** This account's username, as others invite it by. */
  username = $state("");

  get current(): Library | undefined {
    return this.all.find((l) => l.library === this.label);
  }

  get name(): string {
    return nameOf(this.current) || "Personal";
  }

  /** Editors and admins add, re-read and remove documents. */
  get canEdit(): boolean {
    const role = this.current?.role;
    return role === "editor" || role === "admin";
  }

  get isAdmin(): boolean {
    return this.current?.role === "admin";
  }

  get isOwn(): boolean {
    return this.label === own;
  }

  /** The label a call names the current library by: empty for the account's own. */
  get named(): string {
    return this.isOwn ? "" : this.label;
  }

  async load(): Promise<void> {
    try {
      const [{ libraries, username }, { invitations }] = await Promise.all([
        shelves.list({}),
        shelves.invitations({}),
      ]);
      this.all = libraries;
      this.invitations = invitations;
      this.username = username;
      this.error = "";
      if (!this.current) this.choose(own);
    } catch (err) {
      this.error = problem(err);
    } finally {
      this.loaded = true;
    }
  }

  choose(label: string): void {
    this.label = label;
    try {
      localStorage.setItem(remember, label);
    } catch {
      // A browser that keeps nothing starts on the personal library next time.
    }
  }

  /** Creates a library, with this account as its admin, and shows it. */
  async create(slug: string, name: string): Promise<void> {
    const { library } = await shelves.create({ slug, name });
    if (library) this.all = [...this.all, library];
    this.choose(slug);
  }

  /** Joins a library this account is invited to, and shows it. */
  async accept(label: string): Promise<Library | undefined> {
    const { library } = await shelves.accept({ library: label });
    this.invitations = this.invitations.filter((i) => i.library !== label);
    if (library) this.all = [...this.all.filter((l) => l.library !== label), library];
    this.choose(label);
    return library;
  }

  async decline(label: string): Promise<void> {
    await shelves.decline({ library: label });
    this.invitations = this.invitations.filter((i) => i.library !== label);
  }

  /** Takes this account out of the current library and goes back to its own. */
  async leave(): Promise<void> {
    await shelves.revoke({ library: this.label, username: this.username });
    this.all = this.all.filter((l) => l.library !== this.label);
    this.choose(own);
  }
}

export const libraries = new Libraries();
