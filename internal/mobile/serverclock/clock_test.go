package serverclock

import (
	"testing"
	"time"
)

func TestEpochRotatesOnClockStepNotElapsedTime(t *testing.T) {
	wall := time.Unix(1700000000, 0)
	var mono time.Duration
	c := newClock(func() time.Time { return wall }, func() time.Duration { return mono })
	initial := c.ID()
	for range 3 {
		wall = wall.Add(time.Hour)
		mono += time.Hour
		if c.ID() != initial {
			t.Fatal("normal elapsed time rotated the clock")
		}
	}
	wall = wall.Add(-time.Hour)
	afterRollback := c.ID()
	if afterRollback == initial {
		t.Fatal("wall rollback retained old authority epoch")
	}
	wall = wall.Add(time.Hour)
	if c.ID() == afterRollback {
		t.Fatal("wall forward step retained old epoch")
	}
}
