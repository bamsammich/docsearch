# Configuration

Every server setting, the four authentication modes, the security boundaries, and how to register the server with an MCP client. Back to the [README](../README.md).

## Settings

| | flag | environment | docsearch.yaml (server) |
|---|---|---|---|
| bind address | `--addr` | `DOCSEARCH_ADDR` | `addr` |
| database | `--dsn` | `DOCSEARCH_DSN` | — |
| auth mode | `--auth` | `DOCSEARCH_AUTH` | `auth.mode` |
| pin the worker to one library | `--user` (worker only) | `DOCSEARCH_USER` | — |
| library roots | `--root` (repeatable) | `DOCSEARCH_ROOT` (`:`-separated) | `roots` |
| bearer token | — | `DOCSEARCH_TOKEN` | — |
| Origin allowlist | `--allowed-origins` | `DOCSEARCH_ALLOWED_ORIGINS` | `allowed_origins` |
| library UI's client at the oidc provider | `--ui-client-id` | `DOCSEARCH_UI_CLIENT_ID` | `ui.client_id` |
| label the library UI shows on every page, such as `Test instance` | `--instance-label` | `DOCSEARCH_INSTANCE_LABEL` | `ui.instance_label` |
| permit public bind | `--allow-public-bind` | `DOCSEARCH_ALLOW_PUBLIC_BIND=true` | `allow_public_bind` |
| [heading judge](judge.md), on the server and the worker | `--judge-url` | `DOCSEARCH_JUDGE_URL` | `ingest.judge_url` |
| heading judge's bearer token | — | `DOCSEARCH_JUDGE_TOKEN` | — |

`docsearch-server --config docsearch.yaml` (or `DOCSEARCH_CONFIG`) reads the
server's settings from a file, so a deployment repository can hold them.
A setting is taken from the strongest place that holds it:

| strongest | | | | weakest |
|---|---|---|---|---|
| command line | environment | `docsearch.yaml` | database | default |

The database holds what an administrator changed on the Sign-in page, for
a setting nothing stronger sets. A setting the file holds is changed in the
file: the Sign-in page shows the updated file to commit and deploy. Nested
keys nest in the file:

```yaml
addr: 0.0.0.0:8765
public_url: https://docs.example.com
auth:
  mode: oidc
  oidc:
    issuer: https://id.example.com
    sign_up: true
    required_claim:
      name: groups
      value: docsearch-users
roots:
  - /srv/docs
```

A key no setting reads is refused at startup rather than ignored, so a typo
cannot leave a setting at its default. Secrets come from the environment or
the command line alone: the bearer token, the oidc client secret, the heading
judge's token, and the database DSN, which carries the request role's password. A file is shown to
administrators on the Sign-in page, so it never holds one. An administrator sees
on the Sign-in page where each sign-in setting came from.

`--auth` is `none`, `token`, `oidc` or `password`, and defaults to `token`,
which is what a v1 install already runs. `none` asks for no credential and
answers every request as the single built-in user, so it is refused on any
address that is not loopback, whatever `--allow-public-bind` says.

### Signing in with accounts docsearch holds

`password` mode is for a deployment with no identity provider. docsearch
becomes its own OAuth authorization server: Claude, Claude Code and any other
MCP client discover it, register themselves, and send the person to a
sign-in page. Each account has a library of its own.

```bash
docsearch account add alice                 # asks for a password twice
docsearch account add bob --generate        # makes one up and prints it once
docsearch account add carol --takes-builtin # carol keeps the existing library
docsearch account passwd alice              # reset, and lift any lock
docsearch account unlock alice
docsearch account remove alice              # ends sign-in and frees the name; her own library stays
docsearch account list
```

`docsearch account` connects as the database owner, through `--dsn` or
`DOCSEARCH_DSN`, the same credential `migrate` uses.

**Administrators** manage accounts from the library UI, in `password` and
`oidc` mode. Make the first one with the owner DSN; after that,
administrators make others from the People page.

```bash
docsearch account admin alice               # works in oidc mode too, once alice has signed in
docsearch account admin alice --revoke
```

In `password` mode a deployment can instead name its first administrator in
the environment of `docsearch migrate`, which runs before the server on every
start. When no account holds the username, migrate creates it with that
password and makes it an administrator. An account that already exists is
left as it is, so a password changed since, or a role taken away, stays
changed.

