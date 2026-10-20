package cacheproxy

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// BlockView contains observation data only. No URLs, file paths, source
// pointers, proxy configuration or task handles cross this boundary.
type BlockView struct {
	ID              string `json:"id"`
	Resource        string `json:"resource"`
	Block           int64  `json:"block"`
	TotalBlocks     int64  `json:"totalBlocks"` // complete video; zero if its size is unknown
	Bytes           int64  `json:"bytes"`
	Title           string `json:"title"`
	SongID          string `json:"songID"`
	At              int64  `json:"at"` // display anchor, explicitly qualified by TimeBasis
	TimeBasis       string `json:"timeBasis"`
	NeedBy          int64  `json:"needBy"`
	Deadline        int64  `json:"deadline"`
	ProtectionStart int64  `json:"protectionStart"`
	ProtectionUntil int64  `json:"protectionUntil"`
	Protected       bool   `json:"protected"`
	InUse           bool   `json:"inUse"`
	Sources         int    `json:"sources"`
	Readers         int    `json:"readers"`
	State           string `json:"state"`
	WindowClosed    bool   `json:"windowClosed"`
	Opportunistic   bool   `json:"opportunistic"`
	Eligible        bool   `json:"eligible"`
}

type BlockSourceView struct {
	ID           uint64 `json:"id"`
	Kind         string `json:"kind"`
	SongID       string `json:"songID"`
	Title        string `json:"title"`
	Position     int    `json:"position"`
	NeedBy       int64  `json:"needBy"`
	ExpiresAt    int64  `json:"expiresAt"` // fixed handoff expiry only, not a synthetic lease renewal
	WindowClosed bool   `json:"windowClosed"`
}

type BlockDetail struct {
	Block   BlockView         `json:"block"`
	Sources []BlockSourceView `json:"sources"`
}

// One source can refer to multiple resources during a transition. Group by both
// identities, and send block references rather than repeating block properties.
type BlockDemandView struct {
	ID          string   `json:"id"`
	SourceID    uint64   `json:"sourceID"`
	Kind        string   `json:"kind"`
	SongID      string   `json:"songID"`
	Title       string   `json:"title"`
	Position    int      `json:"position"`
	Resource    string   `json:"resource"`
	TotalBlocks int64    `json:"totalBlocks"` // complete video, not the remaining demand span; zero if unknown
	Blocks      []string `json:"blocks"`
}

// Immutable once published. Detail is kept server-side and sent only for a
// hovered/selected block. All observation state dies with the last subscriber.
type BlockSnapshot struct {
	At              int64                      `json:"at"`
	Capacity        int                        `json:"capacity"`
	Used            int                        `json:"used"`
	Active          int                        `json:"active"`
	Open            int                        `json:"open"`
	PrepareAheadMS  int64                      `json:"prepareAheadMS"`
	DownloadGraceMS int64                      `json:"downloadGraceMS"`
	Blocks          map[string]BlockView       `json:"-"`
	Details         map[string]BlockDetail     `json:"-"`
	Demands         map[string]BlockDemandView `json:"-"`
}

type blockObserver struct {
	s         *Server
	ctx       context.Context
	cancel    context.CancelFunc
	wake      chan struct{}
	done      chan struct{}
	refs      int // Server.mu
	mu        sync.Mutex
	snapshot  *BlockSnapshot
	changed   chan struct{}
	closed    bool
	sourceIDs map[blockSource]uint64 // worker only; pruned each capture
	videos    map[string]blockVideo  // worker-only display metadata, pruned each capture
	sequence  uint64
}

type blockVideo struct {
	SongID string
	Title  string
}

type BlockSubscription struct {
	observer *blockObserver
	once     sync.Once
}

func (s *Server) notifyBlockObservation() {
	if o := s.blockObserver.Load(); o != nil {
		select {
		case o.wake <- struct{}{}:
		default:
		}
	}
}

// ObserveBlocks does not start the engine or perform I/O. The idle cost in
// existing mutation paths is a nil pointer check; no observer work exists.
func (s *Server) ObserveBlocks() (*BlockSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, context.Canceled
	}
	o := s.blockObserver.Load()
	if o == nil {
		ctx, cancel := context.WithCancel(s.ctx)
		o = &blockObserver{s: s, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), changed: make(chan struct{}), sourceIDs: make(map[blockSource]uint64)}
		s.blockObserver.Store(o)
		go o.run()
	}
	o.refs++
	return &BlockSubscription{observer: o}, nil
}

func (s *BlockSubscription) Snapshot() (*BlockSnapshot, <-chan struct{}, bool) {
	o := s.observer
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.snapshot, o.changed, o.closed
}

