package upstreamstate

import (
	"context"
	"io"
	"sync"
	"time"
)

// The clock covers continuous body reading, excluding DNS, TLS and headers.
// Stop at the first of the time limit, byte limit, or complete resource.
func readResourceSample(ctx context.Context, body io.ReadCloser, size, maximumBytes int64, maximum time.Duration) (bytes int64, duration time.Duration, err error) {
	started := time.Now()
	var once sync.Once
	closeBody := func() { once.Do(func() { _ = body.Close() }) }
	expired := make(chan struct{})
	timer := time.AfterFunc(maximum, func() { close(expired); closeBody() })
	stopCancel := context.AfterFunc(ctx, closeBody)
	defer func() {
		duration = time.Since(started)
		timer.Stop()
		stopCancel()
		closeBody()
	}()
	limit := min(size, maximumBytes)
	reader := io.LimitReader(body, limit)
	buf := make([]byte, 64<<10)
	for {
		n, readErr := reader.Read(buf)
		bytes += int64(n)
		if ctx.Err() != nil {
			return bytes, 0, ctx.Err()
		}
		select {
		case <-expired:
			if bytes > 0 {
				return bytes, 0, nil
			}
			return bytes, 0, context.DeadlineExceeded
		default:
		}
		if readErr != nil {
			if readErr == io.EOF && bytes == size {
				return bytes, 0, nil
			}
			if readErr == io.EOF {
				readErr = io.ErrUnexpectedEOF
			}
			return bytes, 0, readErr
		}
		if bytes >= limit {
			return bytes, 0, nil
		}
	}
}
