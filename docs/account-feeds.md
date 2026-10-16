# Account observation reads

`ListRecentConnections` and `ListActivityConversations` make one read per page
after the operation's normal warm-up. Call `GetMe` once in the host's bounded
operation, require its canonical identity to match the expected sender, and
retain the same client, cookies, proxy, request gate and account lease. The host
owns authorization, schedule, request allowance, checkpoints and persistence.
These methods add no raw provider MCP tools and perform no writes.

## Recent connections

`ListRecentConnections(ctx, sender, RecentConnectionsParams{Start: 0, Count: 10})`
uses `/relationships/dash/connections`, the `ConnectionListWithProfile-16`
decoration, `q=search` and `sortType=RECENTLY_ADDED`. Count defaults to 10 and is
bounded to 49. The returned connection resource must resolve exactly once and
explicitly identify the canonical recipient. Its opaque or two-member identity
must agree with that recipient and, for tuples, the verified sender. Missing,
conflicting or partial referenced evidence rejects the page.

`ConnectedAt` is the connection resource's optional `createdAt` in milliseconds.
It is not an invitation receipt or an inferred acceptance timestamp. A host can
compare it with a confirmed invitation's send time; zero means unknown.

The page requires the returned paging start and count. `NextStart` advances by
the number of returned resources; a short verified window or explicit total
establishes the end of that traversal. `TotalKnown` distinguishes absent totals
from zero. Offsets are not durable snapshots. Additions and removals shift pages;
hosts must overlap stable connection identities, revisit the head and preserve
unfinished coverage across checks. A missing person never proves removal,
rejection or expiry. Do not remove historical acceptance events on that basis.

The route and response fields are documented in the
[pinned connection implementation](https://github.com/crouton-labs/capture/blob/91fb1cf3bc206ad2493321550c51f8310a583160/vault/libs/linkedin/connections/index.ts#L207-L348).
The consumer's bounded October 16 diagnostic proved the route and exact canonical
recipient matching on existing customer sessions without provider writes. The
fixture uses synthetic identities and retains no customer profile data.

## Inbox category pagination

`ListActivityConversations(ctx, sender, ActivityConversationListParams{Count: 20})`
uses the source-documented INBOX category query. Count is bounded to 25. Pass the
returned `NextCursor` unchanged in the next call. This is cursor pagination, not
a sync token. The existing `GetActivityConversations` initial-snapshot contract
and its `SyncToken` remain unchanged.

The category query does not filter by unread status. It returns conversation
identity, canonical participants, group state and last-activity time, including
already read conversations. It deliberately discards last-message bodies,
profile details and other optional decorations. Filter to authorized tracked
recipients before fetching changed threads with `GetActivityMessages`. Unchanged
conversations need no repeated history download.

`Complete` means the INBOX category cursor ended. It does not establish archive
coverage, a whole-account history snapshot, or deletion. The metadata object is
required; an omitted, null or empty `nextCursor` inside it ends the documented
traversal. Cursor loops, missing referenced identities, invalid participants and
unsupported collection envelopes fail without returning partial pages. Hosts
must commit observations before advancing checkpoints and overlap conversation
identities and last-activity times on later checks.

The separate `ActivityQueries.ConversationsByCategory` override accepts a reviewed
query ID. The source example is
`messengerConversations.9501074288a12f3ae9e3c7ea243bccbf`. A bounded October 16
consumer check validated this operation and its next-page cursor against the
live provider. It is not interchangeable with the
existing `messengerConversationsBySyncToken` operation. Request construction and
the optional cursor contract come from the
[pinned mailbox implementation](https://github.com/crouton-labs/capture/blob/91fb1cf3bc206ad2493321550c51f8310a583160/vault/libs/linkedin/messaging/index.ts#L188-L348).
The query registration and historical example are in the
[pinned query resolver](https://github.com/crouton-labs/capture/blob/91fb1cf3bc206ad2493321550c51f8310a583160/vault/libs/linkedin/helpers/index.ts#L423-L507).

No verified provider discriminator currently identifies invitation-note messages.
Human-message classification is not invitation or ordinary-DM provenance. Do
not invent it from body equality or message order. Saved invitation context must
retain its separate receipt-backed provenance until a provider linkage is
captured and reviewed.

The same read-only check returned an accepted-invitation thread and six history
messages. Five had ordinary matching sender/actor identities; one outbound entry
had the recipient as actor, a null origin token and no invitation linkage. That
entry stays `unknown`. `activity_ambiguous_note.json` preserves the structural
observation with synthetic IDs, message text and timestamps. No test invitation,
DM or read-marking request was made.
