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
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"still-wanna-dance/internal/upstreamnet"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

type endpoint struct {
	Address, Username, Password string
	Parameters                  map[string]any
}
type command struct {
	Directed, ControlURL                           string
	DialTimeoutMS                                  int
	SwapNames, CloseIdle                           bool
	Bindings, RawDial                              bool
	Address                                        string
	ExportSource                                   bool
	Payload, Home, DNS, Range                      string
	Prepare, Stats, ShutdownOnly, Panic, PreCancel bool
	CancelAfterMS                                  int
	Mode, URL                                      string
	Endpoints                                      []endpoint
	Iterations, TimeoutMS                          int
	Concurrent, Retire                             bool
	Monitor                                        bool
}
type observation struct {
	ConnectionMS float64 `json:"connectionMS"`
	Reused       bool    `json:"reused"`
	Index        int     `json:"index"`
	OK           bool    `json:"ok"`
	Status       int     `json:"status"`
	Bytes        int64   `json:"bytes"`
	SHA256       string  `json:"sha256,omitempty"`
	Marker       string  `json:"marker,omitempty"`
	Error        string  `json:"error,omitempty"`
	FirstByteMS  float64 `json:"firstByteMS"`
	ElapsedMS    float64 `json:"elapsedMS"`
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
	if init.Prepare {
		nodes, err := prepare(init.Payload)
		if err != nil {
			json.NewEncoder(os.Stdout).Encode(map[string]any{"accepted": false, "reason": err.Error()})
			return
		}
		// Private protocol to the runner only; NEVER copy these maps to evidence.
		json.NewEncoder(os.Stdout).Encode(map[string]any{"accepted": true, "nodes": nodes})
		return
	}
	var proxies []upstreamrequest.Proxy
	var backend *fullRuntime
	if init.Mode == "B" || init.Mode == "B2" || init.Mode == "D" {
		var err error
		backend, err = startRuntime(init)
		if err != nil {
			fmt.Println(`{"ready":false}`)
			return
		}
		proxies = backend.proxies
	}
	for i, ep := range init.Endpoints {
		id := fmt.Sprintf("stable-v1-%064x", i+1)
		var dial upstreamrequest.DialFunc
		{
			d, err := upstreamnet.NewAuthenticatedSOCKS5Dialer(ep.Address, ep.Username, ep.Password)
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
	defer func() {
		pool.Close()
		if backend != nil {
			backend.Close()
		}
	}()
	ready := map[string]any{"ready": true, "channels": len(proxies)}
	// Private runner IPC only, never copied into exported evidence.
	if backend != nil && init.Mode == "D" {
		ready["privateEndpoints"] = backend.endpoints()
	}
	json.NewEncoder(os.Stdout).Encode(ready)
	var previous []upstreamrequest.Candidate
	for scan.Scan() {
		var cmd command
		if json.Unmarshal(scan.Bytes(), &cmd) != nil {
			return
		}
		if cmd.Panic {
			go func() { panic("synthetic-core-goroutine-fault") }()
			select {}
		}
		if cmd.Directed != "" && backend != nil {
			json.NewEncoder(os.Stdout).Encode(backend.directed(cmd, pool))
			continue
		}
		if cmd.Bindings && backend != nil {
			json.NewEncoder(os.Stdout).Encode(backend.bindingChecks())
			continue
		}
		if cmd.SwapNames && backend != nil {
			pool.CloseIdleConnections()
			json.NewEncoder(os.Stdout).Encode(map[string]any{"swapped": backend.swapNames()})
			continue
		}
		if cmd.CloseIdle {
			pool.CloseIdleConnections()
		}
		if cmd.ShutdownOnly && backend != nil {
			backend.ShutdownOnly()
		}
		if cmd.Stats || cmd.ShutdownOnly {
			out := map[string]any{"goroutines": runtime.NumGoroutine()}
			if backend != nil {
				out = backend.snapshot()
			}
			json.NewEncoder(os.Stdout).Encode(out)
			continue
		}
		if cmd.Retire {
			pool.Close()
			if backend != nil {
				backend.Close()
			}
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
			out := map[string]any{"monitorReady": true, "completed": err == nil, "results": results, "businessUsesSelectedTransport": ok, "businessStatus": businessStatus}
			if cmd.ExportSource {
				sources := m.ResourceSources("")
				if len(sources) > 0 {
					out["privateResourceURL"] = sources[0].ResourceURL
				}
			}
			m.Close()
			json.NewEncoder(os.Stdout).Encode(out)
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
				if cmd.PreCancel {
					cancel()
				}
				if cmd.CancelAfterMS > 0 {
					timer := time.AfterFunc(time.Duration(cmd.CancelAfterMS)*time.Millisecond, cancel)
					defer timer.Stop()
				}
				start := clockSeconds()
				o := observation{Index: idx}
				var traceMu sync.Mutex
				if cmd.RawDial {
					conn, err := proxies[idx].Dial(ctx, "tcp", cmd.Address)
					if conn != nil {
						conn.Close()
					}
					o.OK = err == nil
					if err != nil {
						o.Error = "dial-failed"
						if ctx.Err() != nil {
							o.Error = "deadline-or-cancel"
						}
					}
					o.ElapsedMS = (clockSeconds() - start) * 1000
					results[i] = o
					return
				}
				ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
					GotConn: func(info httptrace.GotConnInfo) {
						traceMu.Lock()
						defer traceMu.Unlock()
						o.ConnectionMS = (clockSeconds() - start) * 1000
						o.Reused = info.Reused
					},
					GotFirstResponseByte: func() { traceMu.Lock(); defer traceMu.Unlock(); o.FirstByteMS = (clockSeconds() - start) * 1000 },
				})
				r, _ := http.NewRequestWithContext(ctx, "GET", cmd.URL, nil)
				if cmd.Range != "" {
					r.Header.Set("Range", cmd.Range)
				}
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
				o.OK = err == nil && (o.Status == 200 || o.Status == 206)
				if err != nil {
					var ne net.Error
					switch {
					case ctx.Err() != nil:
						o.Error = "deadline-or-cancel"
					case errors.Is(err, upstreamrequest.ErrUnavailable):
						o.Error = "retired"
					case errors.As(err, &ne) && ne.Timeout():
						o.Error = "timeout"
					case errors.Is(err, syscall.ECONNREFUSED):
						o.Error = "connection-refused"
					default:
						o.Error = "transport-failed"
					}
				}
				o.ElapsedMS = (clockSeconds() - start) * 1000
				traceMu.Lock()
				results[i] = o
				traceMu.Unlock()
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
