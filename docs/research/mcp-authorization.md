# docsearch does not need to be an authorization server

Step 5a of `docs/plans/authentication.md` asked whether claude.ai and an OIDC
provider need docsearch to run its own OAuth authorization server. The
documentation answers no, and for a reason the plan did not anticipate. The
connector already offers to use a client the operator registered, which every
OIDC provider supports today.

The plan's own question, whether claude.ai supports Client ID Metadata
Documents, turns out to be the less important one.

## What the connector offers

Adding a custom connector in claude.ai presents a choice of how Claude
identifies itself to the authorization server, in three options that line up
with the three mechanisms the MCP specification defines:

| connector option | specification mechanism | what the provider must support |
|---|---|---|
| Use Claude's published identity (recommended) | Client ID Metadata Documents | fetching a URL-formatted `client_id`, and advertising `client_id_metadata_document_supported` |
| Register automatically | Dynamic Client Registration | a `registration_endpoint` |
| Use your own OAuth client | pre-registration | nothing beyond registering a client, which they all do |

Guessed: the mapping of "Claude's published identity" to Client ID Metadata
Documents. The support page names none of the three mechanisms, and the
inference rests on the published-identity wording matching what a metadata
document is, and on the three options matching the specification's three
mechanisms one for one. Step 5e can confirm it from the authorization request
claude.ai actually sends, where a URL-shaped `client_id` is unmistakable.

## Registration is not docsearch's problem at all

Reading the three options as a choice docsearch makes was the mistake in the
plan's framing. Registration happens between claude.ai and the authorization
server, and docsearch is not party to it. All docsearch does is publish
protected resource metadata naming which authorization server issues its
tokens, and validate what arrives.

So which mechanism a deployment uses is the operator's choice, made in the
connector's own settings, and it needs nothing from this codebase. A
deployment on a provider that implements Dynamic Client Registration picks
"Register automatically" and is done. One whose provider implements none of
the three still works, because the operator registers a client by hand.

One fact about the three does matter to docsearch. None of them is required
of a provider for `oidc` mode to work, so docsearch supports every provider
on the registration question without writing a line.

Caveat for a deployment that depends on Dynamic Client Registration: the
specification deprecates it, keeping it for backwards compatibility, and the
connector still offers it. Nothing is breaking today. Should the connector
ever drop it, that deployment moves to a registered client or to a provider
that publishes `client_id_metadata_document_supported`, and docsearch is
unaffected either way.

## Why pre-registration settles the authorization-server question

The specification's priority order puts pre-registration first: a client uses
credentials it already has before it tries anything else. So an operator
registers Claude once in their provider's admin interface, pastes the client
into the connector, and the provider is the authorization server. docsearch
stays a resource server.

Nothing about that path is new or unproven. It is how every OAuth integration
worked before Dynamic Client Registration existed, and it needs no feature
from the provider that a provider could lack.

The two other options are better when they work, because neither asks the
operator to register anything. Client ID Metadata Documents also travel,
since a URL-shaped client identifier is resolved by whichever authorization
server sees it, so one survives a change of provider where a pre-registered
client does not. Worth preferring, not worth building an authorization server for.

## The portability question is the audience, not registration

docsearch is written for anyone to run against their own provider, so the
question that matters is which provider features a deployment cannot work
without. Registration is not one of them. The audience is.

An MCP server **MUST** validate that an access token was issued for it, by
the `resource` indicator of RFC 8707, and **MUST NOT** accept a token issued
for anything else. docsearch can only do that if the token carries docsearch
as its audience, and the token is minted by the provider.

So the gating question is not registration at all:

> Does the provider honour the `resource` parameter and issue a token whose
> audience is docsearch?

A provider that ignores it issues a token audienced to itself or to its
default client, and docsearch then faces a choice the specification does not
permit: accept a token minted for someone else, or refuse every request. RFC 8707 is not universally implemented, and whether a given
provider implements it is a fact about that provider.

What a deployment needs from its provider, then, reduces to one row:

| what the provider can bind an audience with | what the operator does |
|---|---|
| `resource`, per RFC 8707 | nothing; docsearch's canonical URI lands in `aud` |
| a mechanism of its own, such as a scope | configures that scope and the expected audience value |
| nothing at all | cannot use `oidc` mode until docsearch mints its own tokens |

Only the third row justifies step 5f. The second row is the common case and
costs two configuration values rather than an authorization server, which is
why both are listed as decisions above.

## A provider that binds the audience its own way

Zitadel, taken as a worked example because it is a provider people actually
run, does not accept `resource` at its authorization endpoint. The parameter
appears in its documentation only under token exchange, marked as not
supported. A client sends `resource` regardless, because the specification
requires clients to send it whether or not the server understands it, so the
parameter is simply ignored.

Zitadel does let a token carry an audience, through a reserved scope rather
than through `resource`:

```
urn:zitadel:iam:org:project:id:{projectid}:aud
```

Requesting that scope adds the named project's id to the `aud` claim. So the
audience binding the specification asks for is available; it is spelled
differently and its value is a project id rather than docsearch's own URI.

Which gives docsearch a lever, because the scopes a client requests come from
docsearch. A resource server names the scope it needs in its 401 challenge,
and a client that gets no scope there falls back to `scopes_supported` from
the protected resource metadata. Both documents are docsearch's own output,
so an operator whose provider needs a custom scope to bind an audience
configures that scope and the client asks for it.

Two things follow for the code, and they are the real output of this spike:

| decision | why |
|---|---|
| the expected audience is configuration, not docsearch's own URL | a provider may bind an audience to a value of its own, and refusing those tokens would refuse the provider rather than an attacker |
| `scopes_supported` and the challenge scope are configuration | they are how docsearch asks a client for whatever its provider needs |

Unverified: what a Zitadel access token's `aud` actually contains for an
authorization-code grant. Its introspection endpoint reports a token active
only when the requesting client is in the audience, which suggests the
requesting client's id is there by default and docsearch's project is not
until the scope above is requested. Only a captured token settles it.

## What is left to test, and against what

Reading cannot answer the audience question, and it is the only one worth a
real experiment now:

1. Read the provider's authorization server metadata and record whether
   `client_id_metadata_document_supported` is present, and whether the
   `resource` parameter appears in anything it advertises.
2. Register a client, add the connector, and capture the token claude.ai
   presents to docsearch. The `aud` claim is the answer.
3. Record what the authorization request carried, including which
   registration mechanism it used, since that fixes the published-identity
   guess only if the deployment chose published identity.

Step 5d builds the audience check either way, so the experiment belongs
beside it rather than before it. The plan's step 5f, an authorization server,
stays written down and unbuilt until a deployment turns up whose provider
ignores `resource`.

## Sources

| what | where |
|---|---|
| the three mechanisms, their priority order, and the deprecation of Dynamic Client Registration | MCP specification, Client Registration |
| the audience `MUST`, and `MUST NOT` accept or transit another token | MCP specification, Authorization |
| the connector's three OAuth client options | the Claude support article on custom connectors using remote MCP |
| no `resource` at the authorization endpoint, and `resource` unsupported in token exchange | Zitadel's OpenID Connect endpoints documentation |
| the reserved scope that adds a project to `aud` | Zitadel's scopes documentation |

Both specification pages were read in their draft revision. A specification
in draft moves, and the Dynamic Client Registration deprecation arrived after
this project's first authentication decision was recorded, so the next person
to touch authentication should read them again rather than trust this note.
