package linkedin

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
)

// OutreachSession holds one live sender proof for a host-owned operation. The
// host retains its account lease through preparation, reservation and Send.
// Close invalidates every prepared write; nothing is cached across sessions.
// A later GetMe on the same client also invalidates the proof.
type OutreachSession struct {
	client   *Client
	ctx      context.Context
	cancel   context.CancelFunc
	identity Profile
	proof    uint64
}

// OpenOutreach verifies the expected sender once on this client. ctx bounds only
// this probe. The host must close the session when its account lease ends and
// pass the operation context to Prepare and Send.
func (c *Client) OpenOutreach(ctx context.Context, expectedSenderURN string) (*OutreachSession, error) {
	if c == nil || ctx == nil {
		return nil, ErrInvalidParams
	}
	want, err := CanonicalMemberURN(expectedSenderURN)
	if err != nil {
		return nil, err
	}
	me, proof, err := c.getMe(ctx)
	if err != nil {
		return nil, err
	}
	if me.URN != want {
		return nil, ErrSenderIdentityMismatch
	}
	opctx, cancel := context.WithCancel(context.Background())
	return &OutreachSession{client: c, ctx: opctx, cancel: cancel, identity: *me, proof: proof}, nil
}

// Identity returns the authenticated identity, detached from session state.
func (s *OutreachSession) Identity() Profile {
	if s == nil {
		return Profile{}
	}
	return s.identity
}

// Close invalidates this operation's prepared writes without closing the host's
// HTTP transport. The host owns that transport and the account lease.
func (s *OutreachSession) Close() {
	if s != nil && s.cancel != nil {
		s.cancel()
	}
}

func (s *OutreachSession) check(ctx context.Context) error {
	if s == nil || s.client == nil || s.ctx == nil || ctx == nil {
		return ErrInvalidParams
	}
	if err := ctx.Err(); err != nil {
		return mapCtxErr(err)
	}
	if err := s.ctx.Err(); err != nil {
		return mapCtxErr(err)
	}
	if s.client.identityProof.Load() != s.proof || s.client.VerifiedMemberURN() != s.identity.URN {
		return ErrSenderIdentityMismatch
	}
	return nil
}

// bind keeps both the host's operation and this particular call cancellable.
func (s *OutreachSession) bind(ctx context.Context) (context.Context, func(), error) {
	if err := s.check(ctx); err != nil {
		return nil, nil, err
	}
	bound, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return bound, func() { stop(); cancel() }, nil
}

type preparedWrite struct {
	session *OutreachSession
	ctx     context.Context
	used    atomic.Bool
}

func (w *preparedWrite) begin(ctx context.Context) (context.Context, func(), error) {
	if w == nil || w.session == nil || w.ctx == nil || !w.used.CompareAndSwap(false, true) {
		return nil, nil, ErrInvalidParams
	}
	if err := w.check(ctx); err != nil {
		return nil, nil, err
	}
	bound, cancel, err := w.session.bind(ctx)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(w.ctx, cancel)
	return bound, func() { stop(); cancel() }, nil
}

func (w *preparedWrite) check(ctx context.Context) error {
	if err := w.ctx.Err(); err != nil {
		return mapCtxErr(err)
	}
	return w.session.check(ctx)
}

func (s *OutreachSession) prepareMember(ctx context.Context, expectedSender, expectedRecipient, profileURL string) (*Member, error) {
	if err := s.requireSender(expectedSender); err != nil {
		return nil, err
	}
	if expectedRecipient != "" {
		var err error
		expectedRecipient, err = CanonicalMemberURN(expectedRecipient)
		if err != nil {
			return nil, ErrInvalidParams
		}
	}
	member, err := s.client.ResolveMember(ctx, profileURL)
	if err != nil {
		return nil, err
	}
	if expectedRecipient != "" && member.URN != expectedRecipient {
		return nil, ErrRecipientIdentityMismatch
	}
	return member, s.check(ctx)
}

func (s *OutreachSession) requireSender(expected string) error {
	want, err := CanonicalMemberURN(expected)
	if err != nil {
		return err
	}
	if want != s.identity.URN {
		return ErrSenderIdentityMismatch
	}
	return nil
}

// PreparedInvitation binds the verified recipient and exact note to one Send.
// Copies share the same single-use state. Its zero value cannot send.
type PreparedInvitation struct {
	write  *preparedWrite
	params InvitationParams
	member Member
}

