package vrclog

import (
	"bytes"
	"io"
	"time"
)

const roomLookback = 4 << 20

// RoomSnapshot is folded in memory. Only the final room can register demand.
type RoomSnapshot struct {
	Timeline              PlayerTimeline
	Songs                 []Song
	QueueAt, LastActivity time.Time
	AwaitFirstQueue       bool
	FirstQueueAt          time.Time
	SelectedSong          int64
}

func (t *Tail) recoverRoom(r io.ReaderAt, now time.Time) (RoomSnapshot, error) {
	buf := make([]byte, min(t.Offset, roomLookback))
	base := t.Offset - int64(len(buf))
	complete, end := -1, len(buf)
	// Discover complete lines backwards in pages. The oldest page may
	// begin inside a line; only byte zero is an implicit line boundary.
	for high := len(buf); high > 0; {
		low := max(0, high-64*1024)
		if _, err := r.ReadAt(buf[low:high], base+int64(low)); err != nil {
			return RoomSnapshot{}, err
		}
		for cursor := high; ; {
			n := bytes.LastIndexByte(buf[low:cursor], '\n')
			if n < 0 && base+int64(low) > 0 {
				break
			}
			start := low + n + 1
			line := buf[start:end]
			if complete < 0 {
				complete = start
				t.discard = len(line) > maxLine
				if !t.discard {
					t.pending = bytes.Clone(line)
				}
			} else if len(line) <= maxLine {
				e, _, _ := Parse(string(line))
				if e.Reset {
					return RoomSnapshot{}, nil
				}
				if e.EnteredRoom {
					room := replayRoom(buf[start:complete], now)
					if t.discard {
						room.clearQueue()
					}
					return room, nil
				}
			}
			if n < 0 {
				break
			}
			end = start - 1
			cursor = end
		}
		high = low
	}
	if complete < 0 && t.Offset > 0 {
		t.discard = true
	}
	return RoomSnapshot{}, nil
}

func replayRoom(data []byte, now time.Time) RoomSnapshot {
	room := RoomSnapshot{AwaitFirstQueue: true}
	for raw := range bytes.Lines(data) {
		line := string(bytes.TrimSuffix(raw, []byte{'\n'}))
		at := logTime(line)
		if at.After(now) {
			return RoomSnapshot{}
		}
		if !at.IsZero() {
			if !room.LastActivity.IsZero() && at.Sub(room.LastActivity) >= IdleTimeout {
				room.clearQueue()
			}
			room.LastActivity = at
		}
		if len(line) > maxLine {
			room.clearQueue()
			continue
		}
		e, ok := parseLogLine(line)
		if !ok {
			continue
		}
		switch {
		case e.EnteredRoom:
			if at.IsZero() {
				return RoomSnapshot{}
			}
		case e.QueueReset:
			room.clearQueue()
		case e.Songs != nil:
			if at.IsZero() {
				room.clearQueue()
				continue
			}
			if room.SelectedSong != 0 && SelectedSongDeparted(room.Songs, e.Songs, Song{ID: room.SelectedSong}, Song{ID: -1}) {
				room.SelectedSong = 0
			}
			room.Songs, room.QueueAt = e.Songs, at
			if room.AwaitFirstQueue && len(e.Songs) > 0 {
				room.AwaitFirstQueue = false
				room.FirstQueueAt = at
			}
		case e.Playback != nil:
			before := room.Timeline.Anchor
			change := room.Timeline.Observe(e, at)
			if change.SelectedSong != nil {
				room.SelectedSong = *change.SelectedSong
			}
			if change.Stopped || (before != room.Timeline.Anchor && !room.Timeline.Anchor.At.IsZero()) {
				room.AwaitFirstQueue = false
				room.FirstQueueAt = time.Time{}
				if !room.Timeline.selectionPending {
					room.SelectedSong = 0
				}
			}
		}
	}
	if now.Sub(room.LastActivity) >= IdleTimeout {
		return RoomSnapshot{}
	}
	return room
}

func (r *RoomSnapshot) clearQueue() {
	r.Songs, r.QueueAt, r.SelectedSong = nil, time.Time{}, 0
}
