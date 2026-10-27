package initialpriority

import (
	"encoding/json"
	"math"
	"testing"
)

// Guard the bundled model contract, not any particular song's mutable rank.
func TestBundledPriorUsesEqualRequesterBudgets(t *testing.T) {
	var data struct {
		Model                   string  `json:"model"`
		HalfLifeDays            float64 `json:"halfLifeDays"`
		ActiveDaysForFullWeight int     `json:"activeDaysForFullWeight"`
		UnknownPoolWeight       float64 `json:"unknownPoolWeight"`
	}
	if err := json.Unmarshal(encoded, &data); err != nil {
		t.Fatal(err)
	}
	if data.Model != "equalRequester" || data.HalfLifeDays != 180 || data.ActiveDaysForFullWeight != 5 || data.UnknownPoolWeight != 0.25 {
		t.Fatalf("bundled prior lost requester-budget contract: %+v", data)
	}
	if len(scores) == 0 {
		t.Fatal("empty initial prior")
	}
	for id, score := range scores {
		if id <= 0 || math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 0.5 {
			t.Fatalf("invalid prior: id=%d score=%g", id, score)
		}
	}
}
