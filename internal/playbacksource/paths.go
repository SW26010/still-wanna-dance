package playbacksource

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"still-wanna-dance/internal/upstreamrequest"
)

// ReadPaths tries validated playback reads in preference order. A slow path
// keeps its chance to succeed while the next path starts on its share of the
// deadline. Fast failures advance immediately. Only the caller owns the result.
func ReadPaths[T any](parent context.Context, clients []*http.Client, timeout time.Duration, read func(context.Context, *http.Client) (T, error)) (T, error) {
	var zero T
	ctx, cancel := context.WithTimeout(parent, timeout)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
	}()
	if err := ctx.Err(); err != nil {
		return zero, context.Cause(ctx)
	}
	clients = clients[:min(len(clients), upstreamrequest.MaxOperationCandidates)]
	switch len(clients) {
	case 0:
		return zero, upstreamrequest.ErrUnavailable
	case 1:
		return read(ctx, clients[0])
	}
	type result struct {
		value T
		err   error
	}
	done := make(chan result, len(clients))
	deadline, _ := ctx.Deadline()
	interval := time.Until(deadline) / time.Duration(len(clients))
	timer := time.NewTimer(interval)
	defer timer.Stop()
	start := func() {
		client := clients[0]
		clients = clients[1:]
		workers.Go(func() {
			value, err := read(ctx, client)
			done <- result{value, err}
		})
	}
	var failures []error
	count := len(clients)
	start()
	for {
		select {
		case <-ctx.Done():
			return zero, context.Cause(ctx)
		case r := <-done:
			if ctx.Err() != nil {
				return zero, context.Cause(ctx)
			}
			if r.err == nil {
				return r.value, nil
			}
			failures = append(failures, r.err)
			if len(failures) == count {
				return r.value, errors.Join(failures...)
			}
		case <-timer.C:
		}
		if len(clients) > 0 && ctx.Err() == nil {
			start()
			timer.Reset(interval)
		}
	}
}
