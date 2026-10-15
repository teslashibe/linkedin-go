package linkedin

import (
	"context"
	"encoding/json"
)

// SendConnectionInvitation sends the exact required note. Unknown eligibility
// fails before dispatch, and no invitation without its note is substituted.
func (c *Client) SendConnectionInvitation(ctx context.Context, p InvitationParams) (*InvitationReceipt, error) {
	if err := validateOutreachText(p.Note, InvitationNoteLimit); err != nil {
		return nil, err
	}
	recipient, err := CanonicalMemberURN(p.RecipientURN)
	if err != nil {
		return nil, ErrInvalidParams
	}
	me, err := c.requireSender(ctx, p.ExpectedSenderURN)
	if err != nil {
		return nil, err
	}
	member, err := c.ResolveMember(ctx, p.RecipientProfileURL)
	if err != nil {
		return nil, err
	}
	if err := invitationRecipient(member, recipient, me.URN); err != nil {
		return nil, err
	}
	return c.sendInvitation(ctx, p, recipient, nil)
}

func invitationRecipient(member *Member, recipient, sender string) error {
	if member.URN != recipient {
		return ErrRecipientIdentityMismatch
	}
	switch member.InvitationState {
	case "connected":
		return ErrAlreadyConnected
	case "pending":
		return ErrInvitationPending
	case "available":
	default:
		return ErrEligibilityUnknown
	}
	if recipient == sender {
		return ErrInvalidParams
	}
	return nil
}

func (c *Client) sendInvitation(ctx context.Context, p InvitationParams, recipient string, guard func() error) (*InvitationReceipt, error) {
	payload, _ := json.Marshal(map[string]any{
		"invitee":       map[string]any{"inviteeUnion": map[string]any{"memberProfile": recipient}},
		"customMessage": p.Note,
	})
	body, _, err := c.makeGuardedWriteRequest(ctx, apiBase+"/voyagerRelationshipsDashMemberRelationships?action=verifyQuotaAndCreateV2&decorationId=com.linkedin.voyager.dash.deco.relationships.InvitationCreationResultWithInvitee-2", payload, guard)
	if err != nil {
		return nil, err
	}
	value, err := createdValue(body)
	if err != nil {
		return nil, unknownReceiptReason(writeUnknownCreationValue)
	}
	urn := ""
	for _, key := range []string{"invitationUrn", "*invitation", "entityUrn"} {
		if observed, exists := value[key]; exists {
			candidate, ok := observed.(string)
			if !ok || !receiptURN(candidate, "urn:li:fsd_invitation:") || (urn != "" && urn != candidate) {
				return nil, unknownReceiptReason(writeUnknownInvitationReceipt)
			}
			urn = candidate
		}
	}
	if urn == "" {
		return nil, unknownReceiptReason(writeUnknownInvitationReceipt)
	}
	if !invitationRecipientMatches(value, recipient) {
		return nil, unknownReceiptReason(writeUnknownIdentityMismatch)
	}
	if observed, exists := value["customMessage"]; exists && observed != p.Note {
		return nil, unknownReceiptReason(writeUnknownRequestMismatch)
	}
	return &InvitationReceipt{InvitationURN: urn, RecipientURN: recipient}, nil
}

func invitationRecipientMatches(value map[string]any, recipient string) bool {
	nodes := []map[string]any{value}
	if observed, exists := value["invitee"]; exists {
		invitee, ok := observed.(map[string]any)
		if !ok || hasErrorEnvelope(invitee) {
			return false
		}
		nodes = append(nodes, invitee)
		if observed, exists := invitee["inviteeUnion"]; exists {
			union, ok := observed.(map[string]any)
			if !ok || hasErrorEnvelope(union) {
				return false
			}
			nodes = append(nodes, union)
		}
	}
	for _, node := range nodes {
		for _, key := range []string{"memberProfile", "inviteeUrn", "recipientUrn"} {
			if observed, exists := node[key]; exists {
				raw, ok := observed.(string)
				member, err := CanonicalMemberURN(raw)
				if !ok || err != nil || member != recipient {
					return false
				}
			}
		}
	}
	return true
}
