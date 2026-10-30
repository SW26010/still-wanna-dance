package cacheproxy

import (
	"slices"
	"strings"
	"testing"
)

func TestHistoryHidesOnlyAdjacentCanceledRequests(t *testing.T) {
	key := strings.Repeat("a", 32)
	for _, tc := range []struct {
		name   string
		change func(first, next *usageEvent)
		middle *usageEvent
		hide   bool
	}{
		{name: "within second", hide: true},
		{name: "same millisecond", change: func(a, b *usageEvent) { b.at = a.at }, hide: true},
		{name: "exact second", change: func(a, b *usageEvent) { b.at = a.at + 1000 }, hide: true},
		{name: "outside second", change: func(a, b *usageEvent) { b.at = a.at + 1001 }},
		{name: "completed before canceled", change: func(a, b *usageEvent) { b.at = a.at - 1 }},
		{name: "unknown first resource", change: func(a, b *usageEvent) { a.key = "" }},
		{name: "unknown next resource", change: func(a, b *usageEvent) { b.key = "" }},
		{name: "different resource", change: func(a, b *usageEvent) { b.key = strings.Repeat("b", 32) }},
		{name: "song conflict", change: func(a, b *usageEvent) { b.songID = "43" }},
		{name: "unknown first song", change: func(a, b *usageEvent) { a.songID = "" }, hide: true},
		{name: "unknown next song", change: func(a, b *usageEvent) { b.songID = "" }, hide: true},
		{name: "aborted", change: func(a, b *usageEvent) { a.outcome = "aborted" }},
		{name: "failed", change: func(a, b *usageEvent) { a.outcome = "failed" }},
		{name: "incomplete", change: func(a, b *usageEvent) { a.outcome = "incomplete" }},
		{name: "next canceled", change: func(a, b *usageEvent) { b.outcome = "canceled" }},
		{name: "next failed", change: func(a, b *usageEvent) { b.outcome, b.status = "failed", 502 }},
		{name: "non-video completion", change: func(a, b *usageEvent) { b.status = 302 }},
		{name: "intervening HEAD", middle: &usageEvent{at: 10200, method: "HEAD"}, hide: true},
		{name: "intervening cancellation", middle: &usageEvent{at: 10200, method: "GET", key: key, outcome: "canceled"}},
		{name: "intervening failure", middle: &usageEvent{at: 10200, method: "GET", key: key, outcome: "failed"}},
		{name: "intervening other resource", middle: &usageEvent{at: 10200, method: "GET", key: strings.Repeat("b", 32), outcome: "completed", status: 200}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, cfg := setup(t, nil)
			first := usageEvent{at: 10000, completedAt: 10010, method: "GET", key: key, songID: "42", outcome: "canceled", status: 200}
			next := usageEvent{at: 10999, completedAt: 20000, method: "GET", key: key, songID: "42", outcome: "completed", status: 206}
			if tc.change != nil {
				tc.change(&first, &next)
			}
			if err := s.usage.write([]usageEvent{first}); err != nil {
				t.Fatal(err)
			}
			q := RequestQuery{Limit: 50, HideAdjacentCanceled: true}
			before, err := ReadRequestHistory(t.Context(), cfg.Store.path, q)
			if err != nil || len(before.Requests) != 1 {
				t.Fatalf("cancellation without completion was hidden: %+v %v", before, err)
			}
			firstID := before.Requests[0].ID
			events := []usageEvent{next}
			if tc.middle != nil {
				events = append([]usageEvent{*tc.middle}, events...)
			}
			if err := s.usage.write(events); err != nil {
				t.Fatal(err)
			}
			after, err := ReadRequestHistory(t.Context(), cfg.Store.path, q)
			if err != nil {
				t.Fatal(err)
			}
			visible := slices.ContainsFunc(after.Requests, func(r RecentRequest) bool { return r.ID == firstID })
			if visible == tc.hide {
				t.Fatalf("first request visible=%v, want hidden=%v: %+v", visible, tc.hide, after)
			}
			q.HideAdjacentCanceled = false
			all, err := ReadRequestHistory(t.Context(), cfg.Store.path, q)
			want := 2
			if tc.middle != nil && tc.middle.method == "GET" {
				want++
			}
			if err != nil || len(all.Requests) != want {
				t.Fatalf("original records changed: %+v %v", all, err)
			}
		})
	}
}

func TestHistoryFilteredPaginationAndRange(t *testing.T) {
	s, cfg := setup(t, nil)
	var events []usageEvent
	for i := int64(1); i <= 6; i++ {
		key, song := strings.Repeat("a", 32), "42"
		if i%2 == 0 {
			key, song = strings.Repeat("b", 32), "43"
		}
		events = append(events,
			usageEvent{at: i * 10000, method: "GET", key: key, songID: song, outcome: "canceled", status: 200},
			usageEvent{at: i*10000 + 1000, method: "GET", key: key, songID: song, outcome: "completed", status: 206})
	}
	if err := s.usage.write(events); err != nil {
		t.Fatal(err)
	}
	for _, sort := range []string{"time", "song", "resource"} {
		for _, direction := range []string{"ascending", "descending"} {
			q := RequestQuery{Limit: 50, Sort: sort, Direction: direction}
			all, err := ReadRequestHistory(t.Context(), cfg.Store.path, q)
			if err != nil {
				t.Fatal(err)
			}
			var want []int64
			for _, r := range all.Requests {
				if r.Outcome == "completed" {
					want = append(want, r.ID)
				}
			}
			for _, limit := range []int{1, 4} {
				q.Limit, q.HideAdjacentCanceled, q.Cursor = limit, true, ""
				var got []int64
				for {
					page, err := ReadRequestHistory(t.Context(), cfg.Store.path, q)
					if err != nil || page.HasMore && len(page.Requests) != limit {
						t.Fatalf("filtered page: %+v %v", page, err)
					}
					for _, r := range page.Requests {
						got = append(got, r.ID)
					}
					if !page.HasMore {
						break
					}
					if page.NextCursor == "" || page.NextCursor == q.Cursor || len(got) > len(want) {
						t.Fatal("pagination did not advance", page)
					}
					q.Cursor = page.NextCursor
				}
				if !slices.Equal(got, want) {
					t.Fatalf("%s %s limit %d: got %v, want %v", sort, direction, limit, got, want)
				}
			}
		}
	}
	// The successor is outside the selected range, but the display rule is
	// still evaluated against the retained chronological history.
	page, err := ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{From: 10000, To: 11000, Limit: 50, HideAdjacentCanceled: true})
	if err != nil || len(page.Requests) != 0 || page.HasMore {
		t.Fatalf("range changed adjacency: %+v %v", page, err)
	}
}
