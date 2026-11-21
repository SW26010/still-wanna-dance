package cacheproxy

import (
	"context"
	"errors"
	"os"
	"slices"
	"sync"
	"time"

	"still-wanna-dance/internal/applog"
)

type methodKey struct{ target, path string }

type retryBackoff struct {
	delay time.Duration
	until time.Time
}

func (b *retryBackoff) fail(now time.Time) {
	b.delay = min(time.Minute, max(time.Second, 2*b.delay))
	b.until = now.Add(b.delay)
}

// Ordinary callers with the same preferred song share one cursor. Each fill
// owns its selected songs.
type lookupKey struct {
	song  string
	owner *downloadDemand
}

type addressLookup struct {
	active  bool
	policy  resourceAddresses
	cursor  *resourceAddresses // Resume after the first address if that address fails.
	stop    context.CancelFunc
	refresh retryBackoff
	seen    map[string]bool // Valid addresses returned by this scope's latest pass.
}

type blockAddresses struct {
	init     sync.Once
	pending  bool // New scopes awaiting coordinator startup; protected by Server.mu.
	mu       sync.Mutex
	urls     []video
	changed  chan struct{}
	attached string
	lookups  map[lookupKey]*addressLookup // nil until a missing block requests discovery.
	err      error
	wg       sync.WaitGroup
}

func (a *blockAddresses) notifyLocked() {
	if a.changed != nil {
		close(a.changed)
	}
	a.changed = make(chan struct{})
}

func (a *blockAddresses) attach(v video) {
	if v.host == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attached = v.requestURL()
	a.recordLocked(v, false)
}

// Publish a new candidate at the end, or move the endpoint that delivered a
// complete block to the front. Readers retain their immutable snapshots.
func (a *blockAddresses) recordLocked(v video, delivered bool) {
	if v.host == "" {
		return
	}
	key := v.requestURL()
	i := slices.IndexFunc(a.urls, func(old video) bool { return old.requestURL() == key })
	if i == 0 || i > 0 && !delivered {
		return
	}
	urls := slices.Clone(a.urls)
	if i < 0 {
		i = len(urls)
		urls = append(urls, resourceVideo(v))
	}
	if delivered {
		copy(urls[1:i+1], urls[:i])
		urls[0] = resourceVideo(v)
	}
	a.urls = urls
	a.notifyLocked()
}

// Called with Server.mu. Queue and formal playback can outlive the waiter
// that prepared the resource. A withdrawn GET contributes no further queries.
func (s *Server) syncAddressLookupsLocked(f *flight, startDiscovery bool) {
	a := &f.addresses
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lookups == nil {
		if !startDiscovery {
			return
		}
		a.lookups = make(map[lookupKey]*addressLookup)
	}
	for _, q := range a.lookups {
		q.active = false
	}
	include := func(k lookupKey, policy resourceAddresses) {
		q := a.lookups[k]
		if q == nil {
			q = &addressLookup{policy: policy}
			a.lookups[k] = q
			a.pending = true
			a.notifyLocked()
		}
		q.active = true
		if q.cursor == nil && q.refresh.delay == 0 {
			if policy.preferredCheck != nil {
				q.policy.preferredCheck = policy.preferredCheck
			}
		}
	}
	for d := range s.demands {
		if d.flight != f || d.ctx.Err() != nil || d.exit.at.Load() != nil || d.queue && !s.validQueueSourceLocked(d) {
			continue
		}
		k := lookupKey{song: d.lookup.preferredSong}
		if d.lookup.batch {
			k.owner = d
		}
		include(k, d.lookup)
	}
	continuing := func(song string) {
		include(lookupKey{song: song}, resourceAddresses{key: f.route.key, size: f.size, preferredSong: song})
	}
	for _, queue := range s.prefetchSources {
		if queue.use.key == f.route.key && queue.use.first < queue.use.last {
			continuing(queue.song)
		}
	}
	if p := s.formal; p != nil && p.use.key == f.route.key && p.use.first < p.use.last {
		continuing(p.song)
	}
	for k, q := range a.lookups {
		if !q.active {
			delete(a.lookups, k)
			if q.stop != nil {
				q.stop()
			}
			a.notifyLocked()
		}
	}
}

func (w *downloadSession) refreshAddresses(ctx context.Context, findAlternatives bool) {
	a := &w.f.addresses
	a.init.Do(func() {
		w.s.mu.Lock()
		w.s.syncAddressLookupsLocked(w.f, true)
		a.pending = false // This caller starts the initial scopes below.
		w.s.mu.Unlock()
	})
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil || !findAlternatives && len(a.urls) > 0 {
		return
	}
	for k, q := range a.lookups {
		if q.stop != nil || q.refresh.until.After(time.Now()) {
			continue
		}
		if q.cursor == nil {
			query := q.policy
			q.policy.preferredCheck = nil // Reuse the initial check only on the first pass.
			q.cursor = &query
		}
		lookupCtx, stop := context.WithCancel(ctx)
		q.stop = stop
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			defer stop()
			w.discover(lookupCtx, k, q)
		}()
	}
}

