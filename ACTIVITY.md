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

Limited user-authorized burner checks authenticated and received HTTP 200 inbox
responses, but parsing failed. A second cookie export repeated that successful
authentication. Reusing each export in a later process received HTTP 401, while
the burner browser inbox remained usable. The disposable test harness failed to
retain refreshed cookie state between processes and has been corrected. Live
parser validation remains outstanding. No SDK provider write was performed.

Identity and relationship reads allow an explicit empty GraphQL `errors` list.
Non-empty lists and malformed error fields still reject the response. This
read-only handling does not relax write-receipt or exact-parent checks. The
regression suite reproduces the empty-list failure with synthetic profiles;
confirmation against the profile response reported by the companion application's
Keywork diagnosis remains separate.

The production diagnosis identified an optional education-company lookup returning
a GraphQL `DataFetchingException` with status 404. `ResolveMember` tolerates only
that exact path, bound through the result list to the selected canonical member.
It still verifies relationship and invitation eligibility independently. Other
non-empty profile errors return `ErrProfileIncomplete` and never authorize a send.
Offline tests cover authentication errors, identity-field errors, missing paths,
incorrect result bindings and extra unresolved errors. A live resolver check is
required before shipping this correction.