func (s *BlockSubscription) Close() {
	s.once.Do(func() {
		o := s.observer
		o.s.mu.Lock()
		o.refs--
		last := o.refs == 0
		if last {
			o.s.blockObserver.CompareAndSwap(o, nil)
			o.cancel()
		}
		o.s.mu.Unlock()
		if last {
			<-o.done
		}
	})
}

func (o *blockObserver) run() {
	unwatch := o.s.scheduler.WatchChanges(o.wake)
	defer func() {
		unwatch()
		o.mu.Lock()
		o.closed = true
		o.snapshot = nil
		o.sourceIDs = nil
		o.videos = nil
		close(o.changed)
		o.mu.Unlock()
		close(o.done)
	}()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	var previous *BlockSnapshot
	for o.ctx.Err() == nil {
		snapshot, next := o.capture(previous)
		if o.ctx.Err() != nil {
			return
		}
		o.mu.Lock()
		o.snapshot = snapshot
		close(o.changed)
		o.changed = make(chan struct{})
		o.mu.Unlock()
		previous = snapshot
		timer.Stop()
		var due <-chan time.Time
		if !next.IsZero() {
			timer.Reset(max(0, time.Until(next)))
			due = timer.C
		}
		select {
		case <-o.ctx.Done():
			return
		case <-due:
		case <-o.wake:
		}
		// Changes and time boundaries share one event-triggered batch window.
		// Nearby expiries must not bypass it and repeatedly rebuild a snapshot.
		// This timer exists only after a trigger; idle state is never sampled.
		timer.Stop()
		timer.Reset(50 * time.Millisecond)
		select {
		case <-o.ctx.Done():
			return
		case <-timer.C:
		}
		select {
		case <-o.wake:
		default:
		}
	}
}

func timeMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
func blockViewID(id useBlockID) string { return id.key + "/" + strconv.FormatInt(id.block, 10) }

