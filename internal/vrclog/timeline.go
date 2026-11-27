package vrclog

import (
	"math"
	"time"
)

// Keep recent associations for callbacks from replaced or ended previews.
// The current preview keeps its identity until a lifecycle event replaces it.
const previewLogWindow = 90 * time.Second

const playbackFreshness = 4 * time.Second

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
	external     uint64
	At           time.Time
	Started      time.Time // Identity of the first start/sync in this playback.
	Position     float64
	CatalogStart bool
}

// IsExternal reports a start-only queue estimate without exposing media identity.
func (a PlaybackAnchor) IsExternal() bool { return a.external != 0 }

type mediaID struct {
	song     int64
	external uint64
}

func (m mediaID) known() bool { return m.song != 0 || m.external != 0 }

func (a PlaybackAnchor) media() mediaID { return mediaID{a.SongID, a.external} }

type previewAssociation struct {
	songID int64
	until  time.Time
	ended  bool
}

type PlayerTimeline struct {
	Role                 PlayerRole
	Anchor               PlaybackAnchor
	route                mediaID
	lastEvent, lastStart time.Time
	naturalTransitionAt  time.Time
	selectionPending     bool
	synced               bool // The current route has supplied a synchronization.
	previews             map[int64]*previewAssociation
	activePreview        *previewAssociation
	previewCallbacks     *previewAssociation // Last identified source for unlabelled callbacks.
	previewSeek          *PlaybackEvent
}

type PlaybackChange struct {
	SelectedSong *int64 // nil: no selection event; zero: no identifiable queue member.
	Stopped      bool   // The room stopped without continuing into a selection.
}

// QueueRoomSong returns the catalog route following this snapshot in the
// same read. A local load may separate them but cannot confirm a room choice.
func (p *PlayerTimeline) QueueRoomSong(snapshot Event, following []Event, received time.Time) int64 {
	at := snapshot.queueAt
	if at.IsZero() || at.Before(p.lastEvent) || at.After(received) || received.Sub(at) > playbackFreshness {
		return 0
	}
	for _, event := range following {
		e := event.Playback
		if e == nil || !e.At.Equal(at) {
			break
		}
		if e.Kind == "room-route" {
			return e.SongID
		}
		if e.Kind != "route" {
			break
		}
	}
	return 0
}

