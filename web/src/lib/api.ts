// The Connect clients, each sending the session's bearer.

import { Code, ConnectError, createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AccountService } from "../gen/docsearch/account/v1/account_pb";
import { DocumentService } from "../gen/docsearch/document/v1/document_pb";
import { GroupService } from "../gen/docsearch/group/v1/group_pb";
import { JobService } from "../gen/docsearch/ingest/v1/job_pb";
import { LibraryService } from "../gen/docsearch/library/v1/library_pb";
import { session } from "./session.svelte";

/**
 * Sent when a handler refuses what the account's role no longer allows: the
 * page reads the account's libraries and its administrator role again.
 */
export const roleChanged = "docsearch:role-changed";

const bearer: Interceptor = (next) => async (req) => {
  if (session.token) req.header.set("Authorization", `Bearer ${session.token}`);
  try {
    return await next(req);
  } catch (err) {
    if (err instanceof ConnectError && err.code === Code.Unauthenticated) session.rejected();
    if (handlerRefusal(err)) window.dispatchEvent(new Event(roleChanged));
    throw err;
  }
};

/**
 * A refusal a handler made, which says why in words. The doors in front of
 * the handlers, the page address check and the account check, answer a bare
 * 403 that reads as "HTTP 403".
 */
function handlerRefusal(err: unknown): boolean {
  return err instanceof ConnectError && err.code === Code.PermissionDenied &&
    err.rawMessage !== "" && !/^HTTP \d+$/.test(err.rawMessage);
}

/** A server message as a sentence: capitalized, with a full stop. */
export function sentence(message: string): string {
  const s = message.replace(/^./, (c) => c.toUpperCase());
  return /[.!?]$/.test(s) ? s : `${s}.`;
}

const transport = createConnectTransport({ baseUrl: location.origin, interceptors: [bearer] });

export const documents = createClient(DocumentService, transport);
export const jobs = createClient(JobService, transport);
export const shelves = createClient(LibraryService, transport);
export const accounts = createClient(AccountService, transport);
export const groups = createClient(GroupService, transport);

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
    if (err.code === Code.PermissionDenied && handlerRefusal(err)) return sentence(err.rawMessage);
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
