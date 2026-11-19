package console

import (
	"net/http"
	"reflect"
	"sort"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

type blockEvent struct {
	At                  int64                                       `json:"at"`
	Running             bool                                        `json:"running"`
	Reset               bool                                        `json:"reset"`
	Capacity            int                                         `json:"capacity"`
	Used                int                                         `json:"used"`
	Active              int                                         `json:"active"`
	Open                int                                         `json:"open"`
	PrepareAheadMS      int64                                       `json:"prepareAheadMS"`
	DownloadGraceMS     int64                                       `json:"downloadGraceMS"`
	Blocks              []cacheproxy.BlockView                      `json:"blocks"`
	Removed             []string                                    `json:"removed"`
	Demands             []cacheproxy.BlockDemandView                `json:"demands"`
	RemovedDemands      []string                                    `json:"removedDemands"`
	Availability        map[string]cacheproxy.BlockAvailabilityView `json:"availability,omitempty"`
	RemovedAvailability []string                                    `json:"removedAvailability,omitempty"`
}

func blockDelta(previous, current *cacheproxy.BlockSnapshot) (blockEvent, bool) {
	e := blockEvent{Running: true, Reset: previous == nil, Capacity: current.Capacity, Used: current.Used, Active: current.Active, Open: current.Open, PrepareAheadMS: current.PrepareAheadMS, DownloadGraceMS: current.DownloadGraceMS, Blocks: []cacheproxy.BlockView{}, Removed: []string{}}
	e.Demands, e.RemovedDemands = []cacheproxy.BlockDemandView{}, []string{}
	for id, demand := range current.Demands {
		if previous == nil || !reflect.DeepEqual(previous.Demands[id], demand) {
			e.Demands = append(e.Demands, demand)
		}
	}
	for id, b := range current.Blocks {
		if previous == nil {
			e.Blocks = append(e.Blocks, b)
		} else if old, ok := previous.Blocks[id]; !ok || old != b {
			e.Blocks = append(e.Blocks, b)
		}
	}
	for resource, availability := range current.Availability {
		if previous == nil || !reflect.DeepEqual(previous.Availability[resource], availability) {
			if e.Availability == nil {
				e.Availability = make(map[string]cacheproxy.BlockAvailabilityView)
			}
			e.Availability[resource] = availability
		}
	}
	if previous != nil {
		for id := range previous.Demands {
			if _, ok := current.Demands[id]; !ok {
				e.RemovedDemands = append(e.RemovedDemands, id)
			}
		}
		for id := range previous.Blocks {
			if _, ok := current.Blocks[id]; !ok {
				e.Removed = append(e.Removed, id)
			}
		}
		for resource := range previous.Availability {
			if _, ok := current.Availability[resource]; !ok {
				e.RemovedAvailability = append(e.RemovedAvailability, resource)
			}
		}
	}
	sort.Slice(e.Blocks, func(i, j int) bool { return e.Blocks[i].ID < e.Blocks[j].ID })
	sort.Strings(e.Removed)
	sort.Slice(e.Demands, func(i, j int) bool { return e.Demands[i].ID < e.Demands[j].ID })
	sort.Strings(e.RemovedDemands)
	sort.Strings(e.RemovedAvailability)
	changed := previous == nil || len(e.Blocks) > 0 || len(e.Removed) > 0 || len(e.Demands) > 0 || len(e.RemovedDemands) > 0 ||
		len(e.Availability) > 0 || len(e.RemovedAvailability) > 0 || previous.Used != current.Used || previous.Active != current.Active || previous.Open != current.Open || previous.PrepareAheadMS != current.PrepareAheadMS
	return e, changed
}

func (c *Console) serveBlockEvents(w http.ResponseWriter, r *http.Request) {
	if !c.allowEventStream(w, r) {
		return
	}
	c.mu.Lock()
	service, allowed := c.service, !c.closing && c.terms.valid()
	c.mu.Unlock()
	if !allowed {
		http.Error(w, "请先阅读并同意使用条款", http.StatusConflict)
		return
	}
	detailID := r.URL.Query().Get("block")
	if len(detailID) > 128 {
		http.Error(w, "invalid block", http.StatusBadRequest)
		return
	}
	stream := c.openRealtimeEvents(w, r)
	if stream == nil {
		return
	}
	defer stream.close()
	if service == nil {
		if err := stream.write("blocks", 0, blockEvent{Reset: true, Blocks: []cacheproxy.BlockView{}, Removed: []string{}, Demands: []cacheproxy.BlockDemandView{}, RemovedDemands: []string{}}); err != nil {
			return
		}
		// Wait for engine creation on this connection. Do not create an engine,
		// retry periodically, or leave a worker behind when the page disappears.
		service = c.waitBlockEngine(stream.ctx)
		if service == nil {
			return
		}
	}
	sub, err := service.ObserveBlocks()
	if err != nil {
		_ = stream.write("closed", 0, struct{}{})
		return
	}
	defer sub.Close()
	var previous *cacheproxy.BlockSnapshot
	var detail *cacheproxy.BlockDetail
	var revision uint64
	var previousSpeed float64
	previousCacheBytes := int64(-1) // Every connection receives an initial total, including zero.
	for {
		current, changed, closed := sub.Snapshot()
		if closed {
			_ = stream.write("closed", revision, struct{}{})
			return
		}
		if current != nil && current != previous {
			if detailID != "" {
				var next *cacheproxy.BlockDetail
				if v, ok := current.Detail(detailID); ok {
					next = &v
				}
				if previous == nil || !reflect.DeepEqual(detail, next) {
					revision++
					if err := stream.write("block-detail", revision, next); err != nil {
						return
					}
					detail = next
				}
			} else if event, dirty := blockDelta(previous, current); dirty {
				revision++
				// A new/reconnected client may share an old, unchanged snapshot.
				// Anchor its clock at send time without mutating observed state.
				event.At = time.Now().UnixMilli()
				if err := stream.write("blocks", revision, event); err != nil {
					return
				}
			}
			previous = current
		}
		if stream.ctx.Err() != nil {
			return
		}
		var speedChanged, cacheChanged <-chan struct{}
		if detailID == "" {
			speed, changed := sub.DownloadSpeed()
			speedChanged = changed
			// Send the initial block reset before speed: clients clear stale
			// speed when reconnecting, even if a shared observer is already live.
			if previous != nil && speed != previousSpeed {
				revision++
				if err := stream.write("download-speed", revision, struct {
					BytesPerSecond float64 `json:"bytesPerSecond"`
				}{speed}); err != nil {
					return
				}
				previousSpeed = speed
			}
			cacheBytes, changed := sub.BlockCacheBytes()
			cacheChanged = changed
			if previous != nil && cacheBytes != previousCacheBytes {
				revision++
				if err := stream.write("block-cache", revision, struct {
					Bytes int64 `json:"bytes"`
				}{cacheBytes}); err != nil {
					return
				}
				previousCacheBytes = cacheBytes
			}
		}
		select {
		case <-stream.ctx.Done():
			return
		case <-c.lifecycle.stop:
			return
		case <-changed:
		case <-speedChanged:
		case <-cacheChanged:
		}
	}
}
