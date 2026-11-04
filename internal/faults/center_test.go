package faults

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestIncidentCoalescingAndRecovery(t *testing.T) {
	c := New()
	first, second := errors.New("first"), errors.New("second")
	c.Open()
	c.Set("storage", first)
	c.Set("storage", errors.New("same incident"))
	c.Set("cache", second)
	<-c.Wake()
	if open, err := c.Take(); !open || !errors.Is(err, first) || !errors.Is(err, second) {
		t.Fatalf("startup and faults were not coalesced: %v %v", open, err)
	}
	c.Set("storage", first)
	select {
	case <-c.Wake():
		t.Fatal("an unresolved incident opened twice")
	default:
	}
	c.Set("storage", nil)
	c.Set("storage", first)
	<-c.Wake()
	if open, err := c.Take(); !open || !errors.Is(err, first) {
		t.Fatalf("a new incident was not delivered: %v %v", open, err)
	}
	c.Set("temporary", errors.New("temporary"))
	c.Set("temporary", nil)
	<-c.Wake()
	if open, err := c.Take(); open || err != nil {
		t.Fatalf("a recovered fault remained pending: %v %v", open, err)
	}
}

func TestRetryOnlyReportsPersistentFailures(t *testing.T) {
	c := New()
	problem := errors.New("unavailable")
	c.Retry("queue", problem)
	c.Retry("queue", problem)
	select {
	case <-c.Wake():
		t.Fatal("a transient failure opened the WebUI")
	default:
	}
	c.Retry("queue", nil)
	for range 3 {
		c.Retry("queue", problem)
	}
	<-c.Wake()
	if open, err := c.Take(); !open || !errors.Is(err, problem) {
		t.Fatal("persistent failure was not reported", open, err)
	}
	c.Retry("queue", problem)
	select {
	case <-c.Wake():
		t.Fatal("same persistent failure was reported twice")
	default:
	}
	c.Retry("queue", nil)
	for range 3 {
		c.Retry("queue", problem)
	}
	<-c.Wake()
	if open, err := c.Take(); !open || !errors.Is(err, problem) {
		t.Fatal("new persistent incident was not reported", open, err)
	}
}

func TestConcurrentReportsShareOneIncident(t *testing.T) {
	c := New()
	problem := errors.New("unavailable")
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() { defer workers.Done(); c.Set("storage", problem) }()
	}
	workers.Wait()
	<-c.Wake()
	if open, err := c.Take(); !open || !errors.Is(err, problem) {
		t.Fatal("concurrent reports lost the incident", open, err)
	}
	select {
	case <-c.Wake():
		t.Fatal("concurrent reports emitted duplicate openings")
	default:
	}
}

func TestSharedCauseAcrossSourcesIsOneIncident(t *testing.T) {
	c := New()
	root := errors.New("storage unavailable")
	c.Set("storage", root)
	c.Set("action", fmt.Errorf("acceleration failed: %w", root))
	// Clearing the original source must not discard the pending dependent fault.
	c.Set("storage", nil)
	<-c.Wake()
	if open, err := c.Take(); !open || !errors.Is(err, root) {
		t.Fatal("shared fault was lost", open, err)
	}
	c.Set("storage", root)
	select {
	case <-c.Wake():
		t.Fatal("dependent failure opened twice")
	default:
	}
	c.Set("storage", nil)
	c.Set("action", nil)
	c.Set("storage", root)
	<-c.Wake()
	if open, err := c.Take(); !open || !errors.Is(err, root) {
		t.Fatal("new incident after full recovery was not delivered", open, err)
	}
}