// Publish each address when it becomes available. A removed scope cannot
// publish a late result or advance to the next song.
func (w *downloadSession) discover(ctx context.Context, k lookupKey, q *addressLookup) {
	a := &w.f.addresses
	err := q.cursor.search(ctx, w.s, func(v video) bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		if ctx.Err() != nil || a.lookups[k] != q {
			return false
		}
		first := len(a.urls) == 0
		a.recordLocked(v, false)
		if first {
			q.stop = nil // Try the initial address before searching for alternatives.
		}
		return !first
	})
	if err == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil || a.lookups[k] != q {
		return
	}
	windowEnded := errors.Is(err, errAddressLookupBudget)
	if !windowEnded || len(q.cursor.songs) == 0 {
		if windowEnded || errors.Is(err, errPlaybackUpstream) {
			q.seen = q.cursor.tried
		}
		q.cursor = nil
	}
	if errors.Is(err, ErrLocalStorage) || w.s.Store.Error() != nil {
		a.err = err
	}
	q.refresh.fail(time.Now())
	q.stop = nil
	a.notifyLocked()
}

// All published URLs have had a turn for this block. Only retire obsolete
// signatures after the current lookups have supplied their address lists.
func (a *blockAddresses) retireMissingLocked(original string) {
	if len(a.urls) < 2 || len(a.lookups) == 0 {
		return
	}
	for _, q := range a.lookups {
		if q.stop != nil || q.seen == nil {
			return
		}
	}
	preferred := a.urls[0].requestURL()
	obsolete := func(v video) bool {
		k := v.requestURL()
		// Discovery cannot enumerate redirect targets. Keep the preferred
		// endpoint until another complete block promotes its replacement.
		if k == preferred || k == original || k == a.attached {
			return false
		}
		for _, q := range a.lookups {
			if q.seen[k] {
				return false
			}
		}
		return true
	}
	if slices.ContainsFunc(a.urls, obsolete) {
		a.urls = slices.DeleteFunc(slices.Clone(a.urls), obsolete)
		a.notifyLocked()
	}
}

