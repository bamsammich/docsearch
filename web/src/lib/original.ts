// A document's original file, fetched with the session's bearer because a
// plain link cannot carry it.

import { authHeaders, refusal } from "./api";
import { session } from "./session.svelte";

async function fetchOriginal(docId: string): Promise<Blob> {
  const response = await fetch(`/original/${encodeURIComponent(docId)}`, { headers: authHeaders() });
  if (response.status === 401) {
    session.rejected();
    throw new Error("Sign-in failed.");
  }
  if (response.status === 403) throw new Error(refusal((await response.text()).trim()));
  if (!response.ok) throw new Error((await response.text()).trim() || `couldn't open the original (${response.status})`);
  return response.blob();
}

/**
 * Opens the original in a new tab. The tab opens before the fetch, while the
 * click still counts as one, or a popup blocker would stop it.
 */
export async function openOriginal(docId: string): Promise<void> {
  const tab = window.open("", "_blank");
  try {
    const url = URL.createObjectURL(await fetchOriginal(docId));
    if (tab) {
      tab.opener = null;
      tab.location.href = url;
    } else {
      location.assign(url);
    }
    setTimeout(() => URL.revokeObjectURL(url), 60_000);
  } catch (err) {
    tab?.close();
    throw err;
  }
}

/** Saves the original under its own name, for a format no tab can show. */
export async function downloadOriginal(docId: string, filename: string): Promise<void> {
  const url = URL.createObjectURL(await fetchOriginal(docId));
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}