```bash
DOCSEARCH_ADMIN_USERNAME=alice DOCSEARCH_ADMIN_PASSWORD='…' docsearch migrate
```

An administrator sees every account, and in `password` mode adds one with a
generated password shown once, resets or unlocks a password, and removes an
account. Each change is recorded in `account_events`. The role covers
accounts only: an administrator reads a library only through a grant on it,
like anyone. The last administrator cannot step down.

Removing an account takes it out of every shared library and frees its
username for someone new. Its own library stays. A shared library it alone
administered passes to the administrator removing it, and the change shows
in that library's log. `docsearch account remove` hands such a library to
the administrator `--to` names, or else to the longest-standing other
administrator. Resetting a password signs the account out everywhere.

| | |
|---|---|
| passwords | argon2id at 19 MiB, at least 12 characters; four are checked at a time |
| failed sign-ins | five in a row lock the account for 15 minutes |
| rate limits | per address: 30 registrations an hour, 60 authorizations and 20 sign-ins a minute |
| access tokens | ES256 JWTs, valid for an hour |
| refresh tokens | valid for 30 days, and replaced every time one is used |
| clients | register themselves (RFC 7591), hold no secret, and must use PKCE; one that signs nobody in within a day is cleared |

Set `--public-url` to the address people reach the server at. It is the
issuer every token names, and the sign-in page lives there. A refused sign-in
never says whether the username exists. There is no self-service reset,
since that needs a mail server; `docsearch account passwd` is the reset.
Removing an account stops its tokens at once rather than when they expire.
Behind a reverse proxy, every request has the proxy's address, so the rate
limits apply to the deployment as a whole. A forwarded header is not trusted,
because whoever sends one chooses it.

With accounts, in `password` or `oidc` mode, the server takes no filesystem
paths. Every account would read the same `--root` directories, so anyone
signed in could index any file under them. Accounts add documents by URL,
by upload, or through `docsearch-sync`. To let every account read the roots
anyway, set `--shared-paths` (`DOCSEARCH_SHARED_PATHS`) with `--root`. A
server given roots without it refuses to start.

### Signing in through your own provider

In `oidc` mode your identity provider is the OAuth authorization server, and
docsearch reads the tokens it issues. Each identity gets a library of its
own. Any OIDC provider works; what docsearch needs from it is a token whose
audience names docsearch.

| | flag | environment | docsearch.yaml (server) |
|---|---|---|---|
| issuer | `--oidc-issuer` | `DOCSEARCH_OIDC_ISSUER` | `auth.oidc.issuer` |
| docsearch's `/mcp` URI | `--oidc-resource` | `DOCSEARCH_OIDC_RESOURCE` | `auth.oidc.resource` |
| expected audience, if not the URI | `--oidc-audience` | `DOCSEARCH_OIDC_AUDIENCE` | `auth.oidc.audience` |
| scopes a client should request | `--oidc-scopes-supported` | `DOCSEARCH_OIDC_SCOPES_SUPPORTED` | `auth.oidc.scopes_supported` |
| scopes every token must carry | `--oidc-required-scopes` | `DOCSEARCH_OIDC_REQUIRED_SCOPES` | `auth.oidc.required_scopes` |
| client ID, to introspect opaque tokens | `--oidc-client-id` | `DOCSEARCH_OIDC_CLIENT_ID` | `auth.oidc.client_id` |
| client secret | — | `DOCSEARCH_OIDC_CLIENT_SECRET` | — |
| subject who keeps the existing library | `--oidc-default-owner` | `DOCSEARCH_OIDC_DEFAULT_OWNER` | `auth.oidc.default_owner` |
| create accounts on first sign-in | `--oidc-sign-up` | `DOCSEARCH_OIDC_SIGN_UP` | `auth.oidc.sign_up` |
| claim usernames come from | `--oidc-username-claim` | `DOCSEARCH_OIDC_USERNAME_CLAIM` | `auth.oidc.username_claim` |
| claim every token must carry | `--oidc-required-claim-name` | `DOCSEARCH_OIDC_REQUIRED_CLAIM_NAME` | `auth.oidc.required_claim.name` |
| value that claim must hold | `--oidc-required-claim-value` | `DOCSEARCH_OIDC_REQUIRED_CLAIM_VALUE` | `auth.oidc.required_claim.value` |

Scope lists are comma-separated. A signed JWT is checked against the keys the
issuer publishes. An opaque token is sent back to the issuer for
introspection, which needs the client ID and secret; without them an opaque
token is refused.

