package cacheproxy

import (
	"context"
	"sort"
	"time"
)

// ActiveDownload contains observation data only; never URLs or proxy settings.
type ActiveDownload struct {
	ID             uint64      `json:"id"`
	Resource       string      `json:"resource"`
	Stage          string      `json:"stage"`
	Host           string      `json:"host"`
	Bytes          int64       `json:"bytes"`
	Size           int64       `json:"size"`
	BytesPerSecond float64     `json:"bytesPerSecond"`
	IdleMS         int64       `json:"idleMS"`
	Songs          []CacheSong `json:"songs"`
	MoreSongs      bool        `json:"moreSongs"`
	songIDs        []string
}

type ActiveDownloads struct {
	Tasks          []ActiveDownload `json:"tasks"`
	BytesPerSecond float64          `json:"bytesPerSecond"`
}

// ActiveDownloads copies each shared flight once. No disk or network I/O is
// performed under the worker lock. Song associations are bounded candidates,
// never a claim about which song is actually playing.
func (s *Server) ActiveDownloads(ctx context.Context) (result ActiveDownloads, err error) {
	result = ActiveDownloads{Tasks: []ActiveDownload{}}
	// Close may race with optional song enrichment. database/sql safely handles
	// concurrent Close; discard this engine's snapshot (and close errors) once
	// shutdown starts, without retaining the engine or waiting for shutdown.
	defer func() {
		s.mu.Lock()
		if s.closed {
			result, err = ActiveDownloads{Tasks: []ActiveDownload{}}, nil
		}
		s.mu.Unlock()
	}()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return result, nil
	}
	for key, f := range s.flights {
		p := f.progress
		if p == nil {
			continue
		}
		p.mu.Lock()
		now := time.Now()
		task := ActiveDownload{ID: f.id, Resource: key, Stage: p.stage, Host: p.host,
			Bytes: p.bytes, Size: p.size, BytesPerSecond: p.rate(now), IdleMS: max(0, now.Sub(p.lastByte).Milliseconds()), Songs: []CacheSong{}}
		p.mu.Unlock()
		for id := range f.monitorSongs {
			task.songIDs = append(task.songIDs, id)
		}
		result.BytesPerSecond += task.BytesPerSecond
		result.Tasks = append(result.Tasks, task)
	}
	s.mu.Unlock()
	sort.Slice(result.Tasks, func(i, j int) bool { return result.Tasks[i].ID < result.Tasks[j].ID })
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	for i := range result.Tasks {
		task := &result.Tasks[i]
		songs, err := s.Store.resourceSongLabels(ctx, task.Resource, task.songIDs)
		if err != nil {
			return ActiveDownloads{}, err
		}
		task.MoreSongs = len(songs) > 20
		task.Songs = songs[:min(len(songs), 20)]
	}

	return result, nil
}
