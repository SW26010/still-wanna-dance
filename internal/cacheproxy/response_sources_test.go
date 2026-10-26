package cacheproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResponseSourcesReaderWaitAndReuse(t *testing.T) {
	for _, chunk := range []int{1, 32} {
		t.Run(fmt.Sprint(chunk), func(t *testing.T) {
			sp := &spool{blocks: newBlockFiles(t.TempDir(), 10, 4, nil), changed: make(chan struct{}), refs: 1}
			defer sp.release()
			for block, data := range map[int64]string{0: "abcd", 2: "ij"} {
				if _, err := sp.WriteAt([]byte(data), block*4); err != nil {
					t.Fatal(err)
				}
			}
			a, b := sp.reader(t.Context(), 10), sp.reader(t.Context(), 10)
			defer a.Close()
			defer b.Close()
			a.onWait = func() {
				if _, err := sp.WriteAt([]byte("efgh"), 4); err != nil {
					t.Fatal(err)
				}
			}
			for index, reader := range []*spoolReader{a, b} {
				reader.Seek(1, io.SeekStart)
				counter := responseSourceCounter{reader: &reader.sources}
				position := int64(1)
				buffer := make([]byte, chunk)
				for {
					n, err := reader.Read(buffer)
					if n > 0 {
						counter.accept(position, position+int64(n))
						position += int64(n)
					}
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				want := ResponseSources{CacheBytes: 10}
				if index == 0 {
					want = ResponseSources{DownloadBytes: 4, CacheBytes: 6}
				}
				if counter.totals != want {
					t.Fatalf("reader %d: %+v, want %+v", index, counter.totals, want)
				}
			}
		})
	}
}

func TestResponseSourcesRegistrationAndCancellation(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		sp := &spool{blocks: newBlockFiles(t.TempDir(), 4, 4, nil), changed: make(chan struct{}), refs: 1}
		ctx, cancel := context.WithCancel(t.Context())
		sp.demand = func(int64) func() {
			if !cancelRead {
				if _, err := sp.WriteAt([]byte("abcd"), 0); err != nil {
					t.Fatal(err)
				}
			}
			return func() {}
		}
		r := sp.reader(ctx, 4)
		r.onWait = cancel
		counter := responseSourceCounter{reader: &r.sources}
		n, err := r.Read(make([]byte, 4))
		if cancelRead {
			if n != 0 || err != context.Canceled || counter.totals != (ResponseSources{}) {
				t.Fatal(n, err, counter.totals)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			counter.accept(0, int64(n))
			if counter.totals != (ResponseSources{CacheBytes: 4}) {
				t.Fatal(counter.totals)
			}
		}
		r.Close()
		sp.release()
		cancel()
	}
}

func TestResponseSourcesHTTPAndPersistence(t *testing.T) {
	s, cfg := setup(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, payload) }, func(c *Config) { c.BlockSize = 8 })
	assertResponse(t, request(s, "GET", videoURL(payload), nil), 200, payload)
	s.wg.Wait()
	assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=7-8"}), 206, payload[7:9])
	assertResponse(t, request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=35-"}), 206, payload[35:])
	request(s, "GET", videoURL(payload), map[string]string{"Range": "bytes=99-"})
	request(s, "HEAD", videoURL(payload), nil)
	s.usage.flush()
	page, err := ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{Limit: 10})
	if err != nil || len(page.Requests) != 4 {
		t.Fatal(page, err)
	}
	for i, want := range []ResponseSources{{}, {LocalBytes: 4}, {LocalBytes: 16}} {
		if got := page.Requests[i].Sources; got == nil || *got != want {
			t.Fatalf("row %d: %+v, want %+v", i, got, want)
		}
	}
	cold := page.Requests[3].Sources
	if cold == nil || cold.DownloadBytes+cold.CacheBytes+cold.LocalBytes != int64(len(payload)) {
		t.Fatal(cold)
	}
	want, err := ReadResponseSources(t.Context(), cfg.Store.path)
	if err != nil || want.DownloadBytes != cold.DownloadBytes || want.CacheBytes != cold.CacheBytes || want.LocalBytes != cold.LocalBytes+20 {
		t.Fatal(want, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// A new engine can use another geometry; old rows retain their byte values.
	cfg.BlockSize = 4
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	assertResponse(t, request(restarted, "GET", videoURL(payload), map[string]string{"Range": "bytes=7-8"}), 206, payload[7:9])
	restarted.usage.flush()
	page, err = ReadRequestHistory(t.Context(), cfg.Store.path, RequestQuery{Limit: 10})
	if err != nil || page.Requests[0].Sources.LocalBytes != 8 || page.Requests[3].Sources.LocalBytes != 16 {
		t.Fatal(page, err)
	}
	want.LocalBytes += 8
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	// Stop the worker before changing retention. Pruning affects details only.
	if err := s.usage.close(); err != nil {
		t.Fatal(err)
	}
	s.usage.retention = time.Hour
	if _, err := s.usage.prune(time.Now().Add(48 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(cfg.Store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := ReadResponseSources(t.Context(), store.path)
	if err != nil || got != want {
		t.Fatal(got, want, err)
	}
}

type sourceShortWriter struct {
	header   http.Header
	accepted int
}

func (w *sourceShortWriter) Header() http.Header { return w.header }
func (w *sourceShortWriter) WriteHeader(int)     {}
func (w *sourceShortWriter) Write(p []byte) (int, error) {
	return min(len(p), w.accepted), io.ErrUnexpectedEOF
}

func TestResponseSourcesCountOnlyAcceptedBlocks(t *testing.T) {
	for _, accepted := range []int{0, 1, 5} {
		expected := int64(10)
		r := &readSources{size: 10, blockSize: 4, local: true}
		w := &responseWriter{ResponseWriter: &sourceShortWriter{header: make(http.Header), accepted: accepted}, status: 206,
			expectedBytes: &expected, sources: &responseSourceCounter{reader: r}, consumption: &getConsumption{position: 3, limit: 10}}
		w.Write([]byte("defghij"))
		want := int64(0)
		if accepted == 1 {
			want = 4
		}
		if accepted == 5 {
			want = 8
		}
		if w.sources.totals != (ResponseSources{LocalBytes: want}) {
			t.Fatal(accepted, w.sources.totals)
		}
	}
}

func TestResponseSourcesBatchAtomicAndUnknown(t *testing.T) {
	store, err := CreateStore(filepath.Join(t.TempDir(), "sources.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := usageEvent{method: "GET", at: 1, sources: &ResponseSources{DownloadBytes: 8, CacheBytes: 4, LocalBytes: 2}}
	if err := store.usage.write([]usageEvent{e, {method: "GET", songID: "bad"}}); err == nil {
		t.Fatal("bad batch accepted")
	}
	got, err := ReadResponseSources(t.Context(), store.path)
	if err != nil || got != (ResponseSources{}) {
		t.Fatal("partial batch persisted", got, err)
	}
	if err := store.usage.write([]usageEvent{e, {method: "GET", at: 2}}); err != nil {
		t.Fatal(err)
	}
	page, err := ReadRequestHistory(t.Context(), store.path, RequestQuery{Limit: 10})
	if err != nil || page.Requests[0].Sources != nil || *page.Requests[1].Sources != *e.sources {
		t.Fatal(page, err)
	}
	got, err = ReadResponseSources(t.Context(), store.path)
	if err != nil || got != *e.sources {
		t.Fatal(got, err)
	}
}

func BenchmarkResponseSourcesRead(b *testing.B) {
	const size = 8 << 20
	sp := &spool{blocks: newBlockFiles(b.TempDir(), size, 1<<20, nil), changed: make(chan struct{}), refs: 1}
	defer sp.release()
	for offset := int64(0); offset < size; offset += 1 << 20 {
		if _, err := sp.WriteAt([]byte(strings.Repeat("a", 1<<20)), offset); err != nil {
			b.Fatal(err)
		}
	}
	sp.finish(nil)
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprintf("accounting-%t", enabled), func(b *testing.B) {
			r := sp.reader(b.Context(), size)
			defer r.Close()
			buf := make([]byte, 32<<10)
			b.SetBytes(size)
			b.ReportAllocs()
			for b.Loop() {
				r.Seek(0, io.SeekStart)
				counter := responseSourceCounter{reader: &r.sources}
				for pos := int64(0); pos < size; {
					n, err := r.Read(buf)
					if err != nil {
						b.Fatal(err)
					}
					if enabled {
						counter.accept(pos, pos+int64(n))
					}
					pos += int64(n)
				}
				if enabled && counter.totals != (ResponseSources{CacheBytes: size}) {
					b.Fatal(counter.totals)
				}
			}
		})
	}
}
