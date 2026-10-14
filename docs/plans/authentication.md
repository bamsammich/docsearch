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

## Four modes, and only one of them is new work

| mode | what it does | who it is for |
|---|---|---|
| `none` | every request is the built-in user | a laptop, a trusted network |
| `token` | the v1 `DOCSEARCH_TOKEN`, mapped to the built-in user | an install upgrading from v1 |
| `password` | an account signs in, docsearch holds the credential | a deployment with no provider |
| `oidc` | an account signs in through any OIDC provider | anyone who already runs one |

`none` and `token` are the behaviour that ships today, named. Keeping them is
not compatibility theatre: a deployment with one library should not be made
to run an identity provider, and phase 04's `owner.Builtin` is already the
account they resolve to.

`password` is the mode that looks small and is not. Holding a password means
a hashing choice, a reset path, a lockout policy and a session store, none of
which docsearch is better at than a provider. It lands last, and only if
someone wants a deployment without a provider; the plan carries it because
the mode list did, not because anyone has asked for it.

## Steps

| step | what lands | how it is checked |
|---|---|---|
| 5a | the spike that settles whether claude.ai and an OIDC provider need docsearch to be an authorization server at all | a written finding under `docs/research/`, naming what was exercised against both |
| 5b | `users` gains an issuer and a subject, holding whichever identity 5a settled, and creating an account creates its partition | the migration applied and rolled back with two accounts present; two accounts from different providers sharing a subject stay apart; a second account's library is invisible to the first, through the same isolation suite phase 04 built |
| 5c | modes `none` and `token`, named and selected by configuration, resolving to the built-in user | the suite that covers today's behaviour, now asserting which mode produced it |
| 5d | the resource server: protected resource metadata, the 401 and 403 shapes, and the audience check | a token for another audience refused, one missing a scope answered 403, and the metadata fetched by a client |
| 5e | `oidc` against a real provider, with the account created on first sign-in | the end-to-end suite signing in as two accounts and reading two libraries |
| 5f | whatever 5a says is still needed of an authorization server | the grant the spike exercised, end to end |
| 5g | `password`, if it is still wanted | sign-in, reset and lockout, each as its own case |

Steps 5b through 5d are worth doing whatever 5a returns. A resource server
validates tokens from someone, and a user needs an identity, in both
answers. Only 5f depends on the spike for whether it happens at all, and 5b
depends on it for what it stores.

## Not settled by this plan

**What a token means for the CLI.** `docsearch` talks ConnectRPC to the
server with a bearer token today, and nothing about that is an MCP client. A
human at a terminal is not going through a browser redirect for every
command, so the CLI keeps the token mode and the plan does not pretend
otherwise. Whether a signed-in account can mint a token for its own CLI use
is a question for phase 05 to answer and this plan does not.

**Who may sign in.** A provider can authenticate someone docsearch should
still refuse, and a claim in the token is the usual way a deployment says
which accounts it allows. Checking a claim is a small thing to implement and
a policy question to get wrong, so step 5e names which claim it reads and
where the check lives, and does not invent a role system inside docsearch.

**What happens to a library when an account goes away.** Deleting a user
means deleting a partition, and the foreign keys phase 04 added refuse a user
with documents. Nothing in this phase deletes an account, and the first
feature that does will have to decide between cascading and refusing.

**Session binding.** `TokenInfo.UserID` exists so a transport can refuse a
session whose requests change user part way through. The MCP route is
stateless by choice, so nothing in docsearch holds a session today. Should a
later step want one, the field is already carried.
