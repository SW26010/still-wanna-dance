package console

import (
	"context"
	"errors"

	"still-wanna-dance/internal/faults"
)

// SetFaultCenter connects existing application fault boundaries to the shared
// desktop notifier. It does not change error reporting or persistence.
func (c *Console) SetFaultCenter(center *faults.Center) {
	c.faults.Store(center)
	c.queue.setLogFaultObserver(func(err error) {
		c.reportRetryFault("queue:log", err)
	})
	c.reportFault("storage", c.storeOpenError)
	c.reportFault("work", c.workOpenError)
	c.mu.Lock()
	importError := c.importStatus.Error
	settings := c.settings
	c.mu.Unlock()
	if importError != "" {
		c.reportFault("task:import", errors.New(importError))
	}
	if settings.UpstreamMode == "proxy" {
		if settings.ProxyBackend == "external-socks5" && settings.proxySecretError != "" {
			c.reportFault("proxy", errors.New(settings.proxySecretError))
		} else if settings.ProxyBackend == "mihomo" && settings.nodeSecretError != "" {
			c.reportFault("proxy", errors.New(settings.nodeSecretError))
		}
	}
}

func (c *Console) reportRetryFault(source string, err error) {
	if !cancellationOnly(err) {
		if center := c.faults.Load(); center != nil {
			center.Retry(source, err)
		}
	}
}

func (c *Console) reportFault(source string, err error) {
	// A deliberate cancellation is not a failed incident or recovery.
	if cancellationOnly(err) {
		return
	}
	if center := c.faults.Load(); center != nil {
		center.Set(source, err)
	}
}

func cancellationOnly(err error) bool {
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !cancellationOnly(child) {
				return false
			}
		}
		return true
	}
	if child := errors.Unwrap(err); child != nil {
		return cancellationOnly(child)
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, errHostsAuthorizationCanceled)
}

// WatchFaults observes retained storage faults even when no browser is polling.
// Engine publication and shutdown use the existing lifecycle notifications.
func (c *Console) WatchFaults(ctx context.Context) {
	storeDone := make(chan struct{})
	go func() {
		defer close(storeDone)
		if c.store == nil {
			return
		}
		select {
		case <-c.store.FailureDone():
			c.reportFault("storage", c.store.Error())
		case <-c.lifecycle.stop:
		case <-ctx.Done():
		}
	}()
	if engine := c.waitBlockEngine(ctx); engine != nil {
		select {
		case <-engine.LocalFailureDone():
			c.reportFault("storage", engine.StorageError())
		case <-c.lifecycle.stop:
		case <-ctx.Done():
		}
	}
	<-storeDone
}
