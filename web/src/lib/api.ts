// The Connect clients, each sending the session's bearer.

import { Code, ConnectError, createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { DocumentService } from "../gen/docsearch/document/v1/document_pb";
import { JobService } from "../gen/docsearch/ingest/v1/job_pb";
import { session } from "./session.svelte";

const bearer: Interceptor = (next) => async (req) => {
  if (session.token) req.header.set("Authorization", `Bearer ${session.token}`);
  try {
    return await next(req);
  } catch (err) {
    if (err instanceof ConnectError && err.code === Code.Unauthenticated) session.rejected();
    throw err;
  }
};

const transport = createConnectTransport({ baseUrl: location.origin, interceptors: [bearer] });

export const documents = createClient(DocumentService, transport);
export const jobs = createClient(JobService, transport);

/** The server refuses requests from a page opened at an address it does not accept. */
export function forbiddenOrigin(): string {
  const at = session.config?.publicUrl;
  return `This server doesn't accept requests from ${location.origin}.${at ? ` Open the library at ${at}.` : ""}`;
}

/**
 * Two refusals answer 403: a page address the server does not accept, and an
 * account it does not let in. The config says which.
 */
export function refusal(reason = ""): string {
  // A plain-HTTP door says which refusal it was in its body; trust that over
  // the config's hint.
  if (/forbidden origin|invalid host/i.test(reason)) return forbiddenOrigin();
  if (reason) return "This account isn't allowed on this server.";
  return session.config?.originAllowed === false ? forbiddenOrigin() : "This account isn't allowed on this server.";
}

/** A message fit to show a person, from whatever a call threw. */
export function problem(err: unknown): string {
  if (err instanceof ConnectError) {
    if (err.code === Code.Unauthenticated) return "Sign-in failed.";
    if (err.code === Code.PermissionDenied) return refusal();
    if (err.code === Code.Unavailable || /fetch|network/i.test(err.rawMessage)) return "docsearch isn't answering.";
    return err.rawMessage || err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

/** Headers for the plain-HTTP doors: /mcp and /upload. */
export function authHeaders(): Record<string, string> {
  return session.token ? { Authorization: `Bearer ${session.token}` } : {};
}
