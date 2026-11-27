package upstreamrequest

import "time"

// CandidateView reads each exact target once during a synchronous operation.
// Discard it after the read so later operations see withdrawals and new leases.
type CandidateView struct {
	provider Candidates
	targets  map[string]candidateSet
}

type candidateSet struct {
	ordered []Candidate
	byID    map[string]int
}

func NewCandidateView(provider Candidates) *CandidateView {
	return &CandidateView{provider: provider, targets: make(map[string]candidateSet)}
}

func (v *CandidateView) current(target string) candidateSet {
	set, ok := v.targets[target]
	if !ok && v.provider != nil {
		set.ordered = v.provider.Current(target)
		set.byID = make(map[string]int, len(set.ordered))
		for i, c := range set.ordered {
			set.byID[c.ID] = i
		}
		v.targets[target] = set
	}
	return set
}

func (v *CandidateView) Current(target string) []Candidate {
	return v.current(target).ordered
}

func (v *CandidateView) Find(target, id string) (Candidate, bool) {
	set := v.current(target)
	if i, ok := set.byID[id]; ok {
		return set.ordered[i], true
	}
	return Candidate{}, false
}

func (v *CandidateView) SourceActive(s ResourceSource, now time.Time) bool {
	if !now.Before(s.ValidUntil) {
		return false
	}
	if s.APIChannelID == "" {
		return true
	}
	c, ok := v.Find(s.API, s.APIChannelID)
	return ok && (c.ValidUntil.IsZero() || now.Before(c.ValidUntil))
}