**Who gets a library** follows what Grafana and Gitea do. Restrict who can
sign in to docsearch at the provider first. Sign-up starts closed, so an
identity with no account is refused until you pass `--oidc-sign-up`. A
required claim, such as `groups` holding `docsearch-users`, is checked on
every request, so removing someone from the group at the provider removes
their access here.

**Usernames** are how someone is invited to a shared library. A password
account's is the name it signs in with. An `oidc` account's is the token's
`email` when `email_verified` is true, and otherwise its
`preferred_username`, recorded when the account signs in. Many providers let
people choose their own `preferred_username`, so name the claim your
provider controls with `--oidc-username-claim` where you can: only that
claim is then read. An account that has never signed in has no username
yet. A name another account already holds stays with that account.

**Switching an existing install** keeps its library. Name your own subject
with `--oidc-default-owner`, and your first sign-in takes the library the
deployment already holds. Nobody else can claim it.

**The CLI** sends `DOCSEARCH_TOKEN` as its bearer token in every mode. In
`oidc` mode that is an access token from your provider, and the CLI reads the
library of the identity the token names.

**The library roots are a security boundary.** `add_document` accepts a target
that arrives in a tool call, over a network endpoint, and may have been
suggested by the content of a document rather than typed by a person. A path is
resolved through symlinks and confirmed inside one of the roots; every
rejection returns the same error, so the tool cannot be used to probe for
files.

**A target may also be an `http(s)` URL**, crawled as one document. It is held
to the same standard: scheme, host and every resolved address are validated at
the tool boundary before anything is queued, again in the worker — a job row is
not proof that anything checked it — and again on every redirect hop. Rejections
are likewise indistinguishable, so the tool cannot be used to map an internal
network.

The roots come from server configuration only. No tool parameter can widen
them. More than one may be configured — `--root A --root B`, or
`DOCSEARCH_ROOT=A:B` — and one unusable root fails startup rather than being
dropped, because a server quietly serving three of four configured directories
surfaces the fourth only as an unexplained rejection much later.

**The configured roots are named in `add_document`'s description.** A caller
that can see neither the roots nor why a path was refused cannot act on the
rejection, which left the tool usable only on files a person had already
staged. Which directories an operator chose to expose is not what the uniform
error protects; the caller's path is.

**The server refuses to bind a non-loopback address** without
`--allow-public-bind`, and logs a warning when you pass it.

**Bearer token on every `/mcp` request** in `token` mode, compared in constant time over
SHA-256 digests — and compared even when the header is absent, so a missing
token cannot be distinguished from a wrong one by timing.

**Origin allowlist** as a DNS-rebinding defence. An absent `Origin` is allowed
deliberately: native MCP clients send none, and rejecting that would break
every real client while stopping no browser. A browser Origin not on the list
is rejected. Both branches are pinned by tests.

`GET /healthz` and `GET /readyz` are unauthenticated for probes and disclose
nothing about library contents. `/readyz` fails on a schema-version mismatch,
so a server deployed against a database it was not built for is pulled from
service rather than answering wrongly.

## Registering the server with a client

For a client with native HTTP MCP support:

```json
{
  "mcpServers": {
    "docsearch": {
      "type": "http",
      "url": "https://docsearch.your-tailnet.ts.net/mcp",
      "headers": { "Authorization": "Bearer YOUR_TOKEN" }
    }
  }
}
```

### Claude Desktop

**Claude Desktop (tested on 1.24012.9) does not accept HTTP entries in
`claude_desktop_config.json`.** It logs
`Skipped invalid MCP server config entries: { invalidServers: ['docsearch'] }`
and then `[localMcpBridge] no stdio servers connected`. That file takes stdio
servers only, so the HTTP service is reached through a bridge.

Install `deploy/client/docsearch-mcp-bridge` to `~/.local/bin/`, then:

```json
{
  "mcpServers": {
    "docsearch": {
      "command": "/Users/YOU/.local/bin/docsearch-mcp-bridge",
      "args": []
    }
  }
}
```

The bridge reads the token from `~/.config/docsearch/token.env` at launch
rather than taking it from the config file. `claude_desktop_config.json` is the
file most likely to end up in a screenshot or a bug report; the token should
not be in it.

Confirm with `[LocalMcpServerManager] Connected to docsearch (7 tools)` in
`~/Library/Logs/Claude/main.log`.