func (w *downloadSession) fetchBlock(ctx context.Context, block int64, ready func(), needBy func() time.Time) error {
	if w.f.discard {
		return w.s.readRange(ctx, w.f, block, ready, needBy)
	}
	a := &w.f.addresses
	type addressRetry struct {
		backoff retryBackoff
		ipUntil time.Time
		origin  *resourceCandidates // Remaining paths of an interrupted pass over this entry URL.
	}
	retries := make(map[string]addressRetry)
	paths := make(map[methodKey]retryBackoff) // Shared by all entries reaching the same endpoint and route.
	urlRetryAt := func(v video) time.Time { return retries[v.requestURL()].backoff.until }
	awaitingPass := func(v video) bool {
		r := retries[v.requestURL()]
		return r.ipUntil.IsZero() && r.backoff.until.IsZero()
	}
	initial := w.v
	w.s.retentionMu.Lock()
	if w.f.ranges.video.host != "" {
		initial = w.f.ranges.video
	}
	w.s.retentionMu.Unlock()
	a.mu.Lock()
	a.recordLocked(initial, false)
	a.mu.Unlock()

	var dns <-chan struct{}
	if w.s.cfg.DirectCandidates != nil {
		dns = w.s.cfg.DirectCandidates.Changed()
	}
	routes := w.s.cfg.DownloadRoutes
	recovered := routes.recoverySignal()
	var snapshot <-chan struct{}
	var nextURL string
retryBlock:
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		a.mu.Lock()
		if a.changed == nil {
			a.changed = make(chan struct{})
		}
		urls, changed, err := a.urls, a.changed, a.err
		var refreshAt time.Time
		for _, q := range a.lookups {
			if q.stop == nil && !q.refresh.until.IsZero() {
				refreshAt = earlier(refreshAt, q.refresh.until)
			}
		}
		a.mu.Unlock()
		if err != nil {
			return err
		}
		var resourceChanged <-chan struct{}
		if snapshot != nil || len(urls) == 0 {
			// Subscribe before checking inventory so a concurrent publication wakes this block.
			w.s.mu.Lock()
			resourceChanged = w.changes.changed
			w.s.mu.Unlock()
		}
		published := w.s.hasPublishedVideo(w.v)
		if published {
			if ok, err := w.readPublishedBlock(ctx, block); err != nil {
				return err
			} else if ok {
				ready()
				return nil
			}
		}
		// A newly published file warrants another local read. If inventory
		// already led to an unusable file this round, let routing progress.
		fileArrived := func() bool { return !published && w.s.hasPublishedVideo(w.v) }
		if changed != snapshot && snapshot != nil {
			// The URL list is immutable after publication.
			for key, retry := range retries {
				// A redirect target can remain reachable only through an entry.
				if time.Since(retry.backoff.until) > time.Minute && !slices.ContainsFunc(urls, func(v video) bool { return v.requestURL() == key }) {
					delete(retries, key)
				}
			}
		}
		snapshot = changed
		if nextURL != "" {
			if i := slices.IndexFunc(urls, func(v video) bool { return v.requestURL() == nextURL }); i > 0 {
				urls = append(slices.Clone(urls[i:]), urls[:i]...)
			}
			nextURL = ""
		}
		var wake time.Time
		attempted := false

		for _, v := range urls {
			key := v.requestURL()
			retry := retries[key]
			after := maxTime(retry.backoff.until, retry.ipUntil)
			if after.After(time.Now()) {
				wake = earlier(wake, after)
				continue
			}
			if attempted && fileArrived() {
				continue retryBlock
			}
			attempts := &resourceAttempts{paths: paths, block: useBlockID{v.key, block}, persistent: true, needBy: needBy, fallbackDeadline: func() time.Time { return sourceDeadline(needBy()) }}
			attempts.urlRetryAt = urlRetryAt
			if retry.origin != nil {
				retry.origin.err = nil
				attempts.targets = map[string]*resourceCandidates{key: retry.origin}
			}
			attempts.reselectSource = func() bool {
				if fileArrived() {
					return true
				}
				a.mu.Lock()
				defer a.mu.Unlock()
				for _, other := range a.urls {
					k := other.requestURL()
					// An endpoint reached by redirect already has a cursor in this pass.
					if _, tried := retries[k]; !tried && attempts.targets[k] == nil {
						nextURL = k
						return true
					}
				}
				return false
			}
			err := w.s.readRangeAt(ctx, w.f, block, v, ready, attempts)
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			if err == nil {
				return nil
			}
			retry.origin = attempts.targets[key]
			if retry.origin != nil && !retry.origin.more {
				retry.origin = nil
			}
			if errors.Is(err, errReselectSource) {
				retries[key] = retry
				continue retryBlock
			}
			if !errors.Is(err, errUpstreamDownload) {
				return err
			}
			rejected := attempts.rejectedURL
			if rejected != "" {
				w.refreshAddresses(w.addressCtx, true)
				if rejected != key {
					final := retries[rejected]
					final.backoff.fail(time.Now())
					retries[rejected] = final
				}
			}
			if rejected != key && retry.origin != nil {
				// The entry still has an untried route of its own.
				retry.ipUntil = time.Time{}
			} else if rejected == "" && !attempts.retryAt.IsZero() {
				retry.ipUntil = attempts.retryAt
			} else {
				retry.backoff.fail(time.Now())
			}
			// Signed redirect targets can change on every pass. Discard obsolete path history.
			for method, b := range paths {
				if time.Since(b.until) > time.Minute {
					delete(paths, method)
				}
			}
			retries[key] = retry
			attempted = true
			w.f.log.Debug("视频块等待重试", "event_name", "block_retry", "error", applog.SafeError(err), "block", block, "upstream_host", v.host, "retry_at", maxTime(retry.backoff.until, retry.ipUntil))
		}
		a.mu.Lock()
		if a.err == nil && !slices.ContainsFunc(a.urls, awaitingPass) {
			a.retireMissingLocked(w.v.requestURL())
		}
		a.mu.Unlock()
		w.refreshAddresses(w.addressCtx, len(urls) > 0)
		if attempted {
			continue
		}
		if !refreshAt.IsZero() {
			wake = earlier(wake, refreshAt)
		}
		var timer <-chan time.Time
		if !wake.IsZero() {
			timer = time.After(time.Until(wake))
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-changed:
			continue
		case <-resourceChanged:
			continue
		case <-timer:
			continue
		case <-dns:
			dns = w.s.cfg.DirectCandidates.Changed()
		case <-recovered:
			recovered = routes.recoverySignal()
		}
		for k, retry := range retries {
			retry.ipUntil = time.Time{}
			retries[k] = retry
		}
	}
}

// Deliver the range through the waiting readers' existing spool.
func (w *downloadSession) readPublishedBlock(ctx context.Context, block int64) (bool, error) {
	file, err := w.s.verifiedFile(ctx, w.v)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, errInvalidCache) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if file.info.Size() != w.v.size {
		return false, nil
	}
	offset := block * w.s.cfg.BlockSize
	buf := make([]byte, blockBytes(w.v.size, block, w.s.cfg.BlockSize))
	if _, err = file.file.ReadAt(buf, offset); err != nil {
		return false, err
	}
	if _, err = w.f.spool.WriteAt(buf, offset); err != nil {
		return false, err
	}
	w.s.notifyBlockStored(w.v.key)
	return true, nil
}

// A shared route supplies its own retry time. Keep a block-local backoff for
// failures specific to this URL and path, or to an unidentified transport.
func (a *resourceAttempts) failedPath(c *resourceCandidates, sharedUntil time.Time) {
	if !a.persistent {
		return
	}
	if !sharedUntil.IsZero() {
		a.retryAt = earlier(a.retryAt, sharedUntil)
		return
	}
	key := methodKey{c.target, c.path}
	b := a.paths[key]
	b.fail(time.Now())
	a.paths[key] = b
	a.retryAt = earlier(a.retryAt, b.until)
}

func (a *resourceAttempts) pathCooling(c *resourceCandidates, path string) bool {
	until := a.paths[methodKey{c.target, path}].until
	if until.After(time.Now()) {
		a.retryAt = earlier(a.retryAt, until)
		return true
	}
	return false
}
