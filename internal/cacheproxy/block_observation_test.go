package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
)

func observedSnapshot(t *testing.T, sub *BlockSubscription) *BlockSnapshot {
	t.Helper()
	synctest.Wait()
	view, _, closed := sub.Snapshot()
	if view == nil || closed {
		t.Fatal("no live snapshot")
	}
	return view
}

func TestBlockObservationLifecycleSourcesAndGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, s := testSession(t)
		s.ctx = context.Background()
		v := video{key: "resource", size: 2 * rangeBlockSize, songID: "42"}
		s.localResources = map[string]*localResource{v.key: {video: v, done: map[int64]bool{0: true}}}
		clock := &playbackClock{at: time.Now().Add(10 * time.Second), limit: v.size, media: mediaTiming{v.size, time.Minute}}
		ctx := context.WithValue(context.WithValue(context.Background(), playbackClockKey{}, clock), httpGETKey{}, true)
		_, release := addTestDemand(t, s, ctx, v, true)
		_, manual := addTestDemand(t, s, context.Background(), v, false)
		if s.blockObserver.Load() != nil {
			t.Fatal("business created observation")
		}
		sub, err := s.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		other, err := s.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		if sub.observer != other.observer {
			t.Fatal("duplicate observation worker")
		}
		view := observedSnapshot(t, sub)
		first := view.Blocks["resource/0"]
		if view.Used != 2 || first.State != "available" || first.Sources != 2 || first.Readers != 0 || first.NeedBy != clock.at.UnixMilli() {
			t.Fatal(view, first)
		}
		if len(view.Details[first.ID].Sources) != 2 {
			t.Fatal("lost shared sources")
		}
		if len(view.Demands) != 2 || first.SongID != "42" {
			t.Fatal("lost video demand identity", view.Demands, first)
		}
		for _, demand := range view.Demands {
			if demand.SongID != "42" || demand.Resource != v.key || demand.TotalBlocks != 2 || len(demand.Blocks) != 2 || demand.Blocks[0] != "resource/0" || demand.Blocks[1] != "resource/1" {
				t.Fatal("incorrect source block membership/order", demand)
			}
		}
		// No events means no background capture, even with subscribers.
		time.Sleep(time.Second)
		synctest.Wait()
		if current, _, _ := sub.Snapshot(); current != view {
			t.Fatal("polled unchanged state")
		}
		manual()
		release()
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()
		view = observedSnapshot(t, sub)
		tail := view.Blocks[first.ID]
		if len(view.Demands) != 0 || tail.SongID != first.SongID {
			t.Fatal("ended demand retained or video identity lost", view.Demands, tail)
		}
		if view.Used != 0 || len(view.Blocks) != 1 || tail.State != "protected" || tail.InUse || tail.At != first.At || tail.TimeBasis != "last-need-by" || tail.ProtectionUntil == 0 {
			t.Fatal(view, tail)
		}
		other.Close()
		if s.blockObserver.Load() == nil {
			t.Fatal("one viewer closed another")
		}
		time.Sleep(s.cfg.LocalProtectionGrace)
		synctest.Wait()
		if len(observedSnapshot(t, sub).Blocks) != 0 {
			t.Fatal("expired protection remained without reclamation")
		}
		o := sub.observer
		sub.Close()
		if s.blockObserver.Load() != nil || o.snapshot != nil || o.sourceIDs != nil || o.videos != nil {
			t.Fatal("observer retained after last close")
		}
		select {
		case <-o.done:
		default:
			t.Fatal("observer worker still running")
		}
	})
}

func TestBlockObservationBatchesTimeBoundariesAndChangesTogether(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, s := testSession(t)
		s.ctx = context.Background()
		v := video{key: "resource", size: 2 * rangeBlockSize}
		s.localResources = map[string]*localResource{v.key: {video: v, done: map[int64]bool{0: true, 1: true}}}
		start := time.Now()
		s.protection = map[string]map[int64]blockProtection{v.key: {
			0: {until: start.Add(100 * time.Millisecond)},
			1: {until: start.Add(110 * time.Millisecond)},
		}}
		sub, err := s.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		initial := observedSnapshot(t, sub)
		if len(initial.Blocks) != 2 {
			t.Fatal("missing protected blocks")
		}
		time.Sleep(125 * time.Millisecond)
		if observedSnapshot(t, sub) != initial {
			t.Fatal("nearby time boundaries bypassed the batch window")
		}
		s.notifyBlockObservation()
		time.Sleep(25 * time.Millisecond)
		updated := observedSnapshot(t, sub)
		if updated == initial || len(updated.Blocks) != 0 || updated.At != start.Add(150*time.Millisecond).UnixMilli() {
			t.Fatal("boundaries and change did not produce one trailing snapshot", updated)
		}
		time.Sleep(time.Second)
		if observedSnapshot(t, sub) != updated {
			t.Fatal("batch retained a wakeup or polled unchanged state")
		}
	})
}

