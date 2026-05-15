package slack

import (
	"testing"
)

func TestComposeChatID_DM(t *testing.T) {
	c := &Channel{}
	chatID := c.composeChatID("D1234567890", "U9876543210")
	expected := "slack:D1234567890"
	if chatID != expected {
		t.Errorf("DM chatID = %q, want %q", chatID, expected)
	}
}

func TestComposeChatID_PublicChannel(t *testing.T) {
	c := &Channel{}
	chatID := c.composeChatID("C1234567890", "U9876543210")
	expected := "slack:C1234567890:U9876543210"
	if chatID != expected {
		t.Errorf("public channel chatID = %q, want %q", chatID, expected)
	}
}

func TestChatMeta_StoreAndGet(t *testing.T) {
	c := &Channel{
		chatMetaM: make(map[string]*chatMeta),
	}

	meta := &chatMeta{channelID: "C123", threadTS: "1234.5678", userID: "U456"}
	c.storeChatMeta("slack:C123:U456", meta)

	got := c.getChatMeta("slack:C123:U456")
	if got == nil {
		t.Fatal("expected non-nil chatMeta")
	}
	if got.channelID != "C123" {
		t.Errorf("channelID = %q, want C123", got.channelID)
	}
	if got.threadTS != "1234.5678" {
		t.Errorf("threadTS = %q, want 1234.5678", got.threadTS)
	}
}

func TestChatMeta_GetMissing(t *testing.T) {
	c := &Channel{
		chatMetaM: make(map[string]*chatMeta),
	}
	if got := c.getChatMeta("nonexistent"); got != nil {
		t.Error("expected nil for missing chatID")
	}
}

func TestInboundTS_StoredOnMeta(t *testing.T) {
	c := &Channel{
		chatMetaM: make(map[string]*chatMeta),
	}
	c.storeChatMeta("chat1", &chatMeta{channelID: "C123", userID: "U456", inboundTS: "1234.5678"})
	got := c.getChatMeta("chat1")
	if got == nil || got.inboundTS != "1234.5678" {
		t.Errorf("inboundTS = %q, want 1234.5678", got.inboundTS)
	}
}

func TestGenerateNonce(t *testing.T) {
	n1 := generateNonce()
	n2 := generateNonce()

	if len(n1) != 12 {
		t.Errorf("nonce length = %d, want 12 (6 bytes hex)", len(n1))
	}
	if n1 == n2 {
		t.Error("consecutive nonces should be different")
	}
}

func TestSplitActionID(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{"prompt:abc:opt1", []string{"prompt", "abc", "opt1"}},
		{"remember:xyz", []string{"remember", "xyz"}},
		{"single", []string{"single"}},
	}

	for _, tt := range tests {
		got := splitActionID(tt.input)
		if len(got) != len(tt.expected) {
			t.Errorf("splitActionID(%q): got %v, want %v", tt.input, got, tt.expected)
			continue
		}
		for i := range got {
			if got[i] != tt.expected[i] {
				t.Errorf("splitActionID(%q)[%d]: got %q, want %q", tt.input, i, got[i], tt.expected[i])
			}
		}
	}
}
