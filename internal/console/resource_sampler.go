package console

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/videometa"
)

type resourceSampleRequest struct {
	Host  string          `json:"host"`
	IP    string          `json:"ip"`
	ctx   context.Context // caller lifetime for a queued manual sample
	reply chan error
}

type resourceSampleRotation struct {
	sequence uint64
	after    cacheproxy.MediaURL
}

// Downloads, calibration and block sampling share the active upstream's history.
func (c *Console) routeConfigLocked() cacheproxy.RouteConfig {
	direct := c.settings.UpstreamMode == "direct"
	if c.downloadRoutes == nil {
		c.downloadRoutes = cacheproxy.NewDownloadRoutes(direct, c.files().State("download-routes"))
	}
	cfg := cacheproxy.RouteConfig{DownloadRoutes: c.downloadRoutes}
	if direct {
		cfg.DirectCandidates, _ = c.client.Transport.(upstreamrequest.Candidates)
	}
	return cfg
}

// Called with c.mu held. One worker owns the timer, target rotation and
// cleanup. Both the gateway and background jobs may run while it samples.
func (c *Console) startResourceSamplerLocked() {
	if c.sampleDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.sampleCancel, c.sampleDone = cancel, done
	requests := make(chan resourceSampleRequest, 1)
	c.sampleRequests = requests
	direct := c.settings.UpstreamMode == "direct"
	client := c.client
	cfg := cacheproxy.DefaultConfig()
	cfg.RouteConfig = c.routeConfigLocked()
	cfg.IsResourceHost = c.resources.IsResourceDomain
	const interval = 30 * time.Second
	go func() {
		defer close(done)
		timer := time.NewTimer(interval)
		defer timer.Stop()
		var rotation resourceSampleRotation
		for {
			var request resourceSampleRequest
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			case request = <-requests:
				if request.ctx.Err() != nil {
					continue
				}
			}
			if direct || !c.resources.ResourceBusy() {
				err := c.sampleResource(ctx, client, cfg, direct, &rotation, request)
				if request.reply != nil {
					request.reply <- err
				}
				rotation.sequence++
			}
			timer.Reset(interval)
		}
	}()
}

func (c *Console) requestResourceSample(ctx context.Context, request resourceSampleRequest) error {
	if request.Host != "" || request.IP != "" {
		ip, err := netip.ParseAddr(request.IP)
		if err != nil || ip.Zone() != "" || !videometa.ValidHost(request.Host) {
			return rejectRequest(errors.New("请选择有效的资源主机和 IP"))
		}
		request.IP = ip.String()
	}
	c.mu.Lock()
	requests, done := c.sampleRequests, c.sampleDone
	available := !c.closing && c.settings.UpstreamMode == "direct" && requests != nil
	c.mu.Unlock()
	if !available {
		return rejectRequest(errors.New("直连资源采样尚未启动"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	request.ctx, request.reply = ctx, make(chan error, 1)
	select {
	case requests <- request:
	default:
		return rejectRequest(errors.New("已有资源采样在等待执行"))
	}
	select {
	case err := <-request.reply:
		if errors.Is(err, cacheproxy.ErrNoSampleDue) {
			if request.IP != "" {
				return rejectRequest(errors.New("该 IP 当前不在有效直连候选中或正在使用"))
			}
			return rejectRequest(errors.New("当前没有可采样的空闲 IP"))
		}
		return err
	case <-done:
		return rejectRequest(errors.New("资源采样已停止"))
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Console) sampleResource(ctx context.Context, client *http.Client, cfg cacheproxy.Config, direct bool, rotation *resourceSampleRotation, request resourceSampleRequest) error {
	if request.ctx != nil {
		// A manual operation ends on either caller cancellation or worker shutdown.
		manual, cancel := context.WithCancel(request.ctx)
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		ctx = manual
	}
	var target string
	var saved cacheproxy.MediaURL
	sources := c.resources.ResourceSources(request.Host)
	if len(sources) > 0 {
		target = sources[rotation.sequence%uint64(len(sources))].ResourceURL
	} else if c.store != nil {
		saved = c.cachedResourceForHost(ctx, request.Host, rotation.after)
		target = saved.URL
	}
	if target == "" {
		return rejectRequest(errors.New("暂无可采样的视频地址"))
	}
	err := c.sampleResourceBlock(ctx, client, cfg, direct, rotation.sequence, request, target)
	if saved.URL == "" || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return err
	}
	// Each automatic direct turn visits one saved address, even when this
	// host has no eligible IP. A proxy cooldown covers every host.
	noRoute := errors.Is(err, cacheproxy.ErrNoSampleDue)
	advance := err != nil && !noRoute
	if request.reply == nil {
		advance = direct || !noRoute
	}
	if advance {
		rotation.after = cacheproxy.MediaURL{MD5: saved.MD5, Node: saved.Node}
	}
	return err
}

func (c *Console) sampleResourceBlock(ctx context.Context, client *http.Client, cfg cacheproxy.Config, direct bool, sequence uint64, request resourceSampleRequest, target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return applog.SafeError(err)
	}
	meta, err := videometa.ValidateURL(u, cfg.MaxFileBytes)
	if err != nil {
		return err
	}
	if !direct {
		idle, finish, err := c.resources.ResourceProbe(ctx)
		if err != nil {
			return err
		}
		defer finish()
		ctx = idle
	}
	// Prefer a full block when the resource has one, for comparable timings.
	blocks := max(1, meta.Size/cacheproxy.DefaultBlockSize)
	block := int64(sequence % uint64(blocks))
	if request.IP != "" {
		if u.Hostname() != request.Host {
			return rejectRequest(errors.New("暂无该主机的可采样视频地址"))
		}
		err = cacheproxy.SampleBlockOnIP(ctx, cfg, client, target, block, request.IP)
	} else {
		err = cacheproxy.SampleBlock(ctx, cfg, client, target, block, request.reply != nil)
	}
	if err != nil && !errors.Is(err, cacheproxy.ErrNoSampleDue) && ctx.Err() == nil {
		slog.Debug("资源块采样未完成", "host", u.Hostname(), "block", block, "error", applog.SafeError(err))
	}
	return applog.SafeError(err)
}