func TestBlockObservationReopensWithoutInventingHistory(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, s := testSession(t)
		s.ctx = context.Background()
		v := video{key: "resource", size: rangeBlockSize}
		s.localResources = map[string]*localResource{v.key: {video: v, done: map[int64]bool{0: true}}}
		s.cfg.LocalProtectionGrace = 123 * time.Second
		until := time.Now().Add(20 * time.Second)
		s.protection = map[string]map[int64]blockProtection{v.key: {0: {until: until}}}
		sub, err := s.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		b := observedSnapshot(t, sub).Blocks["resource/0"]
		if b.TimeBasis != "ended" || b.At != until.Add(-123*time.Second).UnixMilli() || b.NeedBy != 0 || b.InUse {
			t.Fatal(b)
		}
	})
}

func TestBlockObservationQueueDemandUpdatesWithoutChangingSharedBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, s := testSession(t)
		s.ctx = context.Background()
		v := video{key: "resource", size: 2 * rangeBlockSize, songID: "42"}
		_, manual := addTestDemand(t, s, context.Background(), v, false)
		defer manual()
		q := &queueSource{song: "42", title: "星河", position: 2, startAt: time.Now().Add(time.Minute), timing: mediaTiming{v.size, time.Minute}, use: useSpan{v.key, 1, 2}}
		s.mu.Lock()
		s.prefetchSources = map[string]*queueSource{"42": q}
		s.mu.Unlock()
		sub, err := s.ObserveBlocks()
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		initial := observedSnapshot(t, sub)
		var queue BlockDemandView
		for _, demand := range initial.Demands {
			if demand.Kind == "queue" {
				queue = demand
			}
		}
		if queue.Position != 3 || queue.Title != "星河" || queue.TotalBlocks != 2 || len(queue.Blocks) != 1 || queue.Blocks[0] != "resource/1" {
			t.Fatal("incorrect queue projection", queue)
		}
		s.mu.Lock()
		q.position = 0
		s.notifyBlockObservation()
		s.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		next := observedSnapshot(t, sub)
		if next.Demands[queue.ID].Position != 1 {
			t.Fatal("queue position lost stable identity", next.Demands)
		}
		s.mu.Lock()
		delete(s.prefetchSources, q.song)
		s.notifyBlockObservation()
		s.mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		next = observedSnapshot(t, sub)
		if _, exists := next.Demands[queue.ID]; exists || len(next.Blocks) != 2 || len(next.Demands) != 1 {
			t.Fatal("shared blocks or remaining manual demand changed", next.Demands, next.Blocks)
		}
	})
}

