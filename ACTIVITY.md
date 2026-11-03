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
return a partial successful page after a parse or identity failure, with one
exception below. Hosts must persist partial progress themselves and must not
infer delivery failure from an absent message.

`ListActivityConversations` can return a partial page. A conversation that has
the sender but also a participant with no member identity, such as a company
page or a deleted account, is left out whole and counted in `Skipped`.
`SkippedKinds` lists at most 8 URN namespaces of those participants, such as
`fsd_company`, or `unknown` when there is none, and never an identifier. Hosts
must read `Skipped`: a thread absent from a page with a non-zero count may be one
of the skipped ones. `NextCursor` and `Complete` still describe the traversal.

The page still fails whole when a conversation lacks the sender
(`ErrSenderIdentityMismatch`), when a participant's `hostIdentityUrn` and
`entityUrn` name different members (`ErrIdentityUnavailable`), and when every
conversation on a non-empty page is skipped (`ErrIdentityUnavailable`), so a
provider format change cannot look like an empty inbox.

Message kinds are `human`, `system` or `unknown`. Hosts should count only human
inbound events, preserve provider message/conversation IDs, and deduplicate
observations against confirmed send receipts. Do not mark a conversation read
as a side effect of monitoring.

These methods are intentionally excluded from the SDK's raw MCP server. A host
should expose its consented, tenant-scoped stored activity through its own tools.

## Validation status

Offline fixture and race tests cover identity checks, normalized responses,
cursor handling, skipped non-member conversations, system/unknown message
classification and per-attempt request gating. A host refusal remains typed and proves that HTTP dispatch did not occur;
read retries stop at that refusal and an unsafe write is not marked ambiguous.

Early burner checks exposed parsing failures and a disposable test harness that
discarded refreshed cookies. The harness now preserves verified session state.
Subsequent user-authorized production diagnosis verified the exact-recipient
query, a 20-conversation mailbox page and message history. Profile resolution
also passed with the optional-field error described below. No SDK provider
write was performed during these checks.

Identity and relationship reads allow an explicit empty GraphQL `errors` list.
Non-empty lists and malformed error fields still reject the response. This
read-only handling does not relax write-receipt or exact-parent checks. The
regression suite reproduces the empty-list failure with synthetic profiles;
the separate partial response from the companion application's Keywork diagnosis
is handled as described below.

The production diagnosis identified an optional education-company lookup returning
a GraphQL `DataFetchingException` with status 404. `ResolveMember` tolerates only
that exact path, bound through the result list to the selected canonical member.
It still verifies relationship and invitation eligibility independently. Other
non-empty profile errors return `ErrProfileIncomplete` and never authorize a send.
Offline tests cover authentication errors, identity-field errors, missing paths,
incorrect result bindings and extra unresolved errors. The repaired resolver
passed a live check against the previously failing profile.

Normalized message-to-conversation backlinks retain their identity without
recursively expanding the conversation again. A backlink to another conversation
is rejected. Normal message actors may match their sender; that is not itself a
system event. Generated cards and unrelated or unresolved actors remain excluded
from human reply counts. The redacted production mailbox fixture covers this
shape, and a captured 20-conversation page parses in the offline replay.