// Observe is called in file order by the single log reader. Route lines
// identify anonymous sync callbacks but cannot themselves start playback.
func (p *PlayerTimeline) Observe(event Event, received time.Time) (change PlaybackChange) {
	if event.Reset {
		*p = PlayerTimeline{}
		return
	}
	e := event.Playback
	if e == nil || e.At.Before(p.lastEvent) || e.At.After(received) {
		return
	}
	previousAnchor := p.Anchor
	media := mediaID{e.SongID, e.external}
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
		p.selectionPending = true
		change.SelectedSong = &e.SongID
		return
	case "room-stop-request":
		p.selectionPending = false
		p.naturalTransitionAt = time.Time{}
		return
	default:
		if p.consumePreviewCallback(e) {
			return
		}
	}
	// A stop in file order still ends playback after a delayed read.
	if e.Kind != "end" && e.Kind != "stop" && e.Kind != "room-end" && e.Kind != "room-stop" && received.Sub(e.At) > playbackFreshness {
		if e.Owner != nil && (*e.Owner != (p.Role == Owner)) {
			p.Role = UnknownRole
		}
		switch e.Kind {
		case "route", "room-route":
			// An untrusted route cannot identify the next anonymous sync.
			// The established room clock continues until a playback fact replaces it.
			p.route, p.lastStart, p.synced = mediaID{}, time.Time{}, false
		case "start":
			p.lastEvent = e.At
			if e.external != 0 && media == p.Anchor.media() {
				return // A duplicate cannot invalidate the trusted first start.
			}
			p.Anchor = PlaybackAnchor{}
			p.route, p.lastStart, p.synced = mediaID{song: e.SongID}, e.At, false
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
	current := p.Anchor.media()
	switch e.Kind {
	case "route", "room-route":
		if e.Kind == "room-route" {
			delete(p.previews, e.SongID)
			if media.known() && media != current {
				p.Anchor = PlaybackAnchor{}
			}
		}
		p.lastStart, p.synced = time.Time{}, false
		// A local external load (possibly a retry) cannot select a new room video.
		if e.Kind == "route" && e.external != 0 {
			media.external = p.route.external
		}
		p.route = media
		if e.SongID > 0 || (e.Kind == "room-route" && e.external != 0) {
			p.previewCallbacks = nil
		}
	case "start":
		if e.external != 0 {
			if media != current {
				p.Anchor = PlaybackAnchor{external: e.external, Started: e.At, At: e.At}
			}
			p.route, p.lastStart, p.synced = media, e.At, false
			break
		}
		if e.SongID == 0 {
			p.Anchor = PlaybackAnchor{}
			p.route, p.lastStart, p.synced = mediaID{}, time.Time{}, false
			return
		}
		p.previewCallbacks = nil
		if p.route == media && p.lastStart.Equal(e.At) && (p.Anchor.SongID != 0 || p.Role != Owner) {
			return
		}
		if p.route == media && p.synced && (p.lastStart.IsZero() || p.Anchor.At.Equal(e.At)) {
			if !p.lastStart.IsZero() {
				p.Anchor.Started = e.At
			}
			p.lastStart = e.At
			return
		}
		p.route, p.lastStart, p.synced = media, e.At, false
		p.Anchor = PlaybackAnchor{}
		if p.Role == Owner {
			p.Anchor = PlaybackAnchor{SongID: e.SongID, Started: e.At, At: e.At, CatalogStart: true}
		}
	case "sync", "room-sync", "local-sync":
		if p.route.song <= 0 {
			return
		}
		started := p.Anchor.Started
		if p.Anchor.SongID != p.route.song || started.IsZero() || (p.lastStart.IsZero() && !p.synced) {
			started = e.At
		}
		p.synced = true
		p.Anchor = PlaybackAnchor{Started: started, SongID: p.route.song, At: e.At, Position: e.Position}
	case "end", "stop", "room-end", "room-stop":
		if !media.known() || media == current {
			if e.Kind == "stop" || e.Kind == "room-stop" {
				// The old player's stop callback can cross a log second after
				// natural end or selection of the next song.
				age := e.At.Sub(p.naturalTransitionAt)
				continuedNaturalEnd := e.Kind == "room-stop" && !current.known() && age >= 0 && age <= time.Second
				change.Stopped = !p.selectionPending && !continuedNaturalEnd
			} else if current.known() {
				p.naturalTransitionAt = e.At
			}
			p.Anchor = PlaybackAnchor{}
		} else if e.Kind == "stop" && media == p.route && !current.known() {
			change.Stopped = !p.selectionPending
		}
		if !media.known() || media == p.route {
			p.route, p.lastStart, p.synced = mediaID{}, time.Time{}, false
		}
	}
	active := p.Anchor.media().known()
	if active || change.Stopped {
		p.naturalTransitionAt = time.Time{}
	}
	newPlayback := active && (p.Anchor.media() != previousAnchor.media() || p.Anchor.Started != previousAnchor.Started)
	if change.Stopped || newPlayback {
		p.selectionPending = false
	}
	return
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
	case "role", "transport", "room-end", "room-stop", "room-sync", "room-route":
		return false
	case "local-sync":
		// The local threshold also occurs during some room-player starts.
		return p.activePreview != nil || p.previewApplies(p.previewCallbacks, e.At)
	}
	// A named external callback can belong to the selected or playing room
	// video. Unattributed callbacks still follow the most recent preview.
	if e.SongID == 0 {
		if e.external != 0 && (e.external == p.route.external || e.external == p.Anchor.external) {
			return false
		}
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
			e.SongID != p.Anchor.SongID && e.SongID != p.route.song {
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
