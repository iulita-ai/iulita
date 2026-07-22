package skill

import (
	"sync"
	"testing"
)

// TestRegistry_GetCapabilityRace exercises Get concurrently with runtime
// AddCapability/RemoveCapability, as the Slack OAuth connect/disconnect handlers
// now do from the HTTP goroutine while the assistant tool-loop calls Get. Run
// with -race: before Get held the RLock across hasCapabilities, this was a fatal
// concurrent map read/write.
func TestRegistry_GetCapabilityRace(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockCapSkill{mockSkill: mockSkill{name: "slack_search"}, caps: []string{"slack_user"}})

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					r.Get("slack_search")
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 3000; i++ {
			if i%2 == 0 {
				r.AddCapability("slack_user")
			} else {
				r.RemoveCapability("slack_user")
			}
		}
		close(stop)
	}()
	wg.Wait()
}
