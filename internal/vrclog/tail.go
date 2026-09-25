package vrclog

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const PollInterval = 3 * time.Second
const DiscoverInterval = 15 * time.Second
const maxChunk = 4 << 20
const maxLine = 1 << 20

// Tail is single-owner. Existing logs start at EOF; new sessions start at zero.
// Partial UTF-8 lines are retained until the next newline, with bounded memory.
type Tail struct {
	Dir         string
	Path        string
	Offset      int64
	info        os.FileInfo
	nextScan    time.Time
	initialized bool
	pending     []byte
	discard     bool
}

func (t *Tail) Poll(now time.Time) ([]Event, error) {
	var events []Event
	if !now.Before(t.nextScan) {
		t.nextScan = now.Add(DiscoverInterval)
		entries, err := os.ReadDir(t.Dir)
		if err != nil {
			return nil, err
		}
		// VRChat filenames contain sortable session start timestamps. Do not jump
		// backwards when an old log's mtime changes (e.g. an archive copy).
		latest := ""
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), "output_log_") && strings.HasSuffix(e.Name(), ".txt") && e.Name() > latest {
				latest = e.Name()
			}
		}
		if latest != "" && (t.Path == "" || latest > filepath.Base(t.Path)) {
			path := filepath.Join(t.Dir, latest)
			info, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			t.Path, t.info, t.Offset, t.pending, t.discard = path, info, 0, nil, false
			if !t.initialized {
				t.Offset = info.Size()
				// Do not interpret the suffix of an in-progress line as a new event.
				if t.Offset > 0 {
					f, err := os.Open(path)
					if err != nil {
						return events, err
					}
					var last [1]byte
					_, err = f.ReadAt(last[:], t.Offset-1)
					f.Close()
					if err != nil {
						return events, err
					}
					t.discard = last[0] != '\n'
				}
			}
			events = append(events, Event{Reset: true})
		}
		t.initialized = true
	}
	if t.Path == "" {
		return events, nil
	}
	f, err := os.Open(t.Path)
	if err != nil {
		return append(events, Event{Reset: true}), err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return events, err
	}
	if !os.SameFile(t.info, info) || info.Size() < t.Offset {
		t.Offset, t.pending, t.discard = 0, nil, false
		events = append(events, Event{Reset: true})
	}
	t.info = info
	if info.Size() == t.Offset {
		return events, nil
	}
	if _, err = f.Seek(t.Offset, io.SeekStart); err != nil {
		return events, err
	}
	b, err := io.ReadAll(io.LimitReader(f, maxChunk))
	if err != nil {
		return events, err
	}
	t.Offset += int64(len(b))
	t.pending = append(t.pending, b...)
	for {
		i := bytes.IndexByte(t.pending, '\n')
		if i < 0 {
			break
		}
		line := t.pending[:i]
		t.pending = t.pending[i+1:]
		if t.discard || len(line) > maxLine {
			t.discard = false
			events = append(events, Event{Reset: true})
			continue
		}
		e, ok, parseErr := Parse(string(line))
		if parseErr != nil {
			events = append(events, Event{Reset: true})
			continue
		}
		if ok {
			events = append(events, e)
		}
	}
	if len(t.pending) > maxLine {
		t.pending = nil
		t.discard = true
		events = append(events, Event{Reset: true})
	}
	return events, nil
}
