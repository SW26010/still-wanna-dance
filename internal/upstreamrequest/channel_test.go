package upstreamrequest

import (
	"testing"
)

func TestOwnershipAndChangeNotification(t *testing.T) {
	c := NewChannel()
	initial := c.Snapshot()
	first := c.Publish(&Pool{})
	select {
	case <-initial.Changed:
	default:
		t.Fatal("no publish notification")
	}
	tr := &Pool{}
	second := c.Publish(tr)
	c.Release(first)
	if c.Snapshot().Candidates != tr {
		t.Fatal("old owner released new channel")
	}
	current := c.Snapshot()
	c.Release(second)
	if c.Snapshot().Candidates != nil {
		t.Fatal("channel not released")
	}
	select {
	case <-current.Changed:
	default:
		t.Fatal("no release notification")
	}
}
