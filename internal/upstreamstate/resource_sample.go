package upstreamstate

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// The clock covers continuous body reading, excluding DNS, TLS and headers.
// Full resources may finish before minimum; a stalled read is cut off at maximum.
func readResourceSample(ctx context.Context, body io.ReadCloser, size, minimumBytes int64, minimum, maximum time.Duration) (bytes int64, duration time.Duration, err error) {
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
	reader := io.LimitReader(body, size+1)
	buf := make([]byte, 64<<10)
	for {
		n, readErr := reader.Read(buf)
		bytes += int64(n)
		if ctx.Err() != nil {
			return bytes, 0, ctx.Err()
		}
		if bytes > size {
			return bytes, 0, errors.New("resource exceeds declared size")
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
		if bytes >= minimumBytes && bytes < size && time.Since(started) >= minimum {
			return bytes, 0, nil
		}
	}
}