func TestBlockObservationSourceMetadataKeepsPerBlockTimingAndPrecedence(t *testing.T) {
	_, s := testSession(t)
	s.ctx = context.Background()
	size := int64(3 * rangeBlockSize)
	start := time.Now().Add(time.Minute)
	q := &queueSource{song: "42", title: "星河", position: 2, startAt: start, use: useSpan{"resource", 0, 2}, timing: mediaTiming{size, 3 * time.Minute}}
	s.prefetchSources = map[string]*queueSource{"42": q}
	s.useBlocks = map[useBlockID]int{{"resource", 0}: 1, {"resource", 1}: 1}
	s.localResources = map[string]*localResource{"resource": {video: video{key: "resource", size: size, songID: "99"}, done: map[int64]bool{0: true}}}
	o := &blockObserver{s: s, ctx: s.ctx, sourceIDs: make(map[blockSource]uint64)}
	first, _ := o.capture(nil)
	var sourceID uint64
	for index := int64(0); index < 2; index++ {
		b := first.Blocks[blockViewID(useBlockID{"resource", index})]
		wantTime := start.Add(time.Duration(index) * time.Minute).UnixMilli()
		if b.SongID != "42" || b.Title != q.title || b.NeedBy != wantTime || b.TotalBlocks != 3 {
			t.Fatal("source metadata lost precedence or per-block timing", b)
		}
		source := first.Details[b.ID].Sources[0]
		if source.NeedBy != wantTime || source.Position != 3 || source.SongID != "42" {
			t.Fatal("incorrect block source", source)
		}
		if index == 0 {
			sourceID = source.ID
			if b.State != "available" {
				t.Fatal("lost local availability", b)
			}
		} else if source.ID != sourceID || b.State != "waiting" {
			t.Fatal("shared identity or waiting state changed", b, source)
		}
	}
	q.title, q.position = "远山", 0
	second, _ := o.capture(first)
	for id, b := range second.Blocks {
		if b.Title != "远山" || second.Details[id].Sources[0].ID != sourceID || second.Details[id].Sources[0].Position != 1 {
			t.Fatal("source update lost identity", b)
		}
		if first.Blocks[id].Title != "星河" || first.Details[id].Sources[0].Position != 3 {
			t.Fatal("published snapshot mutated", first.Details[id])
		}
	}
}

func TestBlockObservationDisabledNotificationsAllocateNothing(t *testing.T) {
	s := new(Server)
	if count := testing.AllocsPerRun(1000, func() { s.notifyBlockObservation() }); count != 0 {
		t.Fatal(count)
	}
	if s.blockObserver.Load() != nil {
		t.Fatal("idle notification allocated observer")
	}
}

func TestBlockObservationHandoffKeepsVideoName(t *testing.T) {
	_, s := testSession(t)
	s.ctx = context.Background()
	size := int64(45*rangeBlockSize - 7)
	q := &queueSource{song: "42", title: "星河", use: useSpan{"resource", 12, 14}, timing: mediaTiming{size, time.Minute}}
	s.prefetchSources = map[string]*queueSource{"42": q}
	s.useBlocks = map[useBlockID]int{{"resource", 12}: 1, {"resource", 13}: 1}
	o := &blockObserver{s: s, ctx: s.ctx, sourceIDs: make(map[blockSource]uint64)}
	initial, _ := o.capture(nil)
	delete(s.prefetchSources, "42")
	s.useHandoffs = append(s.useHandoffs, &useHandoff{span: q.use, until: time.Now().Add(time.Minute), clock: playbackClock{media: q.timing}})
	handoff, _ := o.capture(initial)
	if len(handoff.Demands) != 1 {
		t.Fatal("missing handoff", handoff.Demands)
	}
	for _, d := range handoff.Demands {
		if d.Kind != "handoff" || d.Title != "星河" || d.SongID != "42" || d.TotalBlocks != 45 {
			t.Fatal("handoff lost video metadata", d)
		}
	}
	b := handoff.Blocks["resource/12"]
	if b.Block != 12 || b.TotalBlocks != 45 || b.Title != "星河" || handoff.Details[b.ID].Sources[0].Title != "星河" {
		t.Fatal("block/detail lost video metadata", b, handoff.Details[b.ID])
	}
	s.useHandoffs = nil
	s.useBlocks = nil
	o.capture(handoff)
	if len(o.videos) != 0 {
		t.Fatal("metadata outlived its visible video")
	}
}

