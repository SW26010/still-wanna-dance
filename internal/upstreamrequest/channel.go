// Package upstreamrequest is the application-owned upstream request channel.
// Network configuration is published here by infrastructure, not by consumers.
package upstreamrequest

import (
	"errors"
	"sync"
)

var ErrUnavailable = errors.New("upstream request channel is not ready")
var Default = NewChannel()

type Snapshot struct {
	Revision   uint64
	Changed    <-chan struct{}
	Candidates Candidates
}

type Channel struct {
	resourceDomains map[string]resourceObservation
	resources       resourceActivity
	mu              sync.Mutex
	candidates      Candidates
	revision        uint64
	changed         chan struct{}
}

func NewChannel() *Channel { return &Channel{changed: make(chan struct{})} }
func (c *Channel) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Snapshot{Revision: c.revision, Changed: c.changed, Candidates: c.candidates}
}

// Publish never owns or closes the candidates. Revision is a release token so
// shutting down an old application owner cannot clear a newer owner's channel.
func (c *Channel) Publish(p Candidates) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.replace(p)
	return c.revision
}
func (c *Channel) Release(revision uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revision == revision {
		c.replace(nil)
	}
}
func (c *Channel) replace(p Candidates) {
	c.resourceDomains = nil
	close(c.changed)
	c.changed = make(chan struct{})
	c.revision++
	c.candidates = p
}
