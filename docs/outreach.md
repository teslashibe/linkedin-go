# Approved outreach contract

This SDK supplies deterministic LinkedIn transport for a host application that
has already approved a sender, destination, and exact message. The host owns
workspace authorization, persistence, operation leases, approval, scheduling,
cadence, cancellation, and account reconnection. These methods introduce no new
raw provider MCP tools.

The implementation has offline fixture coverage. No live LinkedIn request,
account login, DM, comment, or invitation was made to verify this release.
Consumers should keep delivery independently gated until their authorized live
tests prove each action. A successful account connection proves authentication,
not any write endpoint.

## Identity and session

`GetMe(ctx)` accepts a direct authenticated mini-profile or an explicit reference
to exactly one included self entity. Unrelated included profiles cannot identify
the sender. Missing, conflicting, or errored self evidence fails closed. The
returned identity is `urn:li:fsd_profile:<opaque-id>`; equivalent mini-profile IDs
canonicalize to that identity. Vanity names and numeric member IDs do not
substitute for it.

`ResolveMember(ctx, profileURLOrSlug)` requires one exact matching profile entity,
then reads relationship metadata. Degree zero means unknown. First-degree
connection evidence needs an explicit recipient association; two-member
connection tuples also bind their two IDs to the recipient and verified sender.
Call `GetMe` before resolving a tuple-based relationship. Legacy `networkinfo`
is a safe read fallback after a missing modern endpoint and does not establish
invitation availability.

All write methods perform their own strict sender probe before destination
verification. `ErrSenderIdentityMismatch` and `ErrRecipientIdentityMismatch`
distinguish account replacement from a changed target. The host must reconnect
the same immutable sender or obtain fresh approval; changing either identity is
not a retry.

`VerifiedMemberURN()` reports only the last successful strict self proof. Fresh
`GetMe` clears the old proof before dispatching. `AuthSnapshot()` exports the
live cookie jar for the LinkedIn origin. Imported sessions and rotated auth
cookies retain one canonical origin session, so CSRF and cookie values agree.
The host compares the proof with its expected member before encrypted,
lease-fenced persistence. Cookie values never belong in logs or receipts.

Keep the same residential proxy and captured browser UA throughout browser login,
native reads, and writes. `BrowserProfileFromUserAgent` supports desktop Chrome
and derives its Chromium version, platform, and mobile hint. It cannot infer
timezone, display, language, or the exact browser brand list. Unknown values stay
absent; `x-li-track` is omitted for a partial profile. Supply captured values with
`WithBrowserProfile` when available.

## Actions and exact text

| Action | SDK method | Preconditions | Result |
| --- | --- | --- | --- |
| Ordinary one-recipient DM | `SendMessageWithReceipt` | Exact profile/URN match, observed first-degree relationship, verified sender | Message and conversation URNs from the creation acknowledgement |
| Top-level post comment | `CreateComment` | Observed activity and actual share/ugcPost thread, verified sender | Created comment URN associated with the resolved post |
| Nested comment reply | `CreateComment` | Same post checks, parent observed on that post | Created comment URN distinct from the parent |
| Connection invitation with note | `SendConnectionInvitation` | Exact recipient, explicit no-connection/no-invitation evidence, verified sender | Invitation URN from the creation acknowledgement |

DMs and comments use a conservative application cap of 1,000 UTF-16 code units;
invitation notes use 200. These caps do not claim LinkedIn's undocumented transport
maximums or a particular account entitlement. Blank or invalid UTF-8 text fails
validation. Accepted text retains its whitespace and Unicode exactly; the SDK
does not trim, normalize, truncate, personalize, or add mentions.

DMs create one conversation with one profile recipient. Group messages,
conversation targets, InMail, inbox reads, and incoming-response tracking are
outside this contract. Invitations always include the exact required note; a
note-free invitation is never substituted. An already-connected recipient, pending
invitation, email requirement, exhausted note allowance, or unknown relationship
blocks or returns a typed rejection. Note allowance cannot be proven from the
available relationship read; `verifyQuotaAndCreateV2` remains the authoritative
write-time check. Account-specific quotas and recipient restrictions remain live
release gates.

