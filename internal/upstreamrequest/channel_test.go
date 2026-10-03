package upstreamrequest

import (
	"net/http"
	"testing"
)

func TestOwnershipAndChangeNotification(t *testing.T) {
	c := NewChannel()
	initial := c.Snapshot()
	first := c.Publish(&http.Transport{})
	select {
	case <-initial.Changed:
	default:
		t.Fatal("no publish notification")
	}
	tr := &http.Transport{}
	second := c.Publish(tr)
	c.Release(first)
	if c.Snapshot().Transport != tr {
		t.Fatal("old owner released new channel")
	}
	current := c.Snapshot()
	c.Release(second)
	if c.Snapshot().Transport != nil {
		t.Fatal("channel not released")
	}
	select {
	case <-current.Changed:
	default:
		t.Fatal("no release notification")
	}
}
