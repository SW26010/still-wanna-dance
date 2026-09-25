package applog

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"strings"
)

type traceKey struct{}

// WithTrace preserves an existing operation across resolution and cache work.
func WithTrace(ctx context.Context) context.Context {
	if TraceID(ctx) != "" {
		return ctx
	}
	return context.WithValue(ctx, traceKey{}, rand.Text())
}

func TraceID(ctx context.Context) string {
	id, _ := ctx.Value(traceKey{}).(string)
	return id
}

type locationError struct{ cause error }

func (e *locationError) Error() string {
	return "failed to parse Location header (URL details redacted)"
}
func (e *locationError) Unwrap() error { return e.cause }

// SafeError removes request URLs from transport errors, preserving error identity.
func SafeError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := err.(*locationError); ok {
		return err
	}
	// net/http formats an invalid redirect Location with %q and its parse
	// error with %v before wrapping in url.Error. Neither URL can be removed
	// by unwrapping. Discard the whole diagnostic instead of trying to parse
	// a potentially malformed, relative or quoted URL out of its text.
	if strings.Contains(err.Error(), "failed to parse Location header ") {
		return &locationError{cause: err}
	}
	var u *url.Error
	if errors.As(err, &u) {
		return SafeError(u.Err)
	}
	return err
}
