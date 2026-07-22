package slack

import (
	"context"
	"errors"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"
	"go.uber.org/zap"
)

type fakeWriteAPI struct {
	calls int
	ts    string
	err   error
}

func (f *fakeWriteAPI) PostMessageContext(_ context.Context, _ string, _ ...slackapi.MsgOption) (channelID, timestamp string, err error) {
	f.calls++
	if f.err != nil {
		return "", "", f.err
	}
	if f.ts == "" {
		f.ts = "1700000000.000100"
	}
	return "C1", f.ts, nil
}

func newWriteChannel(t *testing.T) (*Channel, *fakeWriteAPI) {
	t.Helper()
	api := &fakeWriteAPI{}
	c := &Channel{logger: zap.NewNop(), postAPI: api}
	return c, api
}

// --- canWrite: fail-closed table ---

func TestCanWrite_FailClosed(t *testing.T) {
	tests := []struct {
		name     string
		channels []string
		mode     string
		query    string
		wantOK   bool
	}{
		{"unset mode (legacy) is off", []string{"C1"}, "", "C1", false},
		{"garbage mode is off", []string{"C1"}, "wat", "C1", false},
		{"mode off", []string{"C1"}, "off", "C1", false},
		{"draft, allowed", []string{"C1"}, "draft", "C1", true},
		{"auto, allowed", []string{"C1"}, "auto", "C1", true},
		{"draft, not in list", []string{"C1"}, "draft", "C2", false},
		{"empty channel id", []string{"C1"}, "draft", "", false},
		{"case sensitive (no fold)", []string{"C1"}, "draft", "c1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newWriteChannel(t)
			c.SetWriteConfig(tt.channels, tt.mode, 0, [2]int{})
			_, ok := c.canWrite(tt.query)
			if ok != tt.wantOK {
				t.Errorf("canWrite(%q) ok=%v, want %v", tt.query, ok, tt.wantOK)
			}
		})
	}
}

func TestPostToChannel_DeniedWhenNotWritable(t *testing.T) {
	c, api := newWriteChannel(t)
	c.SetWriteConfig([]string{"C1"}, "draft", 0, [2]int{})
	if _, err := c.PostToChannel(context.Background(), "C2", "hi"); !errors.Is(err, ErrWriteDenied) {
		t.Fatalf("expected ErrWriteDenied, got %v", err)
	}
	if api.calls != 0 {
		t.Error("must not call PostMessage when denied")
	}
}

func TestPostToChannel_HappyPath(t *testing.T) {
	c, api := newWriteChannel(t)
	c.SetWriteConfig([]string{"C1"}, "draft", 0, [2]int{})
	ts, err := c.PostToChannel(context.Background(), "C1", "deploy is green")
	if err != nil {
		t.Fatalf("PostToChannel: %v", err)
	}
	if ts == "" || api.calls != 1 {
		t.Errorf("expected one post with a ts, calls=%d ts=%q", api.calls, ts)
	}
}

func TestPostToChannel_SecretBlocked(t *testing.T) {
	c, api := newWriteChannel(t)
	c.SetWriteConfig([]string{"C1"}, "draft", 0, [2]int{})
	_, err := c.PostToChannel(context.Background(), "C1", "the token xoxb-abcdefghij-secretpart is here") // gitleaks:allow (test fixture)
	if !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("expected ErrSecretDetected, got %v", err)
	}
	if api.calls != 0 {
		t.Error("must not post text with a suspected secret")
	}
}

func TestPostToChannel_HourlyBudget(t *testing.T) {
	c, api := newWriteChannel(t)
	c.SetWriteConfig([]string{"C1"}, "auto", 2, [2]int{}) // 2 posts/hour
	for i := 0; i < 2; i++ {
		if _, err := c.PostToChannel(context.Background(), "C1", "ok"); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}
	if _, err := c.PostToChannel(context.Background(), "C1", "ok"); !errors.Is(err, ErrGuardrailBlocked) {
		t.Fatalf("3rd post should be budget-blocked, got %v", err)
	}
	if api.calls != 2 {
		t.Errorf("expected exactly 2 sent, got %d", api.calls)
	}
}

func TestPostToChannel_DraftIgnoresBudget(t *testing.T) {
	c, api := newWriteChannel(t)
	c.SetWriteConfig([]string{"C1"}, "draft", 1, [2]int{}) // 1/hour cap
	// Draft posts (owner-approved) are not budget-limited; both should send.
	for i := 0; i < 3; i++ {
		if _, err := c.PostToChannel(context.Background(), "C1", "approved"); err != nil {
			t.Fatalf("draft post %d should not be budget-blocked: %v", i, err)
		}
	}
	if api.calls != 3 {
		t.Errorf("expected 3 draft posts, got %d", api.calls)
	}
}

func TestPostToChannel_QuietHoursAutoOnly(t *testing.T) {
	c, _ := newWriteChannel(t)
	// A wide quiet window that includes "now" in server-local time.
	nowH := time.Now().Hour()
	c.SetWriteConfig([]string{"C1"}, "auto", 0, [2]int{nowH, (nowH + 2) % 24})
	if !c.inQuietHours(time.Now()) {
		t.Skip("could not construct a quiet window covering now")
	}
	if _, err := c.PostToChannel(context.Background(), "C1", "hi"); !errors.Is(err, ErrGuardrailBlocked) {
		t.Fatalf("auto post in quiet hours should be blocked, got %v", err)
	}
	// Draft mode ignores quiet hours (owner just approved it).
	c.SetWriteConfig([]string{"C1"}, "draft", 0, [2]int{nowH, (nowH + 2) % 24})
	if _, err := c.PostToChannel(context.Background(), "C1", "hi"); err != nil {
		t.Fatalf("draft post should ignore quiet hours, got %v", err)
	}
}

func TestInQuietHours(t *testing.T) {
	c, _ := newWriteChannel(t)
	// Not configured → never quiet.
	c.SetWriteConfig([]string{"C1"}, "auto", 0, [2]int{0, 0})
	if c.inQuietHours(time.Date(2024, 1, 1, 3, 0, 0, 0, time.Local)) {
		t.Error("(0,0) must mean not configured")
	}
	// Wrap-around window 22..8.
	c.SetWriteConfig([]string{"C1"}, "auto", 0, [2]int{22, 8})
	for _, h := range []int{22, 23, 0, 7} {
		if !c.inQuietHours(time.Date(2024, 1, 1, h, 0, 0, 0, time.Local)) {
			t.Errorf("hour %d should be quiet", h)
		}
	}
	for _, h := range []int{8, 12, 21} {
		if c.inQuietHours(time.Date(2024, 1, 1, h, 0, 0, 0, time.Local)) {
			t.Errorf("hour %d should NOT be quiet", h)
		}
	}
}
