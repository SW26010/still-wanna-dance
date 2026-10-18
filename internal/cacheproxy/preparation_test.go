package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func TestPreparationWaitsForUsageReleaseOrCancellation(t *testing.T) {
	for _, cancelWaiting := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelWaiting), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				_, s := testSession(t)
				s.ctx = context.Background()
				_, release := addTestDemand(t, s, context.Background(), video{key: "occupied", size: backgroundUseBlocks * rangeBlockSize}, false)
				defer release()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				result := make(chan error, 1)
				go func() {
					_, closeSource, err := s.addPreparation(ctx, video{key: "next", size: 1})
					if closeSource != nil {
						closeSource()
					}
					result <- err
				}()
				synctest.Wait()
				select {
				case err := <-result:
					t.Fatal("pending preparation did not wait for capacity", err)
				default:
				}
				if cancelWaiting {
					cancel()
				} else {
					release()
				}
				err := <-result
				if cancelWaiting && !errors.Is(err, context.Canceled) || !cancelWaiting && err != nil {
					t.Fatal("incorrect admission result", err)
				}
			})
		})
	}
}

func TestPreparationAdmissionIsIndependentOfSessionCountAndCompletion(t *testing.T) {
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 40)
	for i := range 40 {
		prepareCtx, admitted := WithPreparationAdmission(ctx)
		go func() {
			_, err := s.Prefetch(prepareCtx, videoURL(fmt.Sprintf("resource %d", i)))
			results <- err
		}()
		select {
		case <-admitted:
		case err := <-results:
			t.Fatal("preparation ended before admission", err)
		case <-time.After(5 * time.Second):
			t.Fatal("metadata remained behind resource session count", i)
		}
	}
	// All forty sources must also reach their resource session. Merely
	// signalling before an old 32-session wait would not remove the gate.
	deadline := time.After(5 * time.Second)
	for {
		s.mu.Lock()
		n, changed := len(s.flights), s.capacityChanged
		s.mu.Unlock()
		if n == 40 {
			break
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatal("admitted source blocked behind a resource-session gate", n)
		}
	}
	// Admission belongs to the original source, not its obtain attachment.
	s.mu.Lock()
	sources, blocks := len(s.demands), len(s.useBlocks)
	for _, n := range s.useBlocks {
		if n != 1 {
			t.Error("preparation registered duplicate source ownership", n)
		}
	}
	s.mu.Unlock()
	if sources != 40 || blocks != 40 {
		t.Fatal("usage did not survive independent pending results", sources, blocks)
	}
	cancel()
	for range 40 {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatal("pending result did not observe cancellation", err)
		}
	}
}
