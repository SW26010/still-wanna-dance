package cacheproxy

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/blocksched"
)

// BlockView contains observation data only. No URLs, file paths, source
// pointers, proxy configuration or task handles cross this boundary.
type BlockView struct {
	ID              string `json:"id"`
	Resource        string `json:"resource"`
	Block           int64  `json:"block"`
	ByteOffset      int64  `json:"byteOffset"`
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

// Immutable once published. Compact source associations stay server-side;
// Detail expands only a hovered/selected block. All observation state dies
// with the last subscriber.
type BlockSnapshot struct {
	At              int64                      `json:"at"`
	Capacity        int                        `json:"capacity"`
	Used            int                        `json:"used"`
	Active          int                        `json:"active"`
	Open            int                        `json:"open"`
	PrepareAheadMS  int64                      `json:"prepareAheadMS"`
	DownloadGraceMS int64                      `json:"downloadGraceMS"`
	Blocks          map[string]BlockView       `json:"-"`
	Demands         map[string]BlockDemandView `json:"-"`
	sources         []BlockSourceView
	needs           []blockDetailNeed
	sourceRanges    map[string]blockSourceRange
}

type blockDetailNeed struct {
	source       int
	needBy       int64
	windowClosed bool
}

type blockSourceRange struct{ first, last int }

// Detail expands only the requested block from this immutable snapshot. It
// never consults live sources, and each caller owns its returned source slice.
func (s *BlockSnapshot) Detail(id string) (BlockDetail, bool) {
	b, exists := s.Blocks[id]
	if !exists {
		return BlockDetail{}, false
	}
	r := s.sourceRanges[id]
	d := BlockDetail{Block: b, Sources: make([]BlockSourceView, 0, r.last-r.first)}
	for _, need := range s.needs[r.first:r.last] {
		v := blockSourceLabel(s.sources[need.source], b)
		v.NeedBy, v.WindowClosed = need.needBy, need.windowClosed
		d.Sources = append(d.Sources, v)
	}
	slices.SortFunc(d.Sources, func(a, b BlockSourceView) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return d, true
}

func blockSourceLabel(source BlockSourceView, b BlockView) BlockSourceView {
	if source.SongID == "" {
		source.SongID = b.SongID
	}
	if source.Title == "" && source.SongID == b.SongID {
		source.Title = b.Title
	}
	return source
}

type blockObserver struct {
	s            *Server
	ctx          context.Context
	cancel       context.CancelFunc
	wake         chan struct{}
	done         chan struct{}
	refs         int // Server.mu
	mu           sync.Mutex
	snapshot     *BlockSnapshot
	changed      chan struct{}
	closed       bool
	sourceIDs    map[blockSource]uint64 // worker only; pruned each capture
	videos       map[string]blockVideo  // worker-only display metadata, pruned each capture
	sequence     uint64
	speedBytes   atomic.Int64
	speedWake    chan struct{}
	speedChanged chan struct{} // mu; independent of block snapshots
	speed        float64       // mu
	cacheWake    chan struct{}
	cacheChanged chan struct{} // mu; independent of block snapshots and speed
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
		o = &blockObserver{s: s, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), changed: make(chan struct{}), sourceIDs: make(map[blockSource]uint64), speedWake: make(chan struct{}, 1), speedChanged: make(chan struct{})}
		o.cacheWake, o.cacheChanged = make(chan struct{}, 1), make(chan struct{})
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
	var metrics sync.WaitGroup
	metrics.Go(o.runSpeed)
	metrics.Go(o.runBlockCache)
	defer func() {
		o.cancel()
		metrics.Wait()
		unwatch()
		o.mu.Lock()
		o.closed = true
		o.snapshot = nil
		o.sourceIDs = nil
		o.videos = nil
		close(o.changed)
		close(o.speedChanged)
		close(o.cacheChanged)
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

// Count network reads once at their shared flight, including short-lived tasks
// and retries. Local reuse never enters progressReader. No observer means no
// display counters, timers or notifications; the optional experiment hook stays
// independent of UI subscriptions.
func (s *Server) observeDownloadBytes(n int64) {
	if s.cfg.ObserveDownloadBytes != nil {
		s.cfg.ObserveDownloadBytes(n)
	}
	if o := s.blockObserver.Load(); o != nil {
		o.speedBytes.Add(n)
		select {
		case o.speedWake <- struct{}{}:
		default:
		}
	}
}

func (s *BlockSubscription) DownloadSpeed() (float64, <-chan struct{}) {
	o := s.observer
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.speed, o.speedChanged
}

// Leave bytes accumulated when the clock cannot define a sample interval.
func (o *blockObserver) takeSpeedSample(elapsed time.Duration) (int64, float64, bool) {
	if elapsed <= 0 {
		return 0, 0, false
	}
	bytes := o.speedBytes.Swap(0)
	return bytes, bytesPerSecond(bytes, elapsed), true
}

// Byte arrivals arm a one-second sample. One empty sample resets the speed to
// zero and disarms the timer. Speed samples never capture or wake block state.
func (o *blockObserver) runSpeed() {
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	var due <-chan time.Time
	var last time.Time
	for {
		// While sampling, leave the wake queued so byte reads coalesce without
		// scheduling the worker again before the next sample.
		wake := o.speedWake
		if due != nil {
			wake = nil
		}
		select {
		case <-o.ctx.Done():
			return
		case <-wake:
			if due == nil {
				last = time.Now()
				timer.Reset(time.Second)
				due = timer.C
			}
		case <-due:
			select {
			case <-o.speedWake:
			default:
			}
			now := time.Now()
			bytes, rate, valid := o.takeSpeedSample(now.Sub(last))
			if !valid {
				timer.Reset(time.Second)
				continue
			}
			o.mu.Lock()
			if rate != o.speed {
				o.speed = rate
				close(o.speedChanged)
				o.speedChanged = make(chan struct{})
			}
			o.mu.Unlock()
			last = now
			if bytes > 0 {
				timer.Reset(time.Second)
			} else {
				due = nil
			}
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

// The locked capture keeps one metadata record per source and flat associations.
// It copies values only; identities remain worker-local and never enter a snapshot.
type observedSource struct {
	identity blockSource
	view     BlockSourceView
	need     blocksched.Need
}
type observedNeed struct {
	block, source                  int
	ready                          time.Time
	windowClosed, protectionClosed bool
}
type observedBlock struct {
	id               useBlockID
	name             string
	inUse, available bool
	protection       blockProtection
	size             int64
	song             string
	retainedSong     int64
}
type blockObservationInput struct {
	used    int
	blocks  []observedBlock
	sources []observedSource
	needs   []observedNeed
}

func (o *blockObserver) captureInput(now time.Time) blockObservationInput {
	s := o.s
	s.mu.Lock()
	associations := 0
	for _, refs := range s.useBlocks {
		associations += refs
	}
	input := blockObservationInput{
		used:   len(s.useBlocks),
		blocks: make([]observedBlock, 0, len(s.useBlocks)),
		needs:  make([]observedNeed, 0, associations),
	}
	indexes := make(map[useBlockID]int, len(s.useBlocks))
	sourceIndexes := make(map[blockSource]int)
	sizes := make(map[string]int64)
	for key, f := range s.flights {
		sizes[key] = f.size
	}
	for key, u := range s.resourceUses {
		sizes[key] = u.video.size
	}
	addBlock := func(id useBlockID) int {
		if index, exists := indexes[id]; exists {
			return index
		}
		index := len(input.blocks)
		indexes[id] = index
		input.blocks = append(input.blocks, observedBlock{id: id, inUse: s.useBlocks[id] > 0})
		return index
	}
	s.visitUseLocked(allUseBlocks(""), now, func(id useBlockID, source blockSource, p plannedNeed) {
		index, exists := sourceIndexes[source]
		if !exists {
			index = len(input.sources)
			sourceIndexes[source] = index
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
			}
			input.sources = append(input.sources, observedSource{source, v, p.need})
		}
		input.needs = append(input.needs, observedNeed{addBlock(id), index, p.need.ReadyBy, p.need.WindowClosed, p.protectionClosed})
	})
	s.retentionMu.Lock()
	for key, blocks := range s.protection {
		for block, p := range blocks {
			if p.until.After(now) {
				addBlock(useBlockID{key, block})
			}
		}
	}
	for i := range input.blocks {
		b := &input.blocks[i]
		b.protection = s.protection[b.id.key][b.id.block]
		if u := s.resourceUses[b.id.key]; u != nil {
			b.song = u.video.songID
		}
		if retained, ok := s.retained[b.id.key]; ok {
			b.available = true
			sizes[b.id.key] = retained.size
			if b.song == "" && retained.songID > 0 {
				b.retainedSong = retained.songID
			}
		}
		if local := s.localResources[b.id.key]; local != nil {
			b.available = b.available || local.has(b.id.block)
			sizes[b.id.key] = local.video.size
			if b.song == "" && b.retainedSong == 0 {
				b.song = local.video.songID
			}
		}
	}
	s.retentionMu.Unlock()
	for i := range input.blocks {
		input.blocks[i].size = sizes[input.blocks[i].id.key]
	}
	s.mu.Unlock()
	return input
}

func (o *blockObserver) capture(previous *BlockSnapshot) (*BlockSnapshot, time.Time) {
	s := o.s
	now := time.Now()
	ahead := s.cfg.PrepareAhead
	if ahead <= 0 {
		ahead = blocksched.DefaultPrepareAhead
	}
	input := o.captureInput(now)
	result := &BlockSnapshot{
		At: now.UnixMilli(), Capacity: maxUseBlocks, Used: input.used,
		PrepareAheadMS: ahead.Milliseconds(), DownloadGraceMS: blocksched.DownloadGrace.Milliseconds(),
		Blocks: make(map[string]BlockView, len(input.blocks)), Demands: make(map[string]BlockDemandView),
		sources: make([]BlockSourceView, len(input.sources)), needs: make([]blockDetailNeed, len(input.needs)),
		sourceRanges: make(map[string]blockSourceRange, len(input.blocks)),
	}
	var next time.Time
	advance := func(at time.Time) {
		if at.After(now) {
			next = earlier(next, at)
		}
	}
	seen := make(map[blockSource]bool, len(input.sources))
	for i, source := range input.sources {
		seen[source.identity] = true
		if o.sourceIDs[source.identity] == 0 {
			o.sequence++
			o.sourceIDs[source.identity] = o.sequence
		}
		source.view.ID = o.sourceIDs[source.identity]
		result.sources[i] = source.view
		if source.view.Kind == "handoff" {
			advance(source.need.LeaseUntil)
		}
	}
	for source := range o.sourceIDs {
		if !seen[source] {
			delete(o.sourceIDs, source)
		}
	}
	// All formatting, aggregation and sorting happens after the business locks.
	for i := range input.blocks {
		raw := &input.blocks[i]
		p := raw.protection
		if !raw.inUse && (!raw.available || !p.active(now)) {
			continue
		}
		name := blockViewID(raw.id)
		raw.name = name
		if raw.song == "" && raw.retainedSong > 0 {
			raw.song = strconv.FormatInt(raw.retainedSong, 10)
		}
		b := BlockView{
			ID: name, Resource: raw.id.key, Block: raw.id.block, InUse: raw.inUse, SongID: raw.song, State: "waiting",
			ProtectionStart: timeMS(p.start), ProtectionUntil: timeMS(p.until), Protected: p.active(now),
			ByteOffset: raw.id.block * s.cfg.BlockSize, Bytes: blockBytes(raw.size, raw.id.block, s.cfg.BlockSize),
			TotalBlocks: blockCount(raw.size, s.cfg.BlockSize),
		}
		if !raw.inUse {
			b.State, b.TimeBasis, b.At = "protected", "ended", timeMS(p.until.Add(-s.cfg.LocalProtectionGrace))
			if previous != nil {
				if old, ok := previous.Blocks[name]; ok {
					b.Title, b.SongID = old.Title, old.SongID
					if b.SongID == "" {
						b.SongID = raw.song
					}
					if old.At != 0 && old.TimeBasis != "ended" {
						b.At, b.TimeBasis = old.At, "last-need-by"
					}
				}
			}
		} else if raw.available {
			b.State = "available"
		}
		advance(p.start)
		advance(p.until)
		result.Blocks[name] = b
	}
	slices.SortFunc(input.needs, func(a, b observedNeed) int { return a.block - b.block })
	for first := 0; first < len(input.needs); {
		index := input.needs[first].block
		last := first + 1
		for last < len(input.needs) && input.needs[last].block == index {
			last++
		}
		raw := input.blocks[index]
		if raw.name != "" {
			b := result.Blocks[raw.name]
			var use blockUse
			var video blockVideo
			for i := first; i < last; i++ {
				need := input.needs[i]
				source := input.sources[need.source]
				p := plannedNeed{need: source.need, protectionClosed: need.protectionClosed}
				p.need.Owner, p.need.ReadyBy, p.need.WindowClosed = raw.id.key, need.ready, need.windowClosed
				use = mergeUse(use, p.use(now))
				video = chooseBlockVideo(video, blockVideo{SongID: source.view.SongID, Title: source.view.Title}, o.videos[b.Resource].SongID)
				result.needs[i] = blockDetailNeed{need.source, timeMS(need.ready), need.windowClosed}
			}
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
			b.Sources = last - first
			if video.SongID != "" {
				b.SongID, b.Title = video.SongID, video.Title
			}
			result.Blocks[raw.name] = b
			result.sourceRanges[raw.name] = blockSourceRange{first, last}
		}
		first = last
	}
	// Scheduler commands must never run under Server.mu/retentionMu.
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
	// Build demand membership directly from compact associations, without
	// materializing or reverse-projecting every block's detail/source array.
	type demandKey struct {
		source   int
		resource string
	}
	demands := make(map[demandKey]*BlockDemandView)
	for _, need := range input.needs {
		raw := input.blocks[need.block]
		if raw.name == "" {
			continue
		}
		b := result.Blocks[raw.name]
		key := demandKey{need.source, b.Resource}
		demand := demands[key]
		if demand == nil {
			source := blockSourceLabel(result.sources[need.source], b)
			id := strconv.FormatUint(source.ID, 10) + "/" + b.Resource
			demand = &BlockDemandView{ID: id, SourceID: source.ID, Kind: source.Kind, SongID: source.SongID, Title: source.Title, Position: source.Position, Resource: b.Resource, TotalBlocks: b.TotalBlocks}
			demands[key] = demand
		}
		demand.Blocks = append(demand.Blocks, raw.name)
	}
	for _, demand := range demands {
		sort.Slice(demand.Blocks, func(i, j int) bool {
			return result.Blocks[demand.Blocks[i]].Block < result.Blocks[demand.Blocks[j]].Block
		})
		result.Demands[demand.ID] = *demand
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
	var missing []resourceLabelLookup
	for key, v := range videos {
		old, exists := o.videos[key]
		if v.SongID == "" && old.SongID != "" {
			v = old // A handoff without identity keeps its last known label.
		} else if v.SongID == old.SongID && v.Title == "" {
			v.Title = old.Title
		}
		videos[key] = v
		if v.Title == "" && (!exists || old.SongID != v.SongID) {
			missing = append(missing, resourceLabelLookup{key, v.SongID})
		}
	}
	if len(missing) > 0 && o.s.Store != nil {
		for key, label := range o.s.Store.resourceLabels(o.ctx, missing) {
			videos[key] = label
		}
	}

	o.videos = videos
	for id, b := range snapshot.Blocks {
		v := videos[b.Resource]
		b.SongID, b.Title = v.SongID, v.Title
		snapshot.Blocks[id] = b
	}
}
