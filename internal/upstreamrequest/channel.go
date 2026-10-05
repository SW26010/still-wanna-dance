// Package upstreamrequest is the application-owned upstream request channel.
// Network configuration is published here by infrastructure, not by consumers.
package upstreamrequest

import (
	"errors"
	"net/http"
	"sync"
)

var ErrUnavailable = errors.New("upstream request channel is not ready")
var Default = NewChannel()

type Snapshot struct {
	Transport  http.RoundTripper
	Revision   uint64
	Changed    <-chan struct{}
	Candidates Candidates
}

type Channel struct {
	resourceDomains map[string]resourceObservation
	resources       resourceActivity
	mu              sync.Mutex
	transport       http.RoundTripper
	revision        uint64
	changed         chan struct{}
}

func NewChannel() *Channel { return &Channel{changed: make(chan struct{})} }
func (c *Channel) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, _ := c.transport.(Candidates)
	return Snapshot{Transport: c.transport, Revision: c.revision, Changed: c.changed, Candidates: p}
}

// Publish never owns or closes the transport. Revision is a release token so
// shutting down an old application owner cannot clear a newer owner's channel.
func (c *Channel) Publish(t http.RoundTripper) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.replace(t)
	return c.revision
}
func (c *Channel) Release(revision uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.revision == revision {
		c.replace(nil)
	}
}
func (c *Channel) replace(t http.RoundTripper) {
	c.resourceDomains = nil
	close(c.changed)
	c.changed = make(chan struct{})
	c.revision++
	c.transport = t
}
