package cacheproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"still-wanna-dance/internal/blocksched"
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

func TestStreamResponseCompletesBeforeVerification(t *testing.T) {
	for _, ifRange := range []string{`"different"`, `W/"current"`, "Wed, 21 Oct 2015 07:28:00 GMT", `"current"`} {
		t.Run(ifRange, func(t *testing.T) {
			const body = "complete bytes awaiting verification"
			sp := &spool{blocks: newBlockFiles(t.TempDir(), int64(len(body)), rangeBlockSize, nil), changed: make(chan struct{}), refs: 1, demand: func(int64) func() { return func() {} }}
			defer sp.release()
			if _, err := sp.WriteAt([]byte(body), 0); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			reader := sp.reader(ctx, int64(len(body)))
			defer reader.Close()
			w := &firstBodyRecorder{httptest.NewRecorder(), make(chan struct{}, 1)}
			pending := &streamResponseWriter{ResponseWriter: w, header: make(http.Header)}
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

			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("reader stalled", ctx.Err())
			}
			sp.finish(verificationFailure) // Later publication failure cannot change delivery.
			if ifRange == `"current"` {
				if w.Code != 206 || w.Body.String() != body[len(body)-4:] || reader.err != nil {
					t.Fatalf("range: %d %q %v", w.Code, w.Body.String(), reader.err)
				}
			} else if w.Code != 200 || w.Body.String() != body || reader.err != nil {
				t.Fatalf("full response waited for verification: %d %q %v", w.Code, w.Body.String(), reader.err)
			}
		})
	}
}

func TestStreamResponsePreservesCancellationDuringSeek(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, byteRange := range []string{"", "bytes=4-8"} {
			t.Run(method+"/"+byteRange, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					blocks, _ := readerTestBlocks(t)
					spool := (&localResource{storage: blocks}).makeSpool(nil)
					defer spool.release()
					ctx, cancel := context.WithCancelCause(context.Background())
					defer cancel(nil)
					reader := spool.reader(ctx, blocks.size)
					defer reader.Close()
					cancel(blocksched.ErrExpired)
					synctest.Wait() // Ensure the local reader has closed before Seek.

					request := httptest.NewRequest(method, "/video.mp4", nil)
					request.Header.Set("Range", byteRange)
					response := httptest.NewRecorder()
					pending := serveStream(response, request, video{size: blocks.size}, reader)
					if pending.committed {
						t.Fatalf("ServeContent committed a response before stream error handling: status=%d body=%q", response.Code, response.Body.String())
					}
					if method == http.MethodGet {
						if !errors.Is(reader.err, blocksched.ErrExpired) || cacheErrorStatus(reader.err) != http.StatusGatewayTimeout {
							t.Fatal("GET lost its expiry cause", reader.err)
						}
					} else {
						pending.commit()
						want := http.StatusOK
						if byteRange != "" {
							want = http.StatusPartialContent
						}
						if reader.err != nil || response.Code != want || response.Body.Len() != 0 {
							t.Fatal("HEAD could not read immutable metadata", reader.err, response.Code, response.Body.String())
						}
					}
				})
			})
		}
	}
}
