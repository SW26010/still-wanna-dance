package cacheproxy

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestExportRequestHistoryAllMethodsAndBoundaries(t *testing.T) {
	s, cfg := setup(t, nil)
	events := []usageEvent{{at: 999, method: "GET"}, {at: 1000, method: "HEAD", outcome: "canceled"}, {at: 2000, method: "GET"}}
	for i := 0; i < 601; i++ {
		events = append(events, usageEvent{at: 1500, method: "GET", outcome: "failed"})
	}
	if err := s.Store.usage.write(events); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	n, err := ExportRequestHistory(t.Context(), cfg.Store.path, 1000, 2000, &b)
	if err != nil || n != 602 {
		t.Fatal(n, err)
	}
	var first map[string]any
	if err := json.NewDecoder(&b).Decode(&first); err != nil {
		t.Fatal(err)
	}
	if first["method"] != "HEAD" || first["outcome"] != "canceled" || first["requested_at"] != float64(1000) {
		t.Fatal(first)
	}
}
