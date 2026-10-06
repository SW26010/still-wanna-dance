package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type firstBodyRecorder struct {
	*httptest.ResponseRecorder
	first chan struct{}
}

func (w *firstBodyRecorder) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	select {
	case w.first <- struct{}{}:
	default:
	}
	return n, err
}

func TestStreamIfRangeUsesActualResponseForVerification(t *testing.T) {
	for _, ifRange := range []string{`"different"`, `W/"current"`, "Wed, 21 Oct 2015 07:28:00 GMT", `"current"`} {
		t.Run(ifRange, func(t *testing.T) {
			file, err := os.Create(filepath.Join(t.TempDir(), "spool"))
			if err != nil {
				t.Fatal(err)
			}
			sp := &spool{file: file, changed: make(chan struct{}), refs: 1, demand: func(int64) func() { return func() {} }}
			defer sp.release()
			const body = "complete bytes awaiting verification"
			if _, err := sp.WriteAt([]byte(body), 0); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			reader := sp.reader(ctx, int64(len(body)))
			defer reader.Close()
			w := &firstBodyRecorder{httptest.NewRecorder(), make(chan struct{}, 1)}
			pending := &streamResponseWriter{ResponseWriter: w, header: make(http.Header), stream: reader}
			pending.Header().Set("Content-Type", "video/mp4")
			pending.Header().Set("ETag", `"current"`)
			r := httptest.NewRequest("GET", "/video.mp4", nil).WithContext(ctx)
			r.Header.Set("Range", "bytes=-4")
			r.Header.Set("If-Range", ifRange)
			done := make(chan struct{})
			go func() { http.ServeContent(pending, r, "video.mp4", time.Time{}, reader); close(done) }()
			select {
			case <-w.first:
			case <-ctx.Done():
				t.Fatal("no body", ctx.Err())
			}
			verificationFailure := errors.New("whole-file checksum failed")
			if ifRange != `"current"` {
				sp.finish(verificationFailure)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("reader stalled", ctx.Err())
			}
			if ifRange == `"current"` {
				if w.Code != 206 || w.Body.String() != body[len(body)-4:] || reader.err != nil {
					t.Fatalf("range: %d %q %v", w.Code, w.Body.String(), reader.err)
				}
			} else if w.Code != 200 || w.Body.String() != body[:len(body)-1] || !errors.Is(reader.err, verificationFailure) {
				t.Fatalf("full response escaped verification: %d %q %v", w.Code, w.Body.String(), reader.err)
			}
		})
	}
}
