// Who the signed-in account is to docsearch as a whole: its username, and
// whether it administers.

import { accounts, problem } from "./api";

class Admin {
  username = $state("");
  isAdmin = $state(false);
  /** The sign-in mode: none, token, password or oidc. */
  auth = $state("");
  error = $state("");

  /** Only password and oidc mode have accounts to manage. */
  get available(): boolean {
    return this.isAdmin && (this.auth === "password" || this.auth === "oidc");
  }

  get passwords(): boolean {
    return this.auth === "password";
  }

  async load(): Promise<void> {
    try {
      const me = await accounts.me({});
      this.username = me.username;
      this.isAdmin = me.admin;
      this.auth = me.auth;
      this.error = "";
    } catch (err) {
      this.error = problem(err);
    }
  }
}

export const admin = new Admin();
