package console

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"
)

const healthInterval = 5 * time.Minute

type UpstreamHealth struct {
	Mode      string        `json:"mode"`
	Proxy     string        `json:"proxy,omitempty"`
	Running   bool          `json:"running"`
	Started   time.Time     `json:"started"`
	Finished  time.Time     `json:"finished"`
	NextCheck time.Time     `json:"nextCheck"`
	Checks    []HealthCheck `json:"checks"`
}

type HealthCheck struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	State     string    `json:"state"`
	Stage     string    `json:"stage"`
	Message   string    `json:"message"`
	HTTP      int       `json:"http"`
	ElapsedMS int64     `json:"elapsedMS"`
	Checked   time.Time `json:"checked"`
}

func healthTargets(base string) []HealthCheck {
	return []HealthCheck{
		{ID: "api", Name: "API 连接", URL: base + "/cdn-cgi/trace"},
		{ID: "catalog", Name: "歌曲列表", URL: base + "/Api/Songs/list"},
		{ID: "cf", Name: "CF 视频入口", URL: "https://play.udon.dance/cdn-cgi/trace"},
		{ID: "hkg", Name: "HKG 视频入口", URL: "https://nya.xin.moe/"},
	}
}

// One worker owns scheduling. It is independent of browser polling and CDN
// lifecycle, but never contacts upstreams before terms have been accepted.
func (c *Console) StartUpstreamMonitor() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.healthDone != nil || c.closing {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.healthCancel = cancel
	c.healthDone = make(chan struct{})
	c.healthWake = make(chan struct{}, 1)
	go func() {
		defer close(c.healthDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			if c.termsAccepted() {
				c.runHealthCheck(ctx)
			}
			select {
			case <-ctx.Done():
				return
			case <-c.healthWake:
			case <-ticker.C:
			}
		}
	}()
}

// mu must be held. Cancel in-flight requests and discard the old network's
// results immediately, even if its transport completes after cancellation.
func (c *Console) resetHealthLocked() {
	c.healthGeneration++
	if c.healthRunCancel != nil {
		c.healthRunCancel()
	}
	c.health = UpstreamHealth{}
	c.wakeHealthLocked()
}

func (c *Console) wakeHealthLocked() {
	if c.healthWake != nil {
		select {
		case c.healthWake <- struct{}{}:
		default:
		}
	}
}

func (c *Console) requestHealthCheck() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("控制台正在退出")
	}
	if c.health.Running {
		return nil
	}
	if time.Since(c.health.Started) < 10*time.Second {
		return errors.New("检测刚完成，请稍等几秒再重测（两轮开始至少间隔 10 秒）")
	}
	c.health.NextCheck = time.Time{}
	c.wakeHealthLocked()
	return nil
}

func (c *Console) healthSnapshotLocked() UpstreamHealth {
	h := c.health
	h.Mode = c.settings.UpstreamMode
	if h.Mode == "socks5" {
		h.Proxy = c.settings.SOCKS5Address
	}
	h.Checks = append([]HealthCheck(nil), h.Checks...)
	return h
}

func (c *Console) runHealthCheck(parent context.Context) {
	c.mu.Lock()
	if c.closing || parent.Err() != nil || c.health.Running || time.Now().Before(c.health.NextCheck) {
		c.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(parent, 35*time.Second)
	c.healthRunCancel = cancel
	generation, client := c.healthGeneration, c.client
	checks := healthTargets(c.apiBase)
	for i := range checks {
		checks[i].State = "checking"
	}
	c.health = UpstreamHealth{Running: true, Started: time.Now(), Checks: append([]HealthCheck(nil), checks...)}
	c.mu.Unlock()
	defer cancel()
	var wg sync.WaitGroup
	for i, check := range checks {
		wg.Add(1)
		go func(i int, check HealthCheck) {
			defer wg.Done()
			result := probeHealth(ctx, client, check)
			c.mu.Lock()
			defer c.mu.Unlock()
			if generation == c.healthGeneration && !c.closing {
				c.health.Checks[i] = result
			}
		}(i, check)
	}
	wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation == c.healthGeneration {
		c.health.Running = false
		c.health.Finished = time.Now()
		c.health.NextCheck = time.Now().Add(healthInterval)
		c.healthRunCancel = nil
	}
}

func probeHealth(ctx context.Context, client *http.Client, check HealthCheck) (result HealthCheck) {
	result = check
	started := time.Now()
	defer func() {
		result.ElapsedMS = time.Since(started).Milliseconds()
		result.Checked = time.Now()
	}()
	// Trace callbacks can run on transport goroutines. No raw network error is
	// exposed: SOCKS credentials and signed URLs must not reach status JSON.
	var mu sync.Mutex
	stage := "connect"
	setStage := func(s string) { mu.Lock(); stage = s; mu.Unlock() }
	trace := &httptrace.ClientTrace{
		TLSHandshakeStart: func() { setStage("tls") },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				setStage("headers")
			}
		},
		GotConn: func(httptrace.GotConnInfo) { setStage("headers") },
	}
	ctx = httptrace.WithClientTrace(ctx, trace)
	method := http.MethodGet
	if check.ID == "hkg" {
		method = http.MethodHead
	}
	req, err := http.NewRequestWithContext(ctx, method, check.URL, nil)
	if err == nil {
		var resp *http.Response
		resp, err = client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			result.HTTP = resp.StatusCode
			result.Stage = "headers"
			switch {
			case resp.StatusCode == 524:
				result.State, result.Message = "upstream_error", "源站响应超时（Cloudflare 524）"
			case resp.StatusCode >= 500:
				result.State, result.Message = "upstream_error", fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode)
			case check.ID == "hkg":
				result.State, result.Message = "reachable", "已收到 HTTP 响应；仅验证入口连接，不代表视频可播放"
			case resp.StatusCode != http.StatusOK:
				result.State, result.Message = "http_error", fmt.Sprintf("连接已建立，但接口返回 HTTP %d", resp.StatusCode)
			case check.ID == "catalog":
				setStage("body")
				var body []byte
				body, err = io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
				if err == nil {
					result.Stage = "body"
					if len(body) > 16<<20 {
						result.State, result.Message = "invalid", "歌曲列表超过 16 MiB 限制"
					} else if songs, parseErr := parseCatalog(bytes.NewReader(body)); parseErr != nil {
						result.State, result.Message = "invalid", "响应内容不是有效歌曲列表"
					} else {
						result.State, result.Message = "healthy", fmt.Sprintf("歌曲列表可用（%d 首）", len(songs))
					}
				}
			default:
				result.State, result.Message = "reachable", "边缘入口可达；不代表源站接口或视频可用"
			}
			if err == nil {
				return result
			}
		}
	}
	mu.Lock()
	result.Stage = stage
	mu.Unlock()
	var netErr net.Error
	if errors.Is(err, context.Canceled) {
		result.State, result.Message = "canceled", "检测已取消"
	} else if errors.As(err, &netErr) && netErr.Timeout() {
		result.State, result.Message = "timeout", "请求超时；仅凭超时无法区分网络链路与上游故障"
	} else {
		result.State, result.Message = "network_error", "请求失败，请检查当前网络、代理或证书"
	}
	return result
}