func (o *blockObserver) capture(previous *BlockSnapshot) (*BlockSnapshot, time.Time) {
	s := o.s
	now := time.Now()
	ahead := s.cfg.PrepareAhead
	if ahead <= 0 {
		ahead = blocksched.DefaultPrepareAhead
	}
	result := &BlockSnapshot{At: now.UnixMilli(), Capacity: maxUseBlocks, PrepareAheadMS: ahead.Milliseconds(), DownloadGraceMS: blocksched.DownloadGrace.Milliseconds()}
	var next time.Time
	advance := func(at time.Time) {
		if at.After(now) {
			next = earlier(next, at)
		}
	}
	// Projection uses the canonical aggregation. It never renews a source or
	// retains files. No disk/database/network I/O happens under either lock.
	s.mu.Lock()
	projected, _ := s.projectUseLocked("", now)
	result.Used = len(s.useBlocks)
	result.Blocks = make(map[string]BlockView, len(projected))
	sizes := make(map[string]int64)
	sequential := make(map[string]int64)
	for key, f := range s.flights {
		sizes[key] = f.size
		if f.sequentialCtx != nil && f.sequentialCtx.Err() == nil && f.progress != nil {
			f.progress.mu.Lock()
			sequential[key] = f.progress.bytes / rangeBlockSize
			f.progress.mu.Unlock()
		}
	}
	for key, u := range s.resourceUses {
		sizes[key] = u.video.size
	}
	// Copy mutable source metadata once per source. The projected needs are
	// already detached values; source pointers are used only as identities below.
	sourceViews := make(map[blockSource]BlockSourceView)
	for id, needs := range projected {
		b := BlockView{ID: blockViewID(id), Resource: id.key, Block: id.block, InUse: s.useBlocks[id] > 0, State: "waiting"}
		result.Blocks[b.ID] = b
		for source := range needs {
			if _, copied := sourceViews[source]; copied {
				continue
			}
			v := BlockSourceView{Kind: source.kind}
			switch origin := source.id.(type) {
			case *downloadDemand:
				v.SongID = origin.song
				if origin.clock != nil {
					sizes[id.key] = origin.clock.media.size
				}
				if q := s.prefetchSources[origin.song]; q != nil {
					v.Title = q.title
				}
			case *queueSource:
				v.SongID, v.Title, v.Position = origin.song, origin.title, origin.position+1
				sizes[id.key] = origin.timing.size
			case *useHandoff:
				v.ExpiresAt = timeMS(origin.until)
				sizes[id.key] = origin.clock.media.size
				advance(origin.until)
			}
			sourceViews[source] = v
		}
	}
	s.retentionMu.Lock()
	// Include locally available grace tails even though they no longer consume
	// any of the 4096 usage slots. Pre-subscription history is not invented.
	for key, blocks := range s.protection {
		for block, p := range blocks {
			id := useBlockID{key, block}
			name := blockViewID(id)
			b, exists := result.Blocks[name]
			if !exists && p.until.After(now) {
				b = BlockView{ID: name, Resource: key, Block: block, State: "protected", TimeBasis: "ended", At: timeMS(p.until.Add(-localProtectionGrace))}
				if previous != nil {
					if old, ok := previous.Blocks[name]; ok {
						b.Title, b.SongID = old.Title, old.SongID
						if old.At != 0 && old.TimeBasis != "ended" {
							b.At, b.TimeBasis = old.At, "last-need-by"
						}
					}
				}
				result.Blocks[name] = b
			}
		}
	}
	for name, b := range result.Blocks {
		p := s.protection[b.Resource][b.Block]
		b.ProtectionStart, b.ProtectionUntil, b.Protected = timeMS(p.start), timeMS(p.until), p.active(now)
		advance(p.start)
		advance(p.until)
		available := false
		if u := s.resourceUses[b.Resource]; b.SongID == "" && u != nil {
			b.SongID = u.video.songID
		}
		if retained, ok := s.retained[b.Resource]; ok {
			available = true
			sizes[b.Resource] = retained.size
			if b.SongID == "" && retained.songID > 0 {
				b.SongID = strconv.FormatInt(retained.songID, 10)
			}
		}
		if local := s.localResources[b.Resource]; local != nil {
			available = available || local.has(b.Block)
			sizes[b.Resource] = local.video.size
			if b.SongID == "" {
				b.SongID = local.video.songID
			}
		}
		if !b.InUse && (!available || !b.Protected) {
			delete(result.Blocks, name)
			continue
		}
		if available && b.InUse {
			b.State = "available"
		}
		if current, ok := sequential[b.Resource]; ok && b.InUse && !available && current == b.Block {
			b.State = "downloading"
		}
		b.Bytes = blockBytes(sizes[b.Resource], b.Block)
		if size := sizes[b.Resource]; size > 0 {
			b.TotalBlocks = (size-1)/rangeBlockSize + 1
		}
		result.Blocks[name] = b
	}
	s.retentionMu.Unlock()
	s.mu.Unlock()
	// Aggregate and format only detached values outside the business locks.
	// Preserve source identity/title precedence over the resource fallback.
	result.Details = make(map[string]BlockDetail, len(result.Blocks))
	for id, needs := range projected {
		name := blockViewID(id)
		b, exists := result.Blocks[name]
		if !exists {
			continue
		}
		use := aggregateUse(needs, now)
		b.NeedBy, b.Deadline = timeMS(use.need.ReadyBy), timeMS(blocksched.Deadline(use.need.ReadyBy))
		b.WindowClosed, b.Opportunistic = use.need.WindowClosed, use.need.Opportunistic
		b.At, b.TimeBasis = b.NeedBy, "need-by"
		if b.At == 0 {
			b.TimeBasis = "untimed"
		}
		b.Eligible = !use.need.WindowClosed && (use.need.Opportunistic || !use.need.ReadyBy.IsZero() && !use.need.ReadyBy.Add(-ahead).After(now) && !blocksched.Expired(use.need.ReadyBy, now))
		if !use.need.WindowClosed {
			advance(use.need.ReadyBy.Add(-ahead))
		}
		advance(blocksched.Deadline(use.need.ReadyBy))
		sources := make([]BlockSourceView, 0, len(needs))
		for source, p := range needs {
			if o.sourceIDs[source] == 0 {
				o.sequence++
				o.sourceIDs[source] = o.sequence
			}
			v := sourceViews[source]
			v.ID, v.NeedBy, v.WindowClosed = o.sourceIDs[source], timeMS(p.need.ReadyBy), p.need.WindowClosed
			sources = append(sources, v)
		}
		sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
		b.Sources = len(sources)
		var video blockVideo
		for _, v := range sources {
			video = chooseBlockVideo(video, blockVideo{SongID: v.SongID, Title: v.Title}, o.videos[b.Resource].SongID)
		}
		if video.SongID == "" {
			video = blockVideo{SongID: b.SongID, Title: b.Title}
		}
		b.SongID, b.Title = video.SongID, video.Title
		result.Blocks[name] = b
		result.Details[name] = BlockDetail{Block: b, Sources: sources}
	}
	for source := range o.sourceIDs {
		if _, seen := sourceViews[source]; !seen {
			delete(o.sourceIDs, source)
		}
	}
	// Scheduler commands must never run under Server.mu/retentionMu. Actual
	// reads belong to the scheduler and cannot be inferred from GET presence.
	scheduling := s.scheduler.Observation()
	result.Active, result.Open = scheduling.Active, scheduling.Open
	for key, task := range scheduling.Tasks {
		resource, _, block, valid := parseDownloadTaskKey(key)
		if !valid {
			continue
		}
		name := blockViewID(useBlockID{resource, block})
		b, ok := result.Blocks[name]
		if !ok {
			continue
		}
		b.Readers = task.Readers
		if b.Readers > 0 {
			b.Eligible = true
		}
		if b.State != "available" && b.State != "protected" {
			if task.Running {
				b.State = "downloading"
			}
			if task.Retiring {
				b.State = "stopping"
			}
		}
		b.Deadline = timeMS(task.Deadline)
		result.Blocks[name] = b
	}
	o.labelVideos(result)
	result.Demands = make(map[string]BlockDemandView)
	for name, b := range result.Blocks {
		d := result.Details[name]
		d.Block = b
		if d.Sources == nil {
			d.Sources = []BlockSourceView{}
		}
		for i := range d.Sources {
			source := &d.Sources[i]
			if source.SongID == "" {
				source.SongID = b.SongID
			}
			if source.Title == "" && source.SongID == b.SongID {
				source.Title = b.Title
			}
		}
		result.Details[name] = d
		for _, source := range d.Sources {
			id := strconv.FormatUint(source.ID, 10) + "/" + b.Resource
			demand, exists := result.Demands[id]
			if !exists {
				demand = BlockDemandView{ID: id, SourceID: source.ID, Kind: source.Kind, SongID: source.SongID, Title: source.Title, Position: source.Position, Resource: b.Resource}
				demand.TotalBlocks = b.TotalBlocks
				if demand.SongID == "" {
					demand.SongID = b.SongID
				}
				if demand.Title == "" && demand.SongID == b.SongID {
					demand.Title = b.Title
				}
			}
			demand.Blocks = append(demand.Blocks, name)
			result.Demands[id] = demand
		}
	}
	for _, demand := range result.Demands {
		sort.Slice(demand.Blocks, func(i, j int) bool {
			return result.Blocks[demand.Blocks[i]].Block < result.Blocks[demand.Blocks[j]].Block
		})
	}
	return result, next
}

