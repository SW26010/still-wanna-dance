package blocksched

import "time"

// RollingDeadline keeps an estimated origin until it is reached, then moves
// with the clock. All offsets from one origin move together.
type RollingDeadline struct {
	Base   time.Time
	Offset time.Duration
}

func (d RollingDeadline) At(now time.Time) time.Time {
	if d.Base.IsZero() {
		return time.Time{}
	}
	if now.Before(d.Base) {
		now = d.Base
	}
	return now.Add(d.Offset)
}

// WindowAt returns the first time the rolling deadline is within the lead.
// An offset beyond the lead stays outside as the origin rolls forward.
func (d RollingDeadline) WindowAt(ahead time.Duration) time.Time {
	if d.Base.IsZero() || d.Offset > ahead {
		return time.Time{}
	}
	return d.Base.Add(d.Offset).Add(-ahead)
}

func (n Need) ReadyAt(now time.Time) time.Time {
	return earlier(n.ReadyBy, n.Rolling.At(now))
}

// EarliestReady is the lower bound of the deadline, including outside the window.
func (n Need) EarliestReady() time.Time {
	return n.ReadyAt(n.Rolling.Base)
}

// WindowAt returns the earliest finite entry time. The caller applies
// WindowClosed separately: a fixed handoff can still protect local data.
func (n Need) WindowAt(ahead time.Duration) time.Time {
	at := n.Rolling.WindowAt(ahead)
	if !n.ReadyBy.IsZero() {
		at = earlier(at, n.ReadyBy.Add(-ahead))
	}
	return at
}