func TestBlockObservationOpensDuringHandoffWithCatalogName(t *testing.T) {
	catalog, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected upstream request") })
	key := strings.Repeat("a", 32)
	title, fallback := "星河", "远山"
	if err := catalog.SyncCatalog(context.Background(), Catalog{Source: "test", Revision: "20261020000000", Songs: []CatalogSong{
		{ID: 42, MD5: key, Name: &title}, {ID: 84, MD5: key, ComposedTitle: &fallback},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ song, wantID, wantTitle string }{
		{"84", "84", "远山"}, // Honor known identity when multiple songs use this video.
		{"", "42", "星河"},   // An existing handoff without identity can resolve the resource.
	} {
		t.Run("song="+tc.song, func(t *testing.T) {
			_, s := testSession(t)
			s.ctx, s.usage = context.Background(), catalog.usage
			v := video{key: key, songID: tc.song, size: 2*rangeBlockSize + 37}
			s.resourceUses = map[string]*resourceUse{key: {video: v}}
			s.useBlocks = map[useBlockID]int{{key, 1}: 1}
			s.useHandoffs = append(s.useHandoffs, &useHandoff{span: useSpan{key, 1, 2}, until: time.Now().Add(time.Minute), clock: playbackClock{media: mediaTiming{v.size, time.Minute}}})
			o := &blockObserver{s: s, ctx: s.ctx, sourceIDs: make(map[blockSource]uint64)}
			view, _ := o.capture(nil)
			b := view.Blocks[key+"/1"]
			if b.SongID != tc.wantID || b.Title != tc.wantTitle || b.TotalBlocks != 3 {
				t.Fatal("late subscription has no catalog name/full video extent", b)
			}
			for _, d := range view.Demands {
				if d.Kind != "handoff" || d.Title != tc.wantTitle || d.SongID != tc.wantID || d.TotalBlocks != 3 {
					t.Fatal("handoff card lost catalog identity", d)
				}
			}
		})
	}
}

func TestBlockObservationStableSharedVideoLabels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cached blockVideo
		blocks []blockVideo
		want   blockVideo
	}{
		{"keep-cached-title", blockVideo{"42", "星河"}, []blockVideo{{SongID: "42"}, {SongID: "84"}}, blockVideo{"42", "星河"}},
		{"prefer-cached-identity", blockVideo{"84", "远山"}, []blockVideo{{"42", "星河"}, {SongID: "84"}}, blockVideo{"84", "远山"}},
		{"keep-cached-miss", blockVideo{SongID: "84"}, []blockVideo{{"42", "星河"}, {SongID: "84"}}, blockVideo{SongID: "84"}},
		{"first-identity-is-stable", blockVideo{}, []blockVideo{{SongID: "84"}, {SongID: "42"}}, blockVideo{SongID: "42"}},
		{"title-belongs-to-identity", blockVideo{}, []blockVideo{{"84", "远山"}, {SongID: "42"}}, blockVideo{SongID: "42"}},
		{"refresh-matching-title", blockVideo{"42", "星河"}, []blockVideo{{"42", "星河新版"}, {"84", "远山"}}, blockVideo{"42", "星河新版"}},
		{"conflicting-titles-are-stable", blockVideo{}, []blockVideo{{"42", "Title B"}, {"42", "Title A"}}, blockVideo{"42", "Title A"}},
		{"replace-absent-identity", blockVideo{"42", "星河"}, []blockVideo{{SongID: "84"}}, blockVideo{SongID: "84"}},
		{"handoff-keeps-identity", blockVideo{"42", "星河"}, []blockVideo{{}}, blockVideo{"42", "星河"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, reverse := range []bool{false, true} {
				o := &blockObserver{s: new(Server), videos: map[string]blockVideo{"shared": tc.cached}}
				snapshot := &BlockSnapshot{Blocks: make(map[string]BlockView)}
				for i := range tc.blocks {
					index := i
					if reverse {
						index = len(tc.blocks) - 1 - i
					}
					v := tc.blocks[index]
					id := blockViewID(useBlockID{"shared", int64(index)})
					snapshot.Blocks[id] = BlockView{ID: id, Resource: "shared", Block: int64(index), SongID: v.SongID, Title: v.Title}
				}
				o.labelVideos(snapshot)
				if got := o.videos["shared"]; got != tc.want {
					t.Fatalf("reverse=%v: cached label = %+v, want %+v", reverse, got, tc.want)
				}
				for _, b := range snapshot.Blocks {
					if b.SongID != tc.want.SongID || b.Title != tc.want.Title {
						t.Fatal("shared blocks have inconsistent labels", b)
					}
				}
			}
		})
	}
}