// PrepareInvitation resolves the exact profile and invitation eligibility.
// RecipientURN may be empty on first resolution; a supplied identity must match.
func (s *OutreachSession) PrepareInvitation(ctx context.Context, p InvitationParams) (*PreparedInvitation, error) {
	bound, done, err := s.bind(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := validateOutreachText(p.Note, InvitationNoteLimit); err != nil {
		return nil, err
	}
	member, err := s.prepareMember(bound, p.ExpectedSenderURN, p.RecipientURN, p.RecipientProfileURL)
	if err != nil {
		return nil, err
	}
	if err := invitationRecipient(member, member.URN, s.identity.URN); err != nil {
		return nil, err
	}
	return &PreparedInvitation{write: &preparedWrite{session: s, ctx: ctx}, params: p, member: *member}, nil
}

func (p *PreparedInvitation) Member() Member {
	if p == nil {
		return Member{}
	}
	return p.member
}

func (p *PreparedInvitation) Send(ctx context.Context) (*InvitationReceipt, error) {
	if p == nil {
		return nil, ErrInvalidParams
	}
	bound, done, err := p.write.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return p.write.session.client.sendInvitation(bound, p.params, p.member.URN, func() error { return p.write.check(bound) })
}

// PreparedMessage binds one first-degree recipient and exact body to one Send.
type PreparedMessage struct {
	write  *preparedWrite
	params DirectMessageParams
	member Member
}

// PrepareMessage accepts an empty RecipientURN only for first resolution.
// A supplied identity must match the exact profile and first-degree evidence.
func (s *OutreachSession) PrepareMessage(ctx context.Context, p DirectMessageParams) (*PreparedMessage, error) {
	bound, done, err := s.bind(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := validateOutreachText(p.Body, MessageLimit); err != nil {
		return nil, err
	}
	member, err := s.prepareMember(bound, p.ExpectedSenderURN, p.RecipientURN, p.RecipientProfileURL)
	if err != nil {
		return nil, err
	}
	if err := messageRecipient(member, member.URN, s.identity.URN); err != nil {
		return nil, err
	}
	return &PreparedMessage{write: &preparedWrite{session: s, ctx: ctx}, params: p, member: *member}, nil
}

func (p *PreparedMessage) Member() Member {
	if p == nil {
		return Member{}
	}
	return p.member
}

func (p *PreparedMessage) Send(ctx context.Context) (*MessageReceipt, error) {
	if p == nil {
		return nil, ErrInvalidParams
	}
	bound, done, err := p.write.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return p.write.session.client.sendDirectMessage(bound, p.params, p.member.URN, p.write.session.identity.URN, func() error { return p.write.check(bound) })
}

// PreparedComment binds the observed post, exact parent and text to one Send.
type PreparedComment struct {
	write  *preparedWrite
	params CreateCommentParams
	target CommentTarget
}

func (s *OutreachSession) PrepareComment(ctx context.Context, p CreateCommentParams) (*PreparedComment, error) {
	bound, done, err := s.bind(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := validateOutreachText(p.Text, CommentLimit); err != nil {
		return nil, err
	}
	if err := s.requireSender(p.ExpectedSenderURN); err != nil {
		return nil, err
	}
	target, err := s.client.ResolveCommentTarget(bound, p.PostURN, p.ParentCommentURN)
	if err != nil {
		return nil, err
	}
	if p.ActivityURN != "" && p.ActivityURN != target.ActivityURN {
		return nil, ErrInvalidParams
	}
	if err := s.check(bound); err != nil {
		return nil, err
	}
	return &PreparedComment{write: &preparedWrite{session: s, ctx: ctx}, params: p, target: *target}, nil
}

func (p *PreparedComment) Target() CommentTarget {
	if p == nil {
		return CommentTarget{}
	}
	return p.target
}

func (p *PreparedComment) Send(ctx context.Context) (*PostComment, error) {
	if p == nil {
		return nil, ErrInvalidParams
	}
	bound, done, err := p.write.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return p.write.session.client.sendComment(bound, p.params, &p.target, p.write.session.identity.URN, func() error { return p.write.check(bound) })
}

// ConversationMessageParams is one message into a conversation that already
// exists in the sender's own inbox, such as a reply to someone who wrote there
// without being a first-degree connection. RecipientURN is the conversation's
// other participant, as the host observed them write in it.
type ConversationMessageParams struct {
	ConversationURN   string
	RecipientURN      string
	ExpectedSenderURN string
	Body              string
}

// PreparedConversationMessage binds one conversation of the verified sender,
// its expected participant and an exact body to one Send.
type PreparedConversationMessage struct {
	write                   *preparedWrite
	params                  ConversationMessageParams
	conversation, recipient string
}

// PrepareConversationMessage checks the sender as PrepareMessage does and
// binds the conversation, which must be the sender's own (its viewer-owned
// msg_conversation URN). It reads nothing from LinkedIn and checks no
// connection degree: the host supplies a conversation it observed the
// recipient write in, and LinkedIn decides whether it accepts the message.
func (s *OutreachSession) PrepareConversationMessage(ctx context.Context, p ConversationMessageParams) (*PreparedConversationMessage, error) {
	bound, done, err := s.bind(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := validateOutreachText(p.Body, MessageLimit); err != nil {
		return nil, err
	}
	if err := s.requireSender(p.ExpectedSenderURN); err != nil {
		return nil, err
	}
	recipient, err := CanonicalMemberURN(p.RecipientURN)
	if err != nil || recipient == s.identity.URN {
		return nil, ErrInvalidParams
	}
	if !strings.HasPrefix(p.ConversationURN, "urn:li:msg_conversation:(") || !receiptURN(p.ConversationURN, "urn:li:msg_conversation:") || receiptMember(p.ConversationURN) != s.identity.URN {
		return nil, ErrInvalidParams
	}
	if err := s.check(bound); err != nil {
		return nil, err
	}
	conversation := "urn:li:msg_conversation:(" + s.identity.URN + "," + strings.Split(strings.TrimSuffix(strings.TrimPrefix(p.ConversationURN, "urn:li:msg_conversation:("), ")"), ",")[1] + ")"
	return &PreparedConversationMessage{write: &preparedWrite{session: s, ctx: ctx}, params: p, conversation: conversation, recipient: recipient}, nil
}

// Conversation returns the bound conversation in its viewer-owned form.
func (p *PreparedConversationMessage) Conversation() string {
	if p == nil {
		return ""
	}
	return p.conversation
}

// Send posts the body into the bound conversation once. The receipt is the
// created message and that same conversation; any other conversation, sender,
// recipient or text in the acknowledgment leaves delivery unknown.
func (p *PreparedConversationMessage) Send(ctx context.Context) (*MessageReceipt, error) {
	if p == nil {
		return nil, ErrInvalidParams
	}
	bound, done, err := p.write.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	return p.write.session.client.sendConversationMessage(bound, p.params.Body, p.conversation, p.recipient, p.write.session.identity.URN, func() error { return p.write.check(bound) })
}

func (c *Client) sendConversationMessage(ctx context.Context, text, conversation, recipient, sender string, guard func() error) (*MessageReceipt, error) {
	origin, tracking, err := messageToken()
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{
		"message":    map[string]any{"body": map[string]any{"attributes": []any{}, "text": text}, "renderContentUnions": []any{}, "conversationUrn": conversation, "originToken": origin},
		"mailboxUrn": sender, "trackingId": tracking, "dedupeByClientGeneratedToken": false,
	})
	body, _, err := c.makeGuardedWriteRequest(ctx, apiBase+"/voyagerMessagingDashMessengerMessages?action=createMessage", payload, guard)
	if err != nil {
		return nil, err
	}
	value, err := createdValue(body)
	if err != nil {
		return nil, unknownReceiptReason(writeUnknownCreationValue)
	}
	message, messageOK := selectMessagingReceipt(value, []string{"entityUrn", "backendUrn"}, sender, "urn:li:msg_message:", "urn:li:messagingMessage:")
	created, conversationOK := selectMessagingReceipt(value, []string{"conversationUrn", "backendConversationUrn", "*conversation"}, sender, "urn:li:msg_conversation:", "urn:li:messagingThread:")
	if !messageOK {
		return nil, unknownReceiptReason(writeUnknownMessageReceipt)
	}
	if !conversationOK {
		return nil, unknownReceiptReason(writeUnknownConversationReceipt)
	}
	if created != conversation {
		return nil, unknownReceiptReason(writeUnknownRequestMismatch)
	}
	if observed, exists := value["originToken"]; exists && observed != origin {
		return nil, unknownReceiptReason(writeUnknownRequestMismatch)
	}
	if detail := messageIdentityMismatch(value, sender, recipient); detail != "" {
		return nil, &WriteError{Cause: ErrWriteUnknown, unknownReason: writeUnknownIdentityMismatch, unknownDetail: detail}
	}
	if observed, exists := value["body"]; exists && valueText(observed) != text {
		return nil, unknownReceiptReason(writeUnknownRequestMismatch)
	}
	return &MessageReceipt{MessageURN: message, ConversationURN: created}, nil
}
