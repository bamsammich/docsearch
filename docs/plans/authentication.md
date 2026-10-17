# Signing in, without owing anything to one provider

Phase 05 of v2. A request arrives carrying a credential, docsearch decides
which user it belongs to, and `internal/owner` hands that user to the layer
phase 04 built. The phase ends when a second account can sign in, see only
its own library, and when turning authentication off still serves the
built-in user exactly as it does today.

Phase 04 left the seam deliberately: `owner.Middleware` takes the user as an
argument and every handler asks `owner.From`, so nothing downstream knows how
the answer was reached. Filling that argument from a credential is the whole
phase.

## Settle the authorization server before writing one

The decision recorded earlier was that docsearch runs its own OAuth
authorization server, so an operator's identity provider needs no Dynamic
Client Registration. The MCP authorization specification has since deprecated
Dynamic Client Registration, retaining it "for backwards compatibility with
authorization servers that do not support Client ID Metadata Documents", and
asks authorization servers and clients to support Client ID Metadata
Documents instead.

That weakens the reason for building one. If an MCP client identifies itself
by a URL rather than by registering, an operator's provider can be the
authorization server directly, and docsearch stays a resource server that
validates tokens and advertises where they come from. The authorization
server is the largest and most security-sensitive thing this phase could
build, so whether it is needed at all is the first question rather than a
detail inside step 5c.

Two facts decide it, and neither is established here.

| question | why it decides |
|---|---|
| does claude.ai's connector client support Client ID Metadata Documents | if it does, no registration mechanism is needed from the provider |
| does an OIDC provider accept a client it did not register | one deployment's provider is not another's, so the answer has to come from a real one |

Step 5a answers both against the real claude.ai and a real provider, as a
spike with a written finding, the way phases 01 and 02 settled the extractor
and the engine. A spike that comes back yes deletes most of this phase. One
that comes back no makes docsearch its own authorization server, as planned,
and the spike says which grant and which endpoints are actually exercised
rather than which ones a specification lists.

## The specification is a short list of musts

The resource-server half is small and almost entirely prescribed. Each line
is a requirement on docsearch rather than a design choice.

| requirement | where |
|---|---|
| publish protected resource metadata | RFC 9728, a `MUST` for MCP servers |
| point a 401 at that metadata, with the scope needed | RFC 6750 section 3, a `SHOULD` |
| validate that a token was issued for this server | RFC 8707 section 2, a `MUST` |
| refuse a token issued for anything else, and never forward one | a `MUST NOT` on accepting or transiting |
| answer an insufficient scope with 403 and `insufficient_scope` | RFC 6750 section 3.1, a `SHOULD` |

The audience check is the one worth naming twice. A token minted for another
service, presented here, must be refused rather than introspected and
believed: accepting it makes docsearch a confused deputy against whatever
issued it.

## The SDK already does the resource-server half

`github.com/modelcontextprotocol/go-sdk/auth` is already a dependency, and
it carries most of what those requirements ask for.

| what it gives | what docsearch supplies |
|---|---|
| `RequireBearerToken`, the middleware with the 401 and 403 shapes | a `TokenVerifier` |
| `ProtectedResourceMetadataHandler`, the RFC 9728 endpoint | the metadata values |
| `TokenInfo.UserID`, carried to the handler | the mapping from a credential to a user |

`TokenInfo.UserID` is the seam. The verifier resolves a credential to a user
and the middleware carries it, so `owner.Middleware` stops taking a constant
and starts taking what the verifier found. Nothing in a service, a repository
or a store changes, because phase 04 already made the owner a parameter.

Writing an OAuth resource server by hand when the SDK ships one tested
against the specification would be the wrong kind of work. What stays ours is
the part that is about docsearch: which credential names which user.

Step 5d built that half in `internal/resource`, and found that a token
reaches a resource server in two forms. A signed JWT can be read locally
against the keys the issuer publishes. An opaque token can only be read by
asking the issuer, through introspection as RFC 7662 defines it, and some
providers issue opaque tokens unless told otherwise. Both are supported, so a
deployment need not reconfigure its provider to suit docsearch.

| form | read by | library |
|---|---|---|
| signed JWT | `internal/resource/signed`, against the issuer's key set | `coreos/go-oidc`, with its client check off because the audience is checked in `resource` |
| opaque | `internal/resource/introspect`, asking the issuer | `zitadel/oidc`'s resource-server client, a general OIDC library |
| anything else, with no introspection configured | refused by name | |

