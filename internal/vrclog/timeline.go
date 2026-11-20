package vrclog

import (
	"math"
	"time"
)

// Keep recent associations for callbacks from replaced or ended previews.
// The current preview keeps its identity until a lifecycle event replaces it.
const previewLogWindow = 90 * time.Second

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

type previewAssociation struct {
	songID int64
	until  time.Time
	ended  bool
}

type PlayerTimeline struct {
	Role                 PlayerRole
	Anchor               PlaybackAnchor
	route                int64
	lastEvent, lastStart time.Time
	synced               bool // The current route has supplied a synchronization.
	previews             map[int64]*previewAssociation
	activePreview        *previewAssociation
	previewCallbacks     *previewAssociation // Last identified source for unlabelled callbacks.
	previewSeek          *PlaybackEvent
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
	// A local seek can identify a following sync even if it omits the threshold.
	// A sync with the room threshold keeps its source independently of the seek.
	seek := p.previewSeek
	p.previewSeek = nil
	if e.Kind == "seek" {
		if p.previewCallbacks != nil {
			p.previewSeek = e
		}
		return
	}
	if (e.Kind == "sync" || e.Kind == "local-sync") && seek != nil && e.At.Equal(seek.At) && math.Abs(e.Position-seek.Position) <= 0.01 {
		return
	}

	switch e.Kind {
	case "preview":
		p.rememberPreview(e)
		return
	case "room-selection":
		delete(p.previews, e.SongID)
		return
	case "room-route":
		delete(p.previews, e.SongID)
	default:
		if p.consumePreviewCallback(e) {
			return
		}
	}
	// A stop in file order still ends playback after a delayed read.
	if e.Kind != "end" && e.Kind != "stop" && e.Kind != "room-end" && received.Sub(e.At) > 4*time.Second {
		if e.Owner != nil && (*e.Owner != (p.Role == Owner)) {
			p.Role = UnknownRole
		}
		switch e.Kind {
		case "route", "room-route":
			// An untrusted route cannot identify the next anonymous sync.
			// The established room clock continues until a playback fact replaces it.
			p.route, p.lastStart, p.synced = 0, time.Time{}, false
		case "start":
			p.Anchor = PlaybackAnchor{}
			p.route, p.lastStart, p.synced = e.SongID, e.At, false
			p.lastEvent = e.At
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
	case "route", "room-route":
		if e.Kind == "room-route" && e.SongID > 0 && e.SongID != p.Anchor.SongID {
			p.Anchor = PlaybackAnchor{}
		}
		p.lastStart, p.synced = time.Time{}, false
		p.route = e.SongID
		if e.SongID > 0 {
			p.previewCallbacks = nil
		}
	case "start":
		if e.SongID == 0 {
			p.Anchor = PlaybackAnchor{}
			p.route, p.lastStart, p.synced = 0, time.Time{}, false
			return
		}
		p.previewCallbacks = nil
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
	case "sync", "room-sync", "local-sync":
		if p.route <= 0 {
			return
		}
		started := p.Anchor.Started
		if p.Anchor.SongID != p.route || started.IsZero() || (p.lastStart.IsZero() && !p.synced) {
			started = e.At
		}
		p.synced = true
		p.Anchor = PlaybackAnchor{Started: started, SongID: p.route, At: e.At, Position: e.Position}
	case "end", "stop", "room-end":
		if e.SongID == 0 || e.SongID == p.Anchor.SongID {
			p.Anchor = PlaybackAnchor{}
		}
		if e.SongID == 0 || e.SongID == p.route {
			p.route, p.lastStart, p.synced = 0, time.Time{}, false
		}
	}
}

func (p *PlayerTimeline) rememberPreview(e *PlaybackEvent) {
	if p.previews == nil {
		p.previews = make(map[int64]*previewAssociation)
	}
	for song, preview := range p.previews {
		if e.At.After(preview.until) {
			delete(p.previews, song)
		}
	}
	preview := &previewAssociation{songID: e.SongID, until: e.At.Add(previewLogWindow)}
	p.previews[e.SongID] = preview
	p.activePreview = preview
	p.previewCallbacks = preview
}

func (p *PlayerTimeline) previewApplies(preview *previewAssociation, at time.Time) bool {
	return preview != nil && (preview == p.activePreview || !at.After(preview.until))
}

func (p *PlayerTimeline) consumePreviewCallback(e *PlaybackEvent) bool {
	switch e.Kind {
	case "role", "transport", "room-end", "room-sync":
		return false
	case "local-sync":
		// The local threshold also occurs during some room-player starts.
		return p.activePreview != nil || p.previewApplies(p.previewCallbacks, e.At)
	}
	// A sync or stop with no song ID cannot be attributed to the room
	// while the most recent identified player is the preview.
	if e.SongID == 0 {
		return p.previewApplies(p.previewCallbacks, e.At)
	}
	preview := p.previews[e.SongID]
	if e.Kind == "end" || e.Kind == "stop" {
		// A room selection can claim the named route while the local preview
		// keeps playing the same song.
		if p.activePreview != nil && p.activePreview.songID == e.SongID {
			preview = p.activePreview
		}
		if preview == nil || (preview != p.previewCallbacks && !p.previewApplies(preview, e.At)) {
			return false
		}
		// An ordinary same-song end could belong to either player.
		// A confirmed room end is handled independently of this preview.
		if !e.At.Before(preview.until.Add(-previewLogWindow)) &&
			e.SongID != p.Anchor.SongID && e.SongID != p.route {
			preview.ended = true
			if p.activePreview == preview {
				p.activePreview = nil
			}
			if p.previewCallbacks == preview {
				p.previewCallbacks = nil
			}
		}
		return true
	}
	if !p.previewApplies(preview, e.At) {
		return false
	}
	if !preview.ended && (e.Kind == "route" || e.Kind == "start") &&
		(p.previewCallbacks == nil || preview.until.After(p.previewCallbacks.until)) {
		p.previewCallbacks = preview
	}
	return true
}