## Comment ancestry and acknowledgement fidelity

`ResolveCommentTarget` accepts strict LinkedIn HTTPS `/feed/update/<URN>` and
`/posts/...-activity-<id>-...` links, or canonical activity/share/ugcPost URNs.
It binds activity and backend post identifiers inside one identified update/post
entity and its explicit metadata. Sibling entities cannot supply half of that
association. It does not assume the activity ID equals the share/ugcPost ID.

Parent identities may use legacy `comment` tuples or modern `fsd_comment` and
`fsd_normComment` tuples. Modern tuples put the comment ID first; the canonical
legacy representation puts the post first. The parent must be observed in the
bounded existing comments read, currently at most 50 returned comments. A missing
parent, unsupported provider shape, or deeper parent outside that read fails
closed. Pagination or an exact-parent fetch would be a separate bounded extension.
There is no fallback to a top-level comment.

Creation results must identify the explicitly created entity or a unique explicit
reference into `included`. Unrelated included records, old parent comments,
missing references, contradictory aliases, malformed identities, and in-band
provider errors cannot become success receipts. Message and conversation aliases
must agree within their own family, including viewer ownership when encoded.
When the creation object includes text, sender, recipient, or parent evidence,
it must agree with the approved request.

Current primary comment-creation evidence exposes only the created entity ID.
The bounded member author/actor/commenter variants are informed by the documented
[comment read schema, lines 1465–1563](https://github.com/crouton-labs/capture/blob/91fb1cf3bc206ad2493321550c51f8310a583160/vault/libs/linkedin/posts/index.ts#L1465-L1563).
An author field with unresolved or unsupported identity becomes unknown; the SDK
does not recursively interpret renderers or claim a captured creation author echo.

`PostComment.Text`, `PostURN`, and `ParentURN`, and `InvitationReceipt.RecipientURN`
retain the checked request association. They are not a claim that the provider
echoed those fields. The created identifiers are provider acknowledgement
evidence; delivered timestamps and URLs are never fabricated.

## One write attempt

Every native POST uses one `http.Client.Do` call. Safe warm-up and preflight reads
finish before it. POST redirects, transparent replay bodies/idempotency headers,
hidden rewarming, alternate write endpoints, and configured read retries cannot
repeat the write. This guarantee applies to existing POST methods as well.

Transport failures, lost bodies, redirects, 5xx, HTML success pages, and invalid
creation acknowledgements return `ErrWriteUnknown`. The host must mark the row
unknown and never automatically repeat it. Explicit authorization, rate-limit,
and recognized rejection responses retain their typed error and safe HTTP status/
Retry-After metadata through `WriteError`. Safe reads may still repeat under their
configured retry policy. This is not provider-side exactly-once delivery.

## Pinned protocol evidence

The following links identify author-owned code and research used to recover
schemas. Captured descriptions and synthetic fixture IDs are evidence for shape;
they do not establish live delivery by this library. No provider token or private
capture body is stored here.

| Contract | Pinned source | How it was used |
| --- | --- | --- |
| Strict authenticated mini-profile and old comment acknowledgement handling | [S'more profile.go, commit 10a3165, lines 17–106](https://github.com/teslashibe/smore/blob/10a3165c5297277ec58f267bac1641a55d292a1f/backend/vendor/github.com/teslashibe/linkedin-go/profile.go#L17-L106), [comment fixtures, lines 169–252](https://github.com/teslashibe/smore/blob/10a3165c5297277ec58f267bac1641a55d292a1f/backend/internal/mcp/platforms/linkedin_test.go#L169-L252) | Recovered selected behavior from tracked vendor code. Its v1.7.2 label did not match published upstream source; the permissive self fallback and retrying writer were removed |
| Nested NormComments-43 payload and post-dispatch 500 behavior | [linkedin-relay W5 research, commit eacd3db, lines 261–290](https://github.com/gabros20/linkedin-relay/blob/eacd3db2ca281727f5fa7a7fb46dbc2bb1d9324b/docs/research/W5-sdui-writes.md#L261-L290) | Author's captured nested reply uses the short `activity:` parent tuple as `threadUrn`; missing decoration could create a reply and still return 500 |
| Comment tuple order, top-level/nested write, direct created entity | [capture posts, commit 91fb1cf, lines 213–227](https://github.com/crouton-labs/capture/blob/91fb1cf3bc206ad2493321550c51f8310a583160/vault/libs/linkedin/posts/index.ts#L213-L227), [lines 314–360](https://github.com/crouton-labs/capture/blob/91fb1cf3bc206ad2493321550c51f8310a583160/vault/libs/linkedin/posts/index.ts#L314-L360) | Current author implementation supplies modern comment tuple conversion and NormComments request/result shape |
| Relationship read union and invitation result | [capture connections, commit 91fb1cf, lines 379–450](https://github.com/crouton-labs/capture/blob/91fb1cf3bc206ad2493321550c51f8310a583160/vault/libs/linkedin/connections/index.ts#L379-L450), [lines 503–531](https://github.com/crouton-labs/capture/blob/91fb1cf3bc206ad2493321550c51f8310a583160/vault/libs/linkedin/connections/index.ts#L503-L531) | Current relationship endpoint and explicit no-invitation/pending union. Missing evidence stays unknown; note slicing and receipt-free success were not copied |
| Connection tuple with recipient reference | [linkedin-toolkit endpoints, commit d6b4fcc, lines 190–200](https://github.com/vicnaum/linkedin-toolkit/blob/d6b4fcc8917e1f72461dc3932c6b4b5553ec2559/references/endpoints.md#L190-L200) | Author-documented normalized connection shape; both tuple members and recipient reference must agree |
| Invitation with exact note | [linkedin-mcp endpoints, commit caa2737, lines 453–463](https://github.com/devag7/linkedin-mcp/blob/caa27376ea30bfb34e15f2a1a63a5423c3b40bee/src/browser/endpoints.ts#L453-L463) | `verifyQuotaAndCreateV2`, invitation decoration, `memberProfile`, and `customMessage`; no older endpoint fallback |
| Ordinary messengerMessages request and normalized creation result | [allman-cli messages, commit d19fb68, lines 249–360](https://github.com/tarkaai/allman-cli/blob/d19fb681c0b33116f8e9f8dad1ab23e900fb9d4f/src/linkedin/api/endpoints/messages.ts#L249-L360), [lines 437–480](https://github.com/tarkaai/allman-cli/blob/d19fb681c0b33116f8e9f8dad1ab23e900fb9d4f/src/linkedin/api/endpoints/messages.ts#L437-L480) | One profile recipient, own mailbox, origin token/tracking ID, explicit normalized message result. Missing-result fallbacks and fabricated timestamps were removed |
| Synthetic message/conversation alias fixtures | [allman-cli message parser, commit d19fb68, lines 253–278](https://github.com/tarkaai/allman-cli/blob/d19fb681c0b33116f8e9f8dad1ab23e900fb9d4f/tests/unit/message-parser.test.ts#L253-L278), [normalized sender fixture, commit caa2737, lines 9–17](https://github.com/devag7/linkedin-mcp/blob/caa27376ea30bfb34e15f2a1a63a5423c3b40bee/tests/normalize-messaging.test.ts#L9-L17) | Synthetic identifiers preserve described frontend/backend aliases and explicit sender associations. Viewer-owned message URNs do not prove the author |

## Offline validation and consumer release gates

CI clears provider auth and skips the repository's explicitly named integration
tests before running `go test -race -count=1 ./... -skip '^TestIntegration_'`.
Fixtures cover authenticated identity ambiguity, recipient/edge binding,
relationship eligibility, exact UTF-16 text, top-level and nested payloads,
sibling decoys, rotated cookies and CSRF, explicit receipt conflicts, and uncertain
single-dispatch outcomes. `go vet ./...` and `go build ./...` remain required.

Before enabling delivery, a consumer must independently prove each intended
ordinary DM, top-level/nested comment, and exact-note invitation on explicitly
authorized accounts, including resulting provider identifiers and note/text
association. Connection-only approval does not authorize those sends. Inbox
reading, InMail, note entitlement discovery, arbitrary nested ancestry pagination,
and provider-side idempotency remain outside this release.
