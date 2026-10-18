// The authenticated upload door: PUT the bytes, named by ?name=, and titled
// by ?title= when the person gave one.

import { authHeaders, refusal } from "./api";
import { session } from "./session.svelte";

export interface Received {
  key: string;
  source: string;
  job_id: number;
}

export async function upload(file: File, title = ""): Promise<Received> {
  const query = new URLSearchParams({ name: file.name });
  if (title) query.set("title", title);
  const response = await fetch(`/upload?${query}`, {
    method: "PUT",
    headers: { Accept: "application/json", ...authHeaders() },
    body: file,
  });
  if (response.status === 401) {
    session.rejected();
    throw new Error("Sign-in failed.");
  }
  if (response.status === 403) throw new Error(refusal((await response.text()).trim()));
  const text = await response.text();
  if (!response.ok) {
    // The door answers a refusal in plain text that already names the fix.
    throw new Error(text.trim() || `upload failed (${response.status})`);
  }
  return JSON.parse(text) as Received;
}
