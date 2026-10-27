package console

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

func TestEngineReadsShareCreationAndDrainBeforeClose(t *testing.T) {
	c := testConsole(t)
	key := strings.Repeat("a", 32)
	type result struct {
		engine  *cacheproxy.Server
		release func()
		err     error
	}
	const readers = 8
	results := make(chan result, readers)
	start := make(chan struct{})
	for range readers {
		go func() {
			<-start
			s, release, err := c.retainEngineRead(t.Context(), key)
			results <- result{s, release, err}
		}()
	}
	close(start)
	var engine *cacheproxy.Server
	var releases []func()
	defer func() {
		for _, release := range releases {
			release()
		}
	}()
	for range readers {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatal(r.err)
			}
			releases = append(releases, r.release)
			if engine != nil && engine != r.engine {
				t.Fatal("concurrent readers created separate engines")
			}
			engine = r.engine
		case <-time.After(5 * time.Second):
			t.Fatal("read admission blocked")
		}
	}
	// Background work and preview reads must share the same owner.
	c.lifecycleMu.Lock()
	background, err := c.ensureEngineLocked()
	c.lifecycleMu.Unlock()
	if err != nil || background != engine || c.waitBlockEngine(t.Context()) != engine {
		t.Fatal("task/observer did not receive the shared engine", err)
	}
	c.BeginShutdown()
	if _, release, err := c.retainEngineRead(t.Context(), key); !errors.Is(err, errConsoleClosing) || release != nil {
		t.Fatal("shutdown admitted a new read", err)
	}
	for _, release := range releases[:readers-1] {
		release()
	}
	closed := make(chan error, 1)
	go func() { closed <- c.Close() }()
	// Observe the real engine's admission closure before asserting it is still
	// waiting for the last retained read, rather than racing the Close goroutine.
	deadline := time.Now().Add(5 * time.Second)
	for {
		release, err := engine.RetainRead(key)
		if err != nil {
			break
		}
		release()
		if time.Now().After(deadline) {
			t.Fatal("engine did not begin closing")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-closed:
		t.Fatal("close did not wait for the final read", err)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := c.store.SongUsageCount(t.Context()); err != nil {
		t.Fatal("application store closed before the retained read", err)
	}
	releases[readers-1]()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not finish after releasing reads")
	}
}

func TestEngineAdmissionRejectsShutdownAndCanceledWait(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		c := testConsole(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		if shutdown {
			c.BeginShutdown()
		}
		c.lifecycleMu.Lock()
		done := make(chan error, 1)
		go func() {
			engine, release, err := c.retainEngineRead(ctx, strings.Repeat("a", 32))
			if engine != nil || release != nil {
				t.Error("rejected read returned an engine or reference")
			}
			done <- err
		}()
		want := errConsoleClosing
		if !shutdown {
			cancel()
			want = context.Canceled
		}
		c.lifecycleMu.Unlock()
		select {
		case err := <-done:
			if !errors.Is(err, want) {
				t.Fatal("wrong admission error", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("rejected read remained blocked")
		}
		if shutdown {
			c.lifecycleMu.Lock()
			_, err := c.ensureEngineLocked()
			c.lifecycleMu.Unlock()
			if !errors.Is(err, errConsoleClosing) {
				t.Fatal("shutdown admitted background engine creation", err)
			}
		}
		if c.service != nil {
			t.Fatal("rejected admission created an engine")
		}
	}
}

func TestEngineCreationFailureCanRetryWithoutPublishing(t *testing.T) {
	c := testConsole(t)
	videoDir := c.settings.VideoDir
	c.settings.VideoDir = filepath.Join(t.TempDir(), "missing")
	if engine, release, err := c.retainEngineRead(t.Context(), strings.Repeat("a", 32)); err == nil || engine != nil || release != nil || c.service != nil {
		t.Fatal("failed creation published an engine or read", err)
	}
	c.settings.VideoDir = videoDir
	engine, release, err := c.retainEngineRead(t.Context(), strings.Repeat("a", 32))
	if err != nil {
		t.Fatal("failed creation prevented retry", err)
	}
	defer release()
	if c.waitBlockEngine(t.Context()) != engine {
		t.Fatal("retry did not publish the new engine")
	}
}