Both readers end in the same claims, so the checks that matter are written
once, in `resource`. A token passes when its issuer is the configured one,
it names a subject, and its audience holds the configured value, which
defaults to docsearch's own URI. Only asymmetric signatures are accepted,
since a key shared with the issuer is one every other client of that issuer
holds too.

Scopes split in two. `ScopesSupported` is advertised in the metadata, which
is what a client requests when a challenge names none, and so it is how an
operator asks for a provider-specific scope that binds the audience.
`RequiredScopes` must be on every token and are named in every challenge.
They are separate because a scope that binds an audience is not always
echoed back in the token's own `scope` claim, and requiring it would refuse
every token.

Some providers register every dynamic client into one shared project and
put its identifier in the audience of every token those clients receive.
Configuring that identifier as the expected audience would accept a token
minted for any other service registered the same way. The expected audience
has to be a value only docsearch's tokens carry.

Two limits were found and left:

| limit | consequence |
|---|---|
| the SDK's 401 and 403 challenges carry no `error` parameter, which RFC 6750 asks for; v1.8.0 is unchanged | a client learns the status and the scope, but not `invalid_token` or `insufficient_scope` by name |
| an ID token from the same issuer passes when its audience includes the configured value | a provider that puts a project in ID tokens' audience lets one be used as an access token; the `typ` header that would tell them apart is not set consistently across providers |

## A user gains an issuer and a subject

Phase 04 created `users` with an identifier and a timestamp, and said phase
05 adds an issuer and a subject. Two providers can both call someone
`alice`, so a subject alone collides; the pair is what an account is.

```
ALTER TABLE users ADD COLUMN issuer  text;
ALTER TABLE users ADD COLUMN subject text;
CREATE UNIQUE INDEX users_identity ON users (issuer, subject);
```

Both nullable, because the built-in user has neither and must keep working.
The row migration 6 created is the account a single-library deployment
already uses, and the first account to sign in claims it rather than starting
an empty library beside it, which is what the phase 04 comment promised.

Which pair gets stored depends on what step 5a returns, and the two answers
are not interchangeable.

| authorization server | what `issuer` and `subject` hold |
|---|---|
| the operator's provider | the `iss` and `sub` claims of the token itself |
| docsearch | the provider's `iss` and `sub`, read once at first sign-in, because the token docsearch mints carries its own issuer and a subject that is already the `user_id` |

Storing docsearch's own issuer in the second case would make every account
look like it came from one provider, which is the collision the pair exists
to prevent. So step 5b writes whichever the spike settled, and writing it
before 5a answers would be guessing.

Creating a user creates a chunk partition, which no request path does today.
Phase 05 is where account creation becomes a real operation, so the partition
is created with the row, in one transaction, or a document lands nowhere a
search will look.

Step 5b found that the role a request runs as can do neither part of it.
Phase 04 granted that role no `CREATE` on the schema and only `SELECT` on
`users`, so it is refused both the row and the partition, which is the right
answer to the question phase 04 was asking and the wrong shape for creating
an account. A `SECURITY DEFINER` function owned by the schema owner is the
narrow way through. A request asks for that one operation, the function runs
it with the owner's rights, and `EXECUTE` is granted to the request role
while `PUBLIC` is revoked. Its `search_path` is pinned, because
a definer function that resolves names through the caller's path runs the
caller's code with the owner's rights.

Three constraints came with the columns. An identity is whole or absent, so
an issuer without a subject is refused rather than passing an index that
only sees the pair. The unique index is `NULLS NOT DISTINCT`, which allows
exactly one account with no identity, the built-in one, where the default
would allow any number of accounts nobody can sign in to. And a `user_id` is
checked against an identifier-safe pattern, because it becomes part of a
table name, so a bad one is refused at the row rather than discovered when
the DDL fails.

## Four modes, and only `oidc` has to be built

| mode | what it does | who it is for |
|---|---|---|
| `none` | every request is the built-in user | a laptop, a trusted network |
| `token` | the v1 `DOCSEARCH_TOKEN`, mapped to the built-in user | an install upgrading from v1 |
| `password` | an account signs in, docsearch holds the credential | a deployment with no provider |
| `oidc` | an account signs in through any OIDC provider | anyone who already runs one |

`none` and `token` are the behaviour that ships today, named. Keeping them is
not compatibility theatre: a deployment with one library should not be made
to run an identity provider, and phase 04's `owner.Builtin` is already the
account they resolve to. So `oidc` is the only mode this phase has to build,
and `password` is the only one it might not build at all.

Step 5c settled two things about how a mode is chosen. A `Mode` counts from
one, so the zero value is not a mode and above all not `none`: a mode nobody
parsed must not read as the one that asks for no credential. And an unset
`DOCSEARCH_AUTH` resolves to `token`, which is what every v1 install already
runs, so naming the modes changed no deployment's behaviour.

