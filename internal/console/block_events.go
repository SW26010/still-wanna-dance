package console

import (
	"net/http"
	"reflect"
	"sort"
	"time"

	"still-wanna-dance/internal/cacheproxy"
)

type blockEvent struct {
	At              int64                        `json:"at"`
	Running         bool                         `json:"running"`
	Reset           bool                         `json:"reset"`
	Capacity        int                          `json:"capacity"`
	Used            int                          `json:"used"`
	Active          int                          `json:"active"`
	Open            int                          `json:"open"`
	PrepareAheadMS  int64                        `json:"prepareAheadMS"`
	DownloadGraceMS int64                        `json:"downloadGraceMS"`
	Blocks          []cacheproxy.BlockView       `json:"blocks"`
	Removed         []string                     `json:"removed"`
	Demands         []cacheproxy.BlockDemandView `json:"demands"`
	RemovedDemands  []string                     `json:"removedDemands"`
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
	}
	sort.Slice(e.Blocks, func(i, j int) bool { return e.Blocks[i].ID < e.Blocks[j].ID })
	sort.Strings(e.Removed)
	sort.Slice(e.Demands, func(i, j int) bool { return e.Demands[i].ID < e.Demands[j].ID })
	sort.Strings(e.RemovedDemands)
	changed := previous == nil || len(e.Blocks) > 0 || len(e.Removed) > 0 || len(e.Demands) > 0 || len(e.RemovedDemands) > 0 || previous.Used != current.Used || previous.Active != current.Active || previous.Open != current.Open || previous.PrepareAheadMS != current.PrepareAheadMS
	return e, changed
}

func (c *Console) serveBlockEvents(w http.ResponseWriter, r *http.Request) {
	// Native EventSource cannot send a token header. Match the existing SSE
	// origin checks and additionally reject browser cross-site fetch contexts.
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+c.address {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		http.Error(w, "invalid fetch site", http.StatusForbidden)
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
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	if service == nil {
		if err := writeServerEvent(w, controller, "blocks", 0, blockEvent{Reset: true, Blocks: []cacheproxy.BlockView{}, Removed: []string{}, Demands: []cacheproxy.BlockDemandView{}, RemovedDemands: []string{}}); err != nil {
			return
		}
		// Wait for engine creation on this connection. Do not create an engine,
		// retry periodically, or leave a worker behind when the page disappears.
		service = c.waitBlockEngine(r.Context())
		if service == nil {
			return
		}
	}
	sub, err := service.ObserveBlocks()
	if err != nil {
		_ = writeServerEvent(w, controller, "closed", 0, struct{}{})
		return
	}
	defer sub.Close()
	var previous *cacheproxy.BlockSnapshot
	var detail *cacheproxy.BlockDetail
	var revision uint64
	var previousSpeed float64
	for {
		current, changed, closed := sub.Snapshot()
		if closed {
			_ = writeServerEvent(w, controller, "closed", revision, struct{}{})
			return
		}
		if current != nil && current != previous {
			if detailID != "" {
				var next *cacheproxy.BlockDetail
				if v, ok := current.Details[detailID]; ok {
					next = &v
				}
				if previous == nil || !reflect.DeepEqual(detail, next) {
					revision++
					if err := writeServerEvent(w, controller, "block-detail", revision, next); err != nil {
						return
					}
					detail = next
				}
			} else if event, dirty := blockDelta(previous, current); dirty {
				revision++
				// A new/reconnected client may share an old, unchanged snapshot.
				// Anchor its clock at send time without mutating observed state.
				event.At = time.Now().UnixMilli()
				if err := writeServerEvent(w, controller, "blocks", revision, event); err != nil {
					return
				}
			}
			previous = current
		}
		var speedChanged <-chan struct{}
		if detailID == "" {
			speed, changed := sub.DownloadSpeed()
			speedChanged = changed
			// Send the initial block reset before speed: clients clear stale
			// speed when reconnecting, even if a shared observer is already live.
			if previous != nil && speed != previousSpeed {
				revision++
				if err := writeServerEvent(w, controller, "download-speed", revision, struct {
					BytesPerSecond float64 `json:"bytesPerSecond"`
				}{speed}); err != nil {
					return
				}
				previousSpeed = speed
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-changed:
		case <-speedChanged:
		}
	}
}
