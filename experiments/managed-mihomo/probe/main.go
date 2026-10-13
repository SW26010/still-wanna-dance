//go:build historical_a

// Development-only black-box driver. Not imported by a product entry point.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"still-wanna-dance/internal/console"
	"still-wanna-dance/internal/proxyengine/mihomo"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

type endpoint struct {
	Address, Username, Password string
	Parameters                  map[string]any
}
type command struct {
	Mode, URL             string
	Endpoints             []endpoint
	Iterations, TimeoutMS int
	Concurrent, Retire    bool
	Monitor               bool
}
type observation struct {
	Index       int     `json:"index"`
	OK          bool    `json:"ok"`
	Status      int     `json:"status"`
	Bytes       int64   `json:"bytes"`
	SHA256      string  `json:"sha256,omitempty"`
	Marker      string  `json:"marker,omitempty"`
	Error       string  `json:"error,omitempty"`
	FirstByteMS float64 `json:"firstByteMS"`
	ElapsedMS   float64 `json:"elapsedMS"`
}

func main() {
	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 4096), 2<<20)
	if !scan.Scan() {
		return
	}
	var init command
	if json.Unmarshal(scan.Bytes(), &init) != nil {
		return
	}
	var proxies []upstreamrequest.Proxy
	var nodes []*mihomo.Node
	for i, ep := range init.Endpoints {
		id := fmt.Sprintf("stable-v1-%064x", i+1)
		var dial upstreamrequest.DialFunc
		if init.Mode == "embedded" {
			n, err := mihomo.New(mihomo.Config{ID: id, Parameters: ep.Parameters}, mihomo.Options{LookupIP: func(ctx context.Context, host string) ([]netip.Addr, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if host != "proxy.invalid" {
					return nil, errors.New("fixture DNS only")
				}
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			}})
			if err != nil {
				fmt.Println(`{"ready":false}`)
				return
			}
			nodes = append(nodes, n)
			dial = n.Proxy().Dial
		} else {
			d, err := console.NewAuthenticatedSOCKS5Dialer(ep.Address, ep.Username, ep.Password)
			if err != nil {
				fmt.Println(`{"ready":false}`)
				return
			}
			dial = d
		}
		proxies = append(proxies, upstreamrequest.Proxy{ID: id, Dial: dial})
	}
	pool, err := upstreamrequest.NewMultiPool("socks5", nil, proxies)
	if err != nil {
		fmt.Println(`{"ready":false}`)
		return
	}
	defer pool.Close()
	defer func() {
		for _, n := range nodes {
			n.Close()
		}
	}()
	json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": true, "channels": len(proxies)})
	var previous []upstreamrequest.Candidate
	for scan.Scan() {
		var cmd command
		if json.Unmarshal(scan.Bytes(), &cmd) != nil {
			return
		}
		if cmd.Retire {
			pool.Close()
			ok := len(pool.Current(cmd.URL)) == 0
			for _, c := range previous {
				r, _ := http.NewRequest("GET", cmd.URL, nil)
				_, err := c.Transport.RoundTrip(r)
				ok = ok && errors.Is(err, upstreamrequest.ErrUnavailable)
			}
			json.NewEncoder(os.Stdout).Encode(map[string]any{"retired": ok})
			continue
		}
		if cmd.Monitor {
			channel := upstreamrequest.NewChannel()
			channel.Publish(&upstreamrequest.Transport{Pool: pool})
			m, err := upstreamstate.NewMonitor(upstreamstate.Options{Channel: channel, Policy: upstreamstate.Policy{RequestTimeout: 10 * time.Second, ResourceTimeout: 3 * time.Second, ResourceMaxBytes: 4 << 20}})
			if err != nil {
				fmt.Println(`{"monitorReady":false}`)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
			err = m.CheckSelected(ctx, upstreamstate.CheckPlayback)
			if err == nil {
				err = m.CheckSelected(ctx, upstreamstate.CheckThroughput)
			}
			cancel()
			var results []map[string]any
			for _, r := range m.Snapshot().Results {
				index := -1
				for i, p := range proxies {
					if strings.HasSuffix(r.ChannelID, "/"+p.ID) {
						index = i
					}
				}
				// Never output raw URLs, node names or upstream errors.
				results = append(results, map[string]any{"operation": r.Operation, "state": r.State, "http": r.HTTP, "index": index, "bytes": r.TransferredBytes, "latencyMS": r.EstimatedLatencyMS, "speedBPS": r.EstimatedSpeedBPS})
			}
			selected, ok := m.Recommended(upstreamstate.PlaybackURL)
			businessStatus := 0
			if ok {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s&id=%d", selected.Result.Entry, selected.Result.SampleSongID), nil)
				resp, requestErr := (&http.Client{Transport: selected.Channel.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
				if requestErr == nil {
					businessStatus = resp.StatusCode
					resp.Body.Close()
				}
				cancel()
			}
			m.Close()
			json.NewEncoder(os.Stdout).Encode(map[string]any{"monitorReady": true, "completed": err == nil, "results": results, "businessUsesSelectedTransport": ok, "businessStatus": businessStatus})
			continue
		}
		cs := pool.Current(cmd.URL)
		previous = cs
		if cmd.Iterations < 1 {
			cmd.Iterations = 1
		}
		if cmd.TimeoutMS < 1 {
			cmd.TimeoutMS = 3000
		}
		results := make([]observation, len(cs)*cmd.Iterations)
		var wg sync.WaitGroup
		for i := range results {
			fn := func(i int) {
				defer wg.Done()
				idx := i % len(cs)
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cmd.TimeoutMS)*time.Millisecond)
				defer cancel()
				start := clockSeconds()
				o := observation{Index: idx}
				ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotFirstResponseByte: func() { o.FirstByteMS = (clockSeconds() - start) * 1000 }})
				r, _ := http.NewRequestWithContext(ctx, "GET", cmd.URL, nil)
				resp, err := (&http.Client{Transport: cs[idx].Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(r)
				if err == nil {
					o.Status = resp.StatusCode
					// Only the local fixture can emit these two public marker values.
					if v := resp.Header.Get("X-SWD-Fixture"); v == "A" || v == "B" {
						o.Marker = v
					}
					h := sha256.New()
					o.Bytes, err = io.Copy(h, io.LimitReader(resp.Body, 64<<20))
					resp.Body.Close()
					o.SHA256 = hex.EncodeToString(h.Sum(nil))
				}
				o.OK = err == nil && o.Status == 200
				if err != nil {
					var ne net.Error
					switch {
					case ctx.Err() != nil:
						o.Error = "deadline-or-cancel"
					case errors.Is(err, upstreamrequest.ErrUnavailable):
						o.Error = "retired"
					case errors.As(err, &ne) && ne.Timeout():
						o.Error = "timeout"
					default:
						o.Error = "transport-failed"
					}
				}
				o.ElapsedMS = (clockSeconds() - start) * 1000
				results[i] = o
			}
			wg.Add(1)
			if cmd.Concurrent {
				go fn(i)
			} else {
				fn(i)
			}
		}
		wg.Wait()
		json.NewEncoder(os.Stdout).Encode(map[string]any{"observations": results})
	}
}
