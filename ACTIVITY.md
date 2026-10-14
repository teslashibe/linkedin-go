# Bounded activity reads

The host owns consent, working hours, request allowances and storage. This SDK
provides deterministic provider reads. It does not start a poller or decide when
to contact LinkedIn.

`WithRequestGate` checks host capacity immediately before each HTTP attempt,
including warm-up, redirects and retries. Return an error to refuse dispatch.
Use a durable shared counter when multiple clients can use the same account.

After `GetMe` proves the expected canonical member:

- `GetMemberRelationship` reads one known member's relationship without another
  profile fetch. Only positive first-degree evidence establishes acceptance.
- `GetActivityConversations` reads an initial, bounded mailbox snapshot. Its
  absence of a thread is not evidence that the thread does not exist.
- `FindActivityConversation` checks an exact recipient pair and rejects groups
  or mismatched participants.
- `GetActivityMessages` follows an opaque backwards cursor. Only an explicit
  null cursor proves exhaustion. Missing or repeated cursors fail closed.

Activity reads accept ordinary GraphQL responses and normalized Voyager
`data/data` envelopes with uniquely resolvable `included` references. They never
return a partial successful page after a parse or identity failure. Hosts must
persist partial progress themselves and must not infer delivery failure from
an absent message.

Message kinds are `human`, `system` or `unknown`. Hosts should count only human
inbound events, preserve provider message/conversation IDs, and deduplicate
observations against confirmed send receipts. Do not mark a conversation read
as a side effect of monitoring.

These methods are intentionally excluded from the SDK's raw MCP server. A host
should expose its consented, tenant-scoped stored activity through its own tools.

## Preview validation status

Offline fixture and race tests cover identity checks, normalized responses,
cursor handling, system/unknown message classification and per-attempt request
gating. A host refusal remains typed and proves that HTTP dispatch did not occur;
read retries stop at that refusal and an unsafe write is not marked ambiguous.

A limited user-authorized burner check authenticated, then encountered an
unsupported inbox response. The same supplied export later received HTTP 401
through the SDK, including with the original default transport restored. The
burner remains signed in and its browser inbox loads successfully. The SDK
session/transport difference and a successful live inbox read remain release
blockers; requesting a browser reconnect is not an established fix. No provider
write was performed for this change.
