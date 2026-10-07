package mcp

// Excluded enumerates exported methods on *linkedin.Client that are
// intentionally not exposed via MCP. Each entry must have a non-empty reason.
//
// The coverage test in mcp_test.go fails if any exported method on *Client is
// neither wrapped by a Tool nor present in this map (or vice-versa: if an
// entry here doesn't correspond to a real method).
//
// When the underlying client gains a new method:
//   - prefer to add an MCP tool for it (see search.go / groups.go / etc.)
//   - if the method is unsuitable for an agent (internal observability,
//     auth-only helper, etc.), add it here with a reason
var Excluded = map[string]string{
	"AuthSnapshot":             "host-owned encrypted session refresh after fenced operations",
	"VerifiedMemberURN":        "host-owned strict authenticated proof for fenced session refresh",
	"GetMe":                    "host-owned authenticated connection identity probe",
	"ResolveMember":            "host-owned exact outbound identity and eligibility preflight",
	"ResolveCommentTarget":     "host-owned safe outbound target and ancestry preflight",
	"SendMessageWithReceipt":   "host-owned explicitly approved outreach operation; no raw provider tool",
	"CreateComment":            "host-owned explicitly approved outreach operation; no raw provider tool",
	"SendConnectionInvitation": "host-owned explicitly approved outreach operation; no raw provider tool",
	"HealthCheck":              "session verification is owned by the host so it can coordinate login and challenge UI",
	"RateLimit":                "internal observability; surfaced via the host application's MCP middleware, not as a callable tool",
	"RequestsRemaining":        "internal observability for the human pacer; surface via the host's status reporting, not as a callable tool",
	"BudgetExhausted":          "internal observability for the human pacer daily budget; surface via the host's status reporting, not as a callable tool",
	"PendingRequests":          "internal observability for the per-client serialization queue; surface via the host's status reporting, not as a callable tool",
	"CooldownUntil":            "internal observability for the operator-imposed cooldown gate; surface via the host's status reporting, not as a callable tool",
	"InCooldown":               "internal observability for the operator-imposed cooldown gate; surface via the host's status reporting, not as a callable tool",
	"Role":                     "construction-time tag for multi-account separation; not an agent-facing capability",
	"AssertRole":               "construction-time guard for multi-account separation; not an agent-facing capability",
}
