package linkedin

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestActivityMessageConversationBacklink(t *testing.T) {
	body, err := os.ReadFile("testdata/activity_conversation_backlink.json")
	if err != nil {
		t.Fatal(err)
	}
	page, err := activityFixtureClient(t, string(body)).GetActivityConversations(context.Background(), "urn:li:fsd_profile:SELF")
	if err != nil || len(page.Conversations) != 1 || len(page.Conversations[0].Messages) != 1 {
		t.Fatalf("backlink loop prevented valid mailbox: %+v %v", page, err)
	}
	if page.Conversations[0].Messages[0].Kind != "human" {
		t.Fatal("sender actor was misclassified as a system event")
	}
	source := string(body)
	for _, broken := range []string{
		strings.Replace(source, `"*conversation": "urn:li:msg_conversation:fixture1"`, `"*conversation": "urn:li:msg_conversation:missing"`, 1),
		strings.Replace(source, `"*conversation":`, `"conversation":{"entityUrn":"urn:li:msg_conversation:other"},"*conversation":`, 1),
	} {
		if broken == source {
			t.Fatal("fixture did not exercise its backlink")
		}
		if _, err := activityFixtureClient(t, broken).GetActivityConversations(context.Background(), "urn:li:fsd_profile:SELF"); !errors.Is(err, ErrParseFailed) {
			t.Fatalf("invalid backlink accepted: %v", err)
		}
	}
}

func TestActivityRejectsWrongConversationMessage(t *testing.T) {
	raw := rawActivityMessage{URN: "urn:li:msg_message:fixture", DeliveredAt: 1700000000000}
	raw.Thread.URN = "urn:li:msg_conversation:other"
	if _, err := raw.parse("urn:li:msg_conversation:expected"); !errors.Is(err, ErrParseFailed) {
		t.Fatal("cross-conversation message accepted")
	}
}
