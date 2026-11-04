// Package faults coalesces actionable failures independently of logging and UI.
package faults

import (
	"errors"
	"sync"
)

// Center tracks one incident per source until that source reports recovery.
// Producers never wait for the desktop or browser.
type Center struct {
	mu      sync.Mutex
	active  map[string]error
	pending map[string]error
	retries map[string]int
	open    bool
	wake    chan struct{}
}

func New() *Center {
	return &Center{active: make(map[string]error), pending: make(map[string]error), retries: make(map[string]int), wake: make(chan struct{}, 1)}
}

// Set publishes a final failure or clears the source after recovery. Repeated
// reports for an unresolved incident do not open another browser tab.
func (c *Center) Set(source string, err error) {
	if c == nil || source == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(source, err)
}

// Retry promotes a failure only after three consecutive failed attempts.
// Recovery clears both the pending retry count and any active incident.
func (c *Center) Retry(source string, err error) {
	if c == nil || source == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		delete(c.retries, source)
		c.setLocked(source, nil)
		return
	}
	if c.retries[source] < 3 {
		c.retries[source]++
	}
	if c.retries[source] >= 3 {
		c.setLocked(source, err)
	}
}

func (c *Center) setLocked(source string, err error) {
	if err == nil {
		delete(c.active, source)
		delete(c.pending, source)
		delete(c.retries, source)
		return
	}
	if _, exists := c.active[source]; exists {
		return
	}
	// A retained failure can also surface through an action which depends on
	// it. Keep both sources active, but treat the shared cause as one incident.
	for other, current := range c.active {
		if errors.Is(err, current) || errors.Is(current, err) {
			c.active[source] = err
			if _, awaiting := c.pending[other]; awaiting {
				c.pending[source] = err
			}
			return
		}
	}
	c.active[source] = err
	c.pending[source] = err
	c.signalLocked()
}

// Open requests the usual startup opening through the same coalescing path.
func (c *Center) Open() {
	c.mu.Lock()
	c.open = true
	c.signalLocked()
	c.mu.Unlock()
}

func (c *Center) signalLocked() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *Center) Wake() <-chan struct{} { return c.wake }

// Take combines failures that arose together into one opening. A recovered
// source is removed before delivery; unrelated pending sources are retained.
func (c *Center) Take() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	open := c.open || len(c.pending) != 0
	var problems []error
	for source, err := range c.pending {
		problems = append(problems, err)
		delete(c.pending, source)
	}
	c.open = false
	return open, errors.Join(problems...)
}

// Restore retains unresolved causes when an opening was interrupted by
// shutdown, so the next available WebUI can present the failure instead.
func (c *Center) Restore(cause error) {
	if cause == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for source, err := range c.active {
		if errors.Is(cause, err) || errors.Is(err, cause) {
			c.pending[source] = err
		}
	}
	if len(c.pending) != 0 {
		c.signalLocked()
	}
}
