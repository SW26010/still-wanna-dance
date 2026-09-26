package console

import (
	"context"
	"time"

	"stepstash/internal/cacheproxy"
)

// A completed scan can feed the next download in this process. Entries with
// errors are absent and follow the normal resolve/download path.
type scanPlan struct {
	settings Settings
	songs    []Song
	results  map[int64]scanResult
}

type scanResult struct {
	localOnly bool
	target    string
	receipt   *cacheproxy.LocalReceipt
}

// Separate limits let address requests overlap disk reads without multiplying
// request rate or allowing every network worker to hash a video at once.
type scanLimiter struct {
	resolve chan struct{}
	local   chan struct{}
	ticker  *time.Ticker
}

func newScanLimiter(s Settings) *scanLimiter {
	return &scanLimiter{
		resolve: make(chan struct{}, s.ScanResolveConcurrency),
		local:   make(chan struct{}, s.ScanCheckConcurrency),
		ticker:  time.NewTicker(150 * time.Millisecond),
	}
}

func scanAcquire(ctx context.Context, slots chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-slots
			return err
		}
		return nil
	}
}

func (s *scanLimiter) check(ctx context.Context, resolve func() (string, error), check func(string) (bool, error)) (bool, error) {
	if err := scanAcquire(ctx, s.resolve); err != nil {
		return false, err
	}
	select {
	case <-ctx.Done():
		<-s.resolve
		return false, ctx.Err()
	case <-s.ticker.C:
	}
	if err := ctx.Err(); err != nil {
		<-s.resolve
		return false, err
	}
	target, err := resolve()
	<-s.resolve
	if err != nil {
		return false, err
	}
	if err := scanAcquire(ctx, s.local); err != nil {
		return false, err
	}
	defer func() { <-s.local }()
	return check(target)
}
