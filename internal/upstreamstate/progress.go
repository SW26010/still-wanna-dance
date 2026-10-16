package upstreamstate

import (
	"context"
	"time"
)

// CheckProgress describes the phase about to start and the observations so far.
// An empty Operation marks batch completion. Results contain no response bodies
// or signed playback URLs. Failed prerequisites can leave later phases unstarted.
type CheckProgress struct {
	Operation Operation
	Started   time.Time
	Results   []Result
}

func (m *Monitor) publishProgress(ctx context.Context, op Operation) {
	if m.onProgress == nil || ctx.Err() != nil || m.channel.Snapshot().Revision != m.batchRevision {
		return
	}
	s := m.Snapshot()
	m.onProgress(ctx, CheckProgress{Operation: op, Started: s.Started, Results: s.Results})
}
