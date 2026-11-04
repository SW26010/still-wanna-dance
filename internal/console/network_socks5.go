package console

import (
	"context"
	"errors"
	"time"
)

const socks5ServiceInterval = 30 * time.Second

type socks5ServiceStatus struct {
	Checking      bool      `json:"checking"`
	Closed        bool      `json:"closed"`
	State         string    `json:"state"`
	Error         string    `json:"error,omitempty"`
	Authenticated bool      `json:"authenticated"`
	LatencyMS     *float64  `json:"latencyMS"`
	ObservedAt    time.Time `json:"observedAt"`
	NextCheck     time.Time `json:"nextCheck"`
}

// Caller holds n.mu. The optional probe uses the fixed active configuration.
func (n *networkDiagnostics) startSOCKS5Locked() {
	if n.options.SOCKS5 == nil || n.socks5Done != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.socks5Cancel, n.socks5Done = cancel, make(chan struct{})
	n.socks5Wake = make(chan struct{}, 1)
	n.socks5.Checking = true
	n.tasks.Go(func() {
		defer close(n.socks5Done)
		for {
			n.mu.Lock()
			if ctx.Err() != nil {
				n.mu.Unlock()
				return
			}
			n.socks5.Checking, n.socks5.NextCheck = true, time.Time{}
			// A manual trigger racing the automatic deadline joins this check.
			select {
			case <-n.socks5Wake:
			default:
			}
			n.mu.Unlock()
			started := time.Now()
			authenticated, err := n.options.SOCKS5(ctx)
			finished := time.Now()
			n.mu.Lock()
			if ctx.Err() != nil {
				n.mu.Unlock()
				return
			}
			n.socks5 = socks5ServiceStatus{State: "available", Authenticated: authenticated, ObservedAt: finished, NextCheck: finished.Add(socks5ServiceInterval)}
			if err != nil {
				n.socks5.State, n.socks5.Error = "failed", err.Error()
				if errors.Is(err, context.DeadlineExceeded) {
					n.socks5.Error = "SOCKS5 服务检查超时"
				}
			} else {
				ms := float64(finished.Sub(started)) / float64(time.Millisecond)
				n.socks5.LatencyMS = &ms
			}
			n.mu.Unlock()
			timer := time.NewTimer(socks5ServiceInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			case <-n.socks5Wake:
				timer.Stop()
			}
		}
	})
}

func (n *networkDiagnostics) checkSOCKS5() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.connectivity.Closed {
		return diagnosticsClosed()
	}
	if n.options.SOCKS5 == nil {
		return rejectRequest(errors.New("当前上游未使用外部 SOCKS5"))
	}
	if n.socks5Done == nil {
		n.startSOCKS5Locked()
	} else if !n.socks5.Checking {
		n.socks5.Checking, n.socks5.NextCheck = true, time.Time{}
		n.socks5Wake <- struct{}{}
	}
	return nil
}