func TestBlockObservationSharedBlockKeepsSourceTitlesWithIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cached blockVideo
		first  blockVideo
		second blockVideo
		want   blockVideo
	}{
		{"missing-title", blockVideo{}, blockVideo{SongID: "42"}, blockVideo{"84", "远山"}, blockVideo{SongID: "42"}},
		{"source-order", blockVideo{}, blockVideo{"84", "远山"}, blockVideo{"42", "星河"}, blockVideo{"42", "星河"}},
		{"cached-identity", blockVideo{"84", "远山"}, blockVideo{"42", "星河"}, blockVideo{SongID: "84"}, blockVideo{"84", "远山"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s := testSession(t)
			s.ctx = context.Background()
			source := func(v blockVideo) *queueSource {
				return &queueSource{song: v.SongID, title: v.Title, startAt: time.Now().Add(time.Minute),
					use: useSpan{"resource", 0, 1}, timing: mediaTiming{rangeBlockSize, time.Minute}}
			}
			first, second := source(tc.first), source(tc.second)
			s.prefetchSources = map[string]*queueSource{first.song: first, second.song: second}
			s.useBlocks = map[useBlockID]int{{"resource", 0}: 2}
			o := &blockObserver{s: s, ctx: s.ctx, sequence: 2,
				sourceIDs: map[blockSource]uint64{{kind: "queue", id: first}: 1, {kind: "queue", id: second}: 2},
				videos:    map[string]blockVideo{"resource": tc.cached}}
			view, _ := o.capture(nil)
			b := view.Blocks["resource/0"]
			if b.SongID != tc.want.SongID || b.Title != tc.want.Title {
				t.Fatalf("shared block label = %q/%q, want %+v", b.SongID, b.Title, tc.want)
			}
			for _, demand := range view.Demands {
				wantTitle := s.prefetchSources[demand.SongID].title
				if demand.SongID == tc.want.SongID && wantTitle == "" {
					wantTitle = tc.want.Title
				}
				if demand.Title != wantTitle {
					t.Fatal("demand borrowed another song's title", demand)
				}
			}
			for _, source := range view.Details[b.ID].Sources {
				wantTitle := s.prefetchSources[source.SongID].title
				if source.SongID == tc.want.SongID && wantTitle == "" {
					wantTitle = tc.want.Title
				}
				if source.Title != wantTitle {
					t.Fatal("detail borrowed another song's title", source)
				}
			}
		})
	}
}