// Prefer the cached identity when still present, otherwise lexical order.
// Keep each title with its identity, regardless of source or map order.
func chooseBlockVideo(current, candidate blockVideo, preferred string) blockVideo {
	if candidate.SongID != "" && candidate.SongID != current.SongID &&
		(current.SongID == "" || candidate.SongID == preferred || current.SongID != preferred && candidate.SongID < current.SongID) {
		current = blockVideo{SongID: candidate.SongID}
	}
	if candidate.SongID == current.SongID && candidate.Title != "" && (current.Title == "" || candidate.Title < current.Title) {
		current.Title = candidate.Title
	}
	return current
}

// Handoffs intentionally carry scheduling data only. Recover their display
// identity from existing resource metadata and the catalog, outside business
// locks and only while someone observes. Cache misses too: events do not cause
// repeated catalog reads for the same visible video.
func (o *blockObserver) labelVideos(snapshot *BlockSnapshot) {
	videos := make(map[string]blockVideo)
	for _, b := range snapshot.Blocks {
		videos[b.Resource] = chooseBlockVideo(videos[b.Resource], blockVideo{SongID: b.SongID, Title: b.Title}, o.videos[b.Resource].SongID)
	}
	type lookup struct {
		Resource string `json:"resource"`
		SongID   string `json:"songID"`
	}
	var missing []lookup
	for key, v := range videos {
		old, exists := o.videos[key]
		if v.SongID == "" && old.SongID != "" {
			v = old // A handoff without identity keeps its last known label.
		} else if v.SongID == old.SongID && v.Title == "" {
			v.Title = old.Title
		}
		videos[key] = v
		if v.Title == "" && (!exists || old.SongID != v.SongID) {
			missing = append(missing, lookup{key, v.SongID})
		}
	}
	if len(missing) > 0 && o.s.usage != nil {
		data, _ := json.Marshal(missing)
		ctx, cancel := context.WithTimeout(o.ctx, 2*time.Second)
		defer cancel()
		rows, err := o.s.usage.db.QueryContext(ctx, `
			WITH requested AS (
				SELECT json_extract(value,'$.resource') resource,
				       json_extract(value,'$.songID') song_id FROM json_each(?)
			)
			SELECT r.resource, COALESCE(CAST(s.song_id AS TEXT),r.song_id,''),
			       substr(COALESCE(NULLIF(s.name,''),s.composed_title,''),1,300)
			FROM requested r LEFT JOIN songs s ON s.song_id=COALESCE(NULLIF(r.song_id,''),
				(SELECT sm.song_id FROM song_media sm WHERE sm.md5=r.resource ORDER BY sm.song_id LIMIT 1))`, string(data))
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var key string
				var v blockVideo
				if rows.Scan(&key, &v.SongID, &v.Title) == nil {
					videos[key] = v
				}
			}
		}
	}
	o.videos = videos
	for id, b := range snapshot.Blocks {
		v := videos[b.Resource]
		b.SongID, b.Title = v.SongID, v.Title
		snapshot.Blocks[id] = b
	}
}
