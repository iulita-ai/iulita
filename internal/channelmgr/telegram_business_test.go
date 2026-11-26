package channelmgr

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/iulita-ai/iulita/internal/channel/telegram"
	business "github.com/iulita-ai/iulita/internal/telegrambusiness"
	"go.uber.org/zap"
)

type businessRuntimeTransport func(*http.Request) (*http.Response, error)

// RoundTrip supplies only synthetic bot identity; no Telegram connection occurs.
func (f businessRuntimeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type businessRuntimePoll struct{ business.PollRepository }

func businessRuntimeChannel(t *testing.T) *telegram.Channel {
	t.Helper()
	client := &http.Client{Transport: businessRuntimeTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/getMe") {
			t.Fatalf("unexpected runtime fixture RPC %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":7,"is_bot":true,"first_name":"Fixture","username":"fixture_bot","can_connect_to_business":true}}`))}, nil
	})}
	channel, err := telegram.New("7:fixture", nil, nil, 0, client, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	channel.SetInstanceID("instance")
	channel.SetBusinessService(business.New(nil, nil, nil, business.Options{}))
	channel.SetBusinessPollRepository(businessRuntimePoll{})
	return channel
}

func TestBusinessGenerationDeniesStoppedCancelledAndReplacedChannels(t *testing.T) {
	m := newTestManager(t)
	scope := business.Scope{Key: business.Key{InstanceID: "instance", BotID: 7, ConnectionID: "connection"}, OwnerID: "owner", PeerID: 99}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	close(done)
	m.running["instance"] = &ManagedChannel{tg: businessRuntimeChannel(t), runtimeContext: ctx, cancel: cancel, done: done}
	first := m.BusinessGeneration(scope)
	if first == "" {
		t.Fatal("live exact transport unavailable")
	}
	foreign := scope
	foreign.Key.BotID++
	if m.BusinessGeneration(foreign) != "" {
		t.Fatal("replacement bot admitted old scope")
	}
	cancel()
	if m.BusinessGeneration(scope) != "" {
		t.Fatal("cancelled runtime remained available before goroutine exit")
	}
	newCtx, stop := context.WithCancel(context.Background())
	defer stop()
	m.running["instance"] = &ManagedChannel{tg: businessRuntimeChannel(t), runtimeContext: newCtx, cancel: stop, done: done}
	if next := m.BusinessGeneration(scope); next == "" || next == first {
		t.Fatal("replacement reused old delivery authority")
	}
	m.StopInstance("instance")
	if m.BusinessGeneration(scope) != "" {
		t.Fatal("stopped runtime still authorizes model admission")
	}
}