func TestBlockObservationSharedVideoCatalogCacheAndSourceIdentity(t *testing.T) {
	catalog, _ := setup(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected upstream request") })
	key := strings.Repeat("a", 32)
	title, otherTitle := "星河", "远山"
	if err := catalog.SyncCatalog(context.Background(), Catalog{Source: "test", Revision: "20261020000000", Songs: []CatalogSong{
		{ID: 42, MD5: key, Name: &title}, {ID: 84, MD5: key, Name: &otherTitle},
	}}); err != nil {
		t.Fatal(err)
	}
	_, s := testSession(t)
	s.ctx, s.usage = context.Background(), catalog.usage
	first := &downloadDemand{ctx: s.ctx, key: key, song: "42", playback: true, lastBlock: 1}
	second := &downloadDemand{ctx: s.ctx, key: key, song: "84", playback: true, firstBlock: 1, lastBlock: 2}
	s.demands = map[*downloadDemand]bool{first: true}
	s.useBlocks = map[useBlockID]int{{key, 0}: 1}
	o := &blockObserver{s: s, ctx: s.ctx, sourceIDs: make(map[blockSource]uint64)}
	initial, _ := o.capture(nil)
	if b := initial.Blocks[key+"/0"]; b.SongID != "42" || b.Title != title {
		t.Fatal("initial catalog title missing", b)
	}
	// A changed catalog title makes an unwanted repeat lookup observable.
	if _, err := catalog.usage.db.Exec("UPDATE songs SET name = ? WHERE song_id = 42", "目录新标题"); err != nil {
		t.Fatal(err)
	}
	s.demands[second], s.useBlocks[useBlockID{key, 1}] = true, 1
	previous := initial
	for range 16 {
		view, _ := o.capture(previous)
		for _, b := range view.Blocks {
			if b.SongID != "42" || b.Title != title {
				t.Fatal("shared source changed cached identity/title or repeated lookup", b)
			}
		}
		if len(view.Demands) != 2 {
			t.Fatal("shared resource lost independent sources", view.Demands)
		}
		for _, demand := range view.Demands {
			if len(demand.Blocks) != 1 {
				t.Fatal("source changed its block membership", demand)
			}
			wantSong, wantTitle := "42", title
			if demand.Blocks[0] == key+"/1" {
				wantSong, wantTitle = "84", "" // Another song's resource label is not this source's title.
			}
			if demand.SongID != wantSong || demand.Title != wantTitle {
				t.Fatal("source borrowed another song's title", demand)
			}
			source := view.Details[demand.Blocks[0]].Sources[0]
			if source.SongID != wantSong || source.Title != wantTitle {
				t.Fatal("source detail changed its identity/title", source)
			}
		}
		previous = view
	}
	delete(s.demands, first)
	delete(s.useBlocks, useBlockID{key, 0})
	replacement, _ := o.capture(previous)
	if b := replacement.Blocks[key+"/1"]; b.SongID != "84" || b.Title != otherTitle {
		t.Fatal("remaining identity did not resolve its own catalog title", b)
	}
	if b := initial.Blocks[key+"/0"]; b.SongID != "42" || b.Title != title {
		t.Fatal("published snapshot was modified", b)
	}
}

func TestBlockObservationRealRangeRead(t *testing.T) {
	body := strings.Repeat("b", int(2*rangeBlockSize+37))
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	s, _ := setup(t, func(w http.ResponseWriter, r *http.Request) {
		var first, last int64
		if _, err := fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &first, &last); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		last = min(last, int64(len(body)-1))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", first, last, len(body)))
		w.Header().Set("Content-Length", fmt.Sprint(last-first+1))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		io.WriteString(w, body[first:last+1])
	})
	sub, err := s.ObserveBlocks()
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	finished := make(chan *responseRecorder, 1)
	go func() { finished <- request(s, "GET", videoURL(body), nil) }()
	wait := func(predicate func(*BlockSnapshot) bool) {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			snapshot, changed, closed := sub.Snapshot()
			if snapshot != nil && predicate(snapshot) {
				return
			}
			if closed {
				t.Fatal("observer closed")
			}
			select {
			case <-changed:
			case <-deadline.C:
				t.Fatal("missing real download event", snapshot)
			}
		}
	}
	wait(func(s *BlockSnapshot) bool {
		for _, b := range s.Blocks {
			if b.State == "downloading" && b.Readers > 0 {
				if b.TotalBlocks != 3 {
					t.Error("block numbering lost partial video tail", b)
				}
				for _, demand := range s.Demands {
					if demand.TotalBlocks != 3 {
						t.Error("full video extent must include its partial last block", demand)
					}
				}
				return true
			}
		}
		return false
	})
	release()
	select {
	case response := <-finished:
		if response.Code != 200 || response.aborted || response.Body.String() != body {
			t.Fatal("observation changed HTTP delivery")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GET did not finish")
	}
	wait(func(s *BlockSnapshot) bool {
		if s.Used != 0 || len(s.Blocks) != 3 {
			return false
		}
		for _, b := range s.Blocks {
			if b.State != "protected" || b.Readers != 0 || b.TotalBlocks != 3 {
				return false
			}
		}
		return true
	})
	sub.Close()
	if s.blockObserver.Load() != nil {
		t.Fatal("real subscriber retained observer")
	}
}

func BenchmarkBlockObservationDisabled(b *testing.B) {
	s := new(Server)
	b.ReportAllocs()
	for b.Loop() {
		s.notifyBlockObservation()
	}
}

func BenchmarkBlockObservation4096(b *testing.B) {
	s := &Server{cfg: DefaultConfig(), ctx: context.Background(), useBlocks: make(map[useBlockID]int), demands: make(map[*downloadDemand]bool)}
	s.scheduler = blocksched.New(blocksched.Config{SoftLimit: 16})
	defer s.scheduler.Close()
	size := int64(maxUseBlocks) * rangeBlockSize
	d := &downloadDemand{ctx: context.Background(), key: "benchmark", playback: true, lastBlock: maxUseBlocks, clock: &playbackClock{at: time.Now().Add(time.Hour), limit: size, media: mediaTiming{size, time.Hour}}}
	s.demands[d] = true
	for block := int64(0); block < maxUseBlocks; block++ {
		s.useBlocks[useBlockID{d.key, block}] = 1
	}
	o := &blockObserver{s: s, sourceIDs: make(map[blockSource]uint64)}
	b.ReportAllocs()
	for b.Loop() {
		o.capture(nil)
	}
}