`none` is refused on an address that is not loopback, whatever
`--allow-public-bind` says. That flag opts into being reachable from
elsewhere, not into serving a library to whoever arrives, and the two
together are an open document store that no flag spells clearly enough to
act on. A mode the plan names but no step has built is refused by its own
name rather than as an unknown word, so an operator who configured
`password` early learns which it is.

`password` is the mode that looks small and is not. Holding a password means
a hashing choice, a reset path, a lockout policy and a session store, none of
which docsearch is better at than a provider. It lands in step 5f, beside the
authorization server, because both make docsearch issue credentials of its
own and neither is worth building alone.

## Who gets a library, settled in 5e

Step 5e built `oidc` mode and answered the questions this plan had left to
it by following what self-hosted applications already do. Grafana and Gitea
both pair a switch for creating an account on first sign-in with an optional
claim every sign-in must carry, and both expect the provider to be the first
gate. docsearch copies the pair.

| setting | default | why |
|---|---|---|
| `--oidc-sign-up` | off | a provider that lets anyone register would otherwise hand anyone a library; Gitea starts closed for the same reason |
| `--oidc-required-claim-name` and `-value` | none | checked on every request rather than at account creation, so revoking the claim at the provider revokes access here |
| `--oidc-default-owner` | nobody | names who inherits the built-in library |

The built-in library goes to a named identity rather than to whoever signs
in first. Phase 04 promised the first account would claim it, which is safe
only while the operator is certain to be first. Migration 11 adds
`claim_builtin`, a definer function that writes an identity onto the one row
with none, so the claim is stored once and a second identity finds nothing
left to take.

The CLI keeps sending `DOCSEARCH_TOKEN` as its bearer token. In `oidc` mode
the ConnectRPC routes sit behind the same resource server as `/mcp`, so any
access token the provider issues for docsearch works there, and the CLI reads
the library of whoever the token names. A device-code sign-in for the CLI is
a convenience left for later; nothing about the server would change for it.

Still owed is the run against a real provider: register a client, add the
connector, and capture the token claude.ai presents. That settles what the
provider puts in `aud`, and the published-identity guess in
`docs/research/mcp-authorization.md`. It needs an operator with a provider,
so it is recorded here rather than claimed.

## Steps

| step | what lands | how it is checked |
|---|---|---|
| 5a | the spike that settles whether claude.ai and an OIDC provider need docsearch to be an authorization server at all | a written finding under `docs/research/`, naming what was exercised against both |
| 5b | `users` gains an issuer and a subject, holding whichever identity 5a settled, and creating an account creates its partition | the migration applied and rolled back with two accounts present; two accounts from different providers sharing a subject stay apart; a second account's library is invisible to the first, through the same isolation suite phase 04 built |
| 5c | modes `none` and `token`, named and selected by configuration, resolving to the built-in user | the suite that covers today's behaviour, now asserting which mode produced it |
| 5d | the resource server: protected resource metadata, the 401 and 403 shapes, and the audience check | a token for another audience refused, one missing a scope answered 403, and the metadata fetched by a client |
| 5e | `oidc` against a real provider, with the account created on first sign-in | the end-to-end suite signing in as two accounts and reading two libraries |
| 5f | docsearch issuing its own credentials, covering both reasons it might have to: a provider that can bind no audience, and the `password` mode | the grant the spike exercised, end to end; sign-in, reset and lockout each as its own case where `password` is in scope |

Steps 5b through 5d are worth doing whatever 5a returns. A resource server
validates tokens from someone, and a user needs an identity, in both
answers. Step 5b depends on the spike for what it stores, and 5f for whether
it happens at all.

5f is one step rather than two because its two triggers want the same
machinery. A provider that can bind no audience forces docsearch to mint
tokens audienced to itself, and the `password` mode forces docsearch to hold
a credential and issue something against it. Both make docsearch an issuer,
and an issuer built twice is an issuer with two sets of bugs. Deciding them
together also makes the cheaper answer visible: if neither trigger fires, the
machinery is never written.

## Not settled by this plan

**What happens to a library when an account goes away.** Deleting a user
means deleting a partition, and the foreign keys phase 04 added refuse a user
with documents. Nothing in this phase deletes an account, and the first
feature that does will have to decide between cascading and refusing.

**Session binding.** `TokenInfo.UserID` exists so a transport can refuse a
session whose requests change user part way through. The MCP route is
stateless by choice, so nothing in docsearch holds a session today. Should a
later step want one, the field is already carried.
