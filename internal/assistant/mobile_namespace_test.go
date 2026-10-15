package assistant

import (
	"context"
	"errors"
	"testing"

	"github.com/iulita-ai/iulita/internal/channel"
	"github.com/iulita-ai/iulita/internal/domain"
)

func TestMobileCannotEnterLegacyAssistant(t *testing.T) {
	// No provider/store: any work before the namespace guard would panic.
	a := &Assistant{}
	for _, id := range []string{"mobile:conversation:x", "MOBILE:invalid"} {
		reply, err := a.HandleMessage(context.Background(), channel.IncomingMessage{ChatID: id, Text: "private"})
		if reply != "" || !errors.Is(err, domain.ErrMobileNamespace) {
			t.Fatalf("%q %v", reply, err)
		}
	}
}
