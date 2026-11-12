package cacheproxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

type pausedPublicationFile struct {
	*os.File
	phase   string
	started chan struct{}
	resume  chan struct{}
}

func (f *pausedPublicationFile) Sync() error {
	if f.phase == "sync" {
		close(f.started)
		<-f.resume
	}
	return f.File.Sync()
}

func (f *pausedPublicationFile) Close() error {
	if f.phase == "close" {
		close(f.started)
		<-f.resume
	}
	return f.File.Close()
}

func stagedPublicationFile(t *testing.T, cfg Config, body string) *os.File {
	t.Helper()
	file, err := os.CreateTemp(cfg.publishDir(), "download-publish-*.part")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		file.Close()
		os.Remove(file.Name())
	})
	if _, err := file.WriteString(body); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestPublicationPreparationDoesNotBlockRetentionDecisions(t *testing.T) {
	for _, phase := range []string{"sync", "close"} {
		t.Run(phase, func(t *testing.T) {
			s, cfg := setup(t, nil)
			f, v := completedRangeFlight(t, s, payload)
			otherFlight, otherVideo := completedRangeFlight(t, s, "other video")
			seedPrioritySong(t, s, "9000001", v.key)
			r, err := s.reservePublication(t.Context(), v)
			if err != nil {
				t.Fatal(err)
			}
			defer s.releasePublication(r)
			file := &pausedPublicationFile{
				File: stagedPublicationFile(t, cfg, payload), phase: phase,
				started: make(chan struct{}), resume: make(chan struct{}),
			}
			otherFile := stagedPublicationFile(t, cfg, "other video")
			resume := sync.OnceFunc(func() { close(file.resume) })
			published := make(chan error, 1)
			publicationDone := make(chan struct{})
			go func() {
				defer close(publicationDone)
				published <- s.commitPublication(r, file, cfg.videoFile(v.key), v, f)
			}()
			defer func() { resume(); <-publicationDone }()
			receiveDemandEvent(t, file.started)

			operations := make(chan error, 1)
			operationsDone := make(chan struct{})
			go func() {
				defer close(operationsDone)
				operations <- func() error {
					if err := s.ObserveSongURL(t.Context(), PlaybackObservation{
						SongID: 9000002, Node: "cf", URL: videoURL("other video"),
					}); err != nil {
						return fmt.Errorf("mapping write: %w", err)
					}
					if err := s.Store.SetSongPolicy(t.Context(), 9000002, SongFavorite); err != nil {
						return fmt.Errorf("favorite write: %w", err)
					}
					other, err := s.reservePublication(t.Context(), otherVideo)
					if err != nil {
						return fmt.Errorf("other admission: %w", err)
					}
					defer s.releasePublication(other)
					if err := s.commitPublication(other, otherFile, cfg.videoFile(otherVideo.key), otherVideo, otherFlight); err != nil {
						return fmt.Errorf("other publication: %w", err)
					}
					return s.Store.SetSongPolicy(t.Context(), 9000001, SongBlocked)
				}()
			}()
			defer func() { resume(); <-operationsDone }()
			select {
			case err := <-operations:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("retention operations blocked while publication %s was paused", phase)
			}
			expectRetained(t, cfg.videoFile(v.key), false)
			expectRetained(t, cfg.videoFile(otherVideo.key), true)
			resume()
			if err := receiveDemandEvent(t, published); !errors.Is(err, ErrNotRetained) {
				t.Fatal("publication ignored a policy change during preparation", err)
			}
			expectRetained(t, cfg.videoFile(v.key), false)
			if retainedBytes(s) != otherVideo.size {
				t.Fatal("abandoned publication changed inventory", retainedBytes(s))
			}
		})
	}
}

func TestPublicationPreparationRechecksCanceledReservation(t *testing.T) {
	for _, reason := range []string{"canceled", "displaced"} {
		t.Run(reason, func(t *testing.T) {
			s, cfg := setup(t, nil)
			f, v := completedRangeFlight(t, s, payload)
			high := video{key: videoKey("higher priority"), size: v.size}
			if reason == "displaced" {
				seedPrioritySong(t, s, "9000001", v.key)
				seedPrioritySong(t, s, "9000002", high.key)
				setRetentionLimit(t, s, v.size)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			r, err := s.reservePublication(ctx, v)
			if err != nil {
				t.Fatal(err)
			}
			defer s.releasePublication(r)
			file := &pausedPublicationFile{
				File: stagedPublicationFile(t, cfg, payload), phase: "sync",
				started: make(chan struct{}), resume: make(chan struct{}),
			}
			resume := sync.OnceFunc(func() { close(file.resume) })
			published := make(chan error, 1)
			publicationDone := make(chan struct{})
			go func() {
				defer close(publicationDone)
				published <- s.commitPublication(r, file, cfg.videoFile(v.key), v, f)
			}()
			defer func() { resume(); <-publicationDone }()
			receiveDemandEvent(t, file.started)
			changed := make(chan error, 1)
			changeDone := make(chan struct{})
			go func() {
				defer close(changeDone)
				if reason == "canceled" {
					cancel()
					changed <- nil
					return
				}
				other, err := s.reservePublication(t.Context(), high)
				if err == nil {
					s.releasePublication(other)
				}
				changed <- err
			}()
			defer func() { resume(); <-changeDone }()
			if err := receiveDemandEvent(t, changed); err != nil {
				t.Fatal(err)
			}
			resume()
			want := context.Canceled
			if reason == "displaced" {
				want = ErrNotRetained
			}
			if err := receiveDemandEvent(t, published); !errors.Is(err, want) {
				t.Fatal("publication ignored a canceled reservation", err)
			}
			expectRetained(t, cfg.videoFile(v.key), false)
			if retainedBytes(s) != 0 {
				t.Fatal("abandoned publication changed inventory", retainedBytes(s))
			}
		})
	}
}
