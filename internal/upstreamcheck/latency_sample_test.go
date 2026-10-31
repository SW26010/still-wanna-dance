package upstreamcheck

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLatencyProbeReadsOnlyOneByte(t *testing.T) {
	for _, status := range []int{206, 200} {
		body := strings.NewReader("body must not be downloaded")
		client := requestClient(transportFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Range") != "bytes=0-0" {
				t.Fatal(r.Header)
			}
			return &http.Response{StatusCode: status, Header: http.Header{"Content-Range": []string{"bytes 0-0/131072"}}, Body: io.NopCloser(body)}, nil
		}))
		sample, err := parseSample(videoFixture)
		if err != nil {
			t.Fatal(err)
		}
		o := probeResourceMode(context.Background(), client, DefaultPolicy(), 42, sample, false)
		wantRead := 0
		if status == 206 {
			wantRead = 1
			if o.state != "available" {
				t.Fatal(o)
			}
		} else if o.state == "available" {
			t.Fatal("accepted ignored Range")
		}
		if body.Size()-int64(body.Len()) != int64(wantRead) || o.bytes != 0 || o.transferDuration != 0 {
			t.Fatal("latency probe read or measured throughput", o)
		}
	}
}
