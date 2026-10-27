package cacheproxy

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRequestHistoryChangesFollowCommitAndPruning(t *testing.T) {
	store, err := CreateStore(filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	u := store.usage
	u.unlimited = true
	revision, changed, closed := store.RequestHistoryChanges()
	if revision != 0 || closed || u.started || store.engine.Load() != nil {
		t.Fatal("observation started work or returned an invalid initial state")
	}
	_, other, _ := store.RequestHistoryChanges()
	assertQuiet := func() {
		t.Helper()
		select {
		case <-changed:
			t.Fatal("notified without a committed history change")
		default:
		}
	}
	get := usageEvent{method: "GET", at: 1000, status: 200}
	// A late validation failure rolls back the preceding valid insertion.
	if err := u.write([]usageEvent{get, {songID: "invalid"}}); err == nil {
		t.Fatal("invalid write succeeded")
	}
	assertQuiet()
	if err := u.write([]usageEvent{{summaryOnly: true, songID: "42", at: 1000}, {method: "HEAD", at: 1000}}); err != nil {
		t.Fatal(err)
	}
	assertQuiet()
	if err := u.write([]usageEvent{get, get}); err != nil {
		t.Fatal(err)
	}
	for _, wake := range []<-chan struct{}{changed, other} {
		select {
		case <-wake:
		default:
			t.Fatal("committed batch did not wake all readers")
		}
	}
	page, err := ReadRequestHistory(t.Context(), store.path, RequestQuery{Limit: 50})
	if err != nil || len(page.Requests) != 2 {
		t.Fatal("notification preceded commit", page, err)
	}
	revision, changed, closed = store.RequestHistoryChanges()
	if revision != 1 || closed {
		t.Fatal("batch was not coalesced", revision, closed)
	}
	u.unlimited = false // No retention: no new history, but old details are pruned.
	if err := u.write([]usageEvent{get}); err != nil {
		t.Fatal(err)
	}
	assertQuiet()
	if deleted, err := u.prune(time.Now()); err != nil || deleted != 3 {
		t.Fatal(deleted, err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("pruning did not wake readers")
	}
	revision, changed, _ = store.RequestHistoryChanges()
	if revision != 2 {
		t.Fatal(revision)
	}
	if deleted, err := u.prune(time.Now()); err != nil || deleted != 0 {
		t.Fatal(deleted, err)
	}
	assertQuiet()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("closing the store left a reader waiting")
	}
	if _, _, closed := store.RequestHistoryChanges(); !closed {
		t.Fatal("closed store still advertises updates")
	}
}
