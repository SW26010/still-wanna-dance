package vrclog

import "time"

type PlayerRole uint8

const (
	UnknownRole PlayerRole = iota
	Owner
	Follower
)

func (r PlayerRole) String() string {
	switch r {
	case Owner:
		return "owner"
	case Follower:
		return "non-owner"
	default:
		return "unknown"
	}
}

// An owner start uses the song's catalog start. A synchronization always
// supplies an absolute media position, including for late joiners.
type PlaybackAnchor struct {
	SongID       int64
	At           time.Time
	Started      time.Time // Identity of the first start/sync in this playback.
	Position     float64
	CatalogStart bool
}

type PlayerTimeline struct {
	Role                 PlayerRole
	Anchor               PlaybackAnchor
	route                int64
	lastEvent, lastStart time.Time
	synced               bool // The current route has supplied a synchronization.
}

// Observe is called in file order by the single log reader. Route lines
// identify anonymous sync callbacks but cannot themselves start playback.
func (p *PlayerTimeline) Observe(event Event, received time.Time) {
	if event.Reset {
		*p = PlayerTimeline{}
		return
	}
	e := event.Playback
	if e == nil || e.At.Before(p.lastEvent) || e.At.After(received) {
		return
	}
	// A stop in file order still ends playback after a delayed read.
	if e.Kind != "end" && e.Kind != "stop" && received.Sub(e.At) > 4*time.Second {
		if e.Owner != nil && (*e.Owner != (p.Role == Owner)) {
			p.Role = UnknownRole
		}
		switch e.Kind {
		case "route":
			// An untrusted route cannot identify the next anonymous sync.
			// The established room clock continues until a playback fact replaces it.
			p.route, p.lastStart, p.synced = 0, time.Time{}, false
		case "start":
			*p = PlayerTimeline{Role: p.Role, route: e.SongID, lastStart: e.At, lastEvent: e.At}
		}
		return
	}
	p.lastEvent = e.At
	if e.Owner != nil {
		p.Role = Follower
		if *e.Owner {
			p.Role = Owner
		} else if p.Anchor.CatalogStart {
			p.Anchor = PlaybackAnchor{}
		}
	}
	switch e.Kind {
	case "route":
		p.lastStart, p.synced = time.Time{}, false
		p.route = e.SongID
	case "start":
		if e.SongID == 0 {
			*p = PlayerTimeline{Role: p.Role, lastEvent: e.At}
			return
		}
		if p.route == e.SongID && p.lastStart.Equal(e.At) && (p.Anchor.SongID != 0 || p.Role != Owner) {
			return
		}
		if p.route == e.SongID && p.synced && (p.lastStart.IsZero() || p.Anchor.At.Equal(e.At)) {
			if !p.lastStart.IsZero() {
				p.Anchor.Started = e.At
			}
			p.lastStart = e.At
			return
		}
		p.route, p.lastStart, p.synced = e.SongID, e.At, false
		p.Anchor = PlaybackAnchor{}
		if p.Role == Owner {
			p.Anchor = PlaybackAnchor{SongID: e.SongID, Started: e.At, At: e.At, CatalogStart: true}
		}
	case "sync":
		if p.route <= 0 {
			return
		}
		started := p.Anchor.Started
		if p.Anchor.SongID != p.route || started.IsZero() || (p.lastStart.IsZero() && !p.synced) {
			started = e.At
		}
		p.synced = true
		p.Anchor = PlaybackAnchor{Started: started, SongID: p.route, At: e.At, Position: e.Position}
	case "end", "stop":
		if e.SongID == 0 || e.SongID == p.Anchor.SongID {
			p.Anchor = PlaybackAnchor{}
		}
		if e.SongID == 0 || e.SongID == p.route {
			p.route, p.lastStart, p.synced = 0, time.Time{}, false
		}
	}
}
