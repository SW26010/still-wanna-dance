package console

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
	"still-wanna-dance/internal/vrclog"
)

//go:embed index.html
var page string

//go:embed about.html
var aboutPage string

//go:embed assets/console.css assets/console.js assets/cache.js assets/navigation.js
var assets embed.FS

type Settings struct {
	ThroughputIntervalMinutes int    `json:"throughputIntervalMinutes"`
	AutoStartQueue            bool   `json:"autoStartQueue"`
	RequestRetentionDays      int    `json:"requestRetentionDays"`
	AutoStartCDN              bool   `json:"autoStartCDN"`
	QueuePrefetchCount        int    `json:"queuePrefetchCount"`
	DownloadUpstream          string `json:"downloadUpstream"`
	UpstreamMode              string `json:"upstreamMode"`
	SOCKS5Address             string `json:"socks5Address"`
	SOCKS5Username            string `json:"socks5Username"`
	SOCKS5Password            string `json:"socks5Password,omitempty"`
	MaxCacheBytes             int64  `json:"maxCacheBytes"`
	StorageDir                string `json:"storageDir"`
	LogDir                    string `json:"logDir"`
	ManualLogDir              bool   `json:"manualLogDir"`
}

type Console struct {
	exitRequested        chan struct{}
	restartRequested     chan struct{}
	exitOnce             sync.Once
	terms                termsReceipt // protected by mu; separate from editable settings
	termsPageID          string
	termsExitTimer       *time.Timer
	termsExitGeneration  uint64     // invalidates callbacks already waiting for mu
	activationMu         sync.Mutex // rejects duplicate wizard submissions, including across tabs
	activation           Activation // protected by mu; never persisted
	activationGeneration uint64     // protected by mu; distinguishes same-clock-tick retries
	// Acquire lifecycleMu before mu; state readers never wait on lifecycleMu.
	lifecycleMu         sync.Mutex
	queueUpdateMu       sync.Mutex // acquire before mu; serializes engine queue protection
	taskMu              sync.Mutex
	inventoryMu         sync.Mutex
	inventory           Inventory
	inventorySettings   Settings
	inventoryGeneration uint64
	inventoryDone       chan struct{}
	inventoryCancel     context.CancelFunc // protected by inventoryMu
	mu                  sync.Mutex
	settings            Settings // immutable active configuration for this Console
	savedSettings       Settings // last successfully persisted configuration
	configPath          string
	token               string
	address             string
	videoAddress        string
	httpsAddress        string
	https               *httpsRelay
	service             *cacheproxy.Server
	httpServer          *http.Server
	videoListener       net.Listener
	cdnError            string
	actionErrors        map[string]string
	batch               Batch
	lastBatch           Batch
	importStatus        ImportStatus
	importReceipts      []cacheproxy.ImportedVideo
	importCancel        context.CancelFunc
	importDone          chan struct{}
	batchCancel         context.CancelFunc
	batchDone           chan struct{}
	queue               QueueStatus
	queueDesired        bool // user intent; preserved while batch temporarily pauses queue
	queueCancel         context.CancelFunc
	queueDone           chan struct{}
	closing             bool
	client              *http.Client
	checksumURL         string
	apiBase             string
	dns                 *directDNS
	upstreamDial        upstreamDialFunc
	requestRevision     uint64
	monitor             *upstreamstate.Monitor
	monitorRequested    bool
	monitorManual       bool
	monitorManualAt     time.Time
	monitorManualDone   chan struct{}
}

func New(configPath, address string) (*Console, error) {
	configPath, err := filepath.Abs(configPath)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	c := &Console{configPath: configPath, address: address, videoAddress: "127.0.0.1:80", httpsAddress: "127.0.0.1:443", token: hex.EncodeToString(b), apiBase: "https://api.udon.dance", checksumURL: "https://x.kiva.moe/api/v2/wanna/songs"}
	c.settings = Settings{StorageDir: "still-wanna-dance-data", RequestRetentionDays: 30}
	c.exitRequested = make(chan struct{})
	c.restartRequested = make(chan struct{})
	c.dns = &directDNS{}
	data, _, err := readConfigFile(configPath)
	if err != nil {
		return nil, err
	}
	if data != nil {
		if err := json.Unmarshal(data, &c.settings); err != nil {
			return nil, fmt.Errorf("读取控制台配置：%w", err)
		}
	}
	c.settings, err = c.resolveSettings(c.settings)
	c.savedSettings = c.settings
	if err == nil {
		err = c.loadTerms()
	}
	if err == nil {
		c.upstreamDial, c.client, err = c.networkFor(c.settings)
	}
	if err == nil && data == nil {
		err = c.writeConfigFile(c.settings)
	}
	if err == nil {
		c.loadSnapshots()
		c.requestRevision = upstreamrequest.Default.Publish(c.client.Transport)
	}
	return c, err
}

// Relative paths belong to the configuration, independent of the launch directory.
func (c *Console) resolveSettings(s Settings) (Settings, error) {
	for _, p := range []*string{&s.StorageDir, &s.LogDir} {
		if strings.TrimSpace(*p) != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(filepath.Dir(c.configPath), *p)
		}
	}
	return absoluteSettings(s)
}

// Keep directories inside the portable folder movable; external stores stay absolute.
func (c *Console) storedSettings(s Settings) Settings {
	if !s.ManualLogDir {
		s.LogDir = ""
	}
	base := filepath.Dir(c.configPath)
	for _, p := range []*string{&s.StorageDir, &s.LogDir} {
		if pathContains(base, *p) {
			if rel, err := filepath.Rel(base, *p); err == nil {
				*p = rel
			}
		}
	}
	return s
}

func absoluteSettings(s Settings) (Settings, error) {
	if s.ThroughputIntervalMinutes == 0 {
		s.ThroughputIntervalMinutes = 20
	}
	if s.ThroughputIntervalMinutes < 1 || s.ThroughputIntervalMinutes > 1440 {
		return s, errors.New("自动吞吐检测间隔必须为 1～1440 分钟")
	}
	if s.UpstreamMode == "" {
		s.UpstreamMode = "direct"
	}
	if s.UpstreamMode != "direct" && s.UpstreamMode != "socks5" && s.UpstreamMode != "auto" {
		return s, errors.New("上游连接必须是 direct、socks5 或 auto")
	}
	s.SOCKS5Address = strings.TrimSpace(s.SOCKS5Address)
	if s.UpstreamMode == "socks5" || s.UpstreamMode == "auto" {
		if err := validateSOCKS5Address(s.SOCKS5Address); err != nil {
			return s, err
		}
		if err := validateSOCKS5Credentials(s.SOCKS5Username, s.SOCKS5Password); err != nil {
			return s, err
		}
	}
	if s.QueuePrefetchCount == 0 {
		s.QueuePrefetchCount = 3
	}
	if s.QueuePrefetchCount < 1 || s.QueuePrefetchCount > 100 {
		return s, errors.New("队列预缓存数量必须为 1～100")
	}
	if s.DownloadUpstream == "" {
		s.DownloadUpstream = "auto"
	}
	if s.DownloadUpstream != "auto" && s.DownloadUpstream != "cf" && s.DownloadUpstream != "hkg" {
		return s, errors.New("回源线路必须是 auto、cf 或 hkg")
	}
	if s.RequestRetentionDays < 0 || s.RequestRetentionDays > 36500 {
		return s, errors.New("请求明细保留天数必须为 0～36500")
	}
	if s.MaxCacheBytes < 0 {
		return s, errors.New("缓存上限不能为负数")
	}
	if !s.ManualLogDir {
		s.LogDir = defaultLogDir()
	} else if strings.TrimSpace(s.LogDir) == "" {
		return s, errors.New("请填写手动指定的 VRChat 日志目录")
	}
	var logErr error
	s.LogDir, logErr = filepath.Abs(s.LogDir)
	if logErr != nil {
		return s, logErr
	}
	if strings.TrimSpace(s.StorageDir) == "" {
		return s, errors.New("请填写存储目录")
	}
	var err error
	s.StorageDir, err = filepath.Abs(s.StorageDir)
	return s, err
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func writableDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(path, ".stepstash-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	err = f.Close()
	removeErr := os.Remove(name)
	if err != nil {
		return err
	}
	return removeErr
}

func (c *Console) save(s Settings) error {
	return c.saveSettings(s, false)
}

// An omitted password in the settings API preserves the stored secret. Merge
// under the lifecycle lock so concurrent saves cannot restore stale secrets.
func (c *Console) saveSettings(s Settings, preservePassword bool) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return errors.New("控制台正在退出")
	}
	oldSettings := c.savedSettings
	c.mu.Unlock()
	if preservePassword {
		s.SOCKS5Password = oldSettings.SOCKS5Password
	}
	var err error
	s, err = c.resolveSettings(s)
	if err != nil {
		return err
	}
	if err = writableDir(s.StorageDir); err != nil {
		return fmt.Errorf("目录不可写：%w", err)
	}
	if err = c.writeConfigFile(s); err != nil {
		return err
	}
	c.mu.Lock()
	c.savedSettings = s
	c.mu.Unlock()
	slog.Info("settings_saved", "storage_dir", s.StorageDir, "vrchat_log_dir", s.LogDir)
	return nil
}

// Call with lifecycleMu and mu held. Release mu while initializing the shared engine.
func (c *Console) ensureEngine() error {
	if c.service != nil {
		return nil
	}
	c.mu.Unlock()
	defer c.mu.Lock()
	if err := writableDir(c.settings.StorageDir); err != nil {
		return fmt.Errorf("存储目录不可写：%w", err)
	}
	cfg := cacheproxy.DefaultConfig()
	cfg.BeginVideoRequest = c.beginVideoRequest
	cfg.BeginResourceLoad = upstreamrequest.Default.BeginResourceLoad
	cfg.IsResourceHost = upstreamrequest.Default.IsResourceDomain
	cfg.Logger = slog.Default().With("component", "cache")
	cfg.StorageDir = c.settings.StorageDir
	cfg.MaxCacheBytes = c.settings.MaxCacheBytes
	cfg.RequestRetentionDays = c.settings.RequestRetentionDays
	cfg.DialContext = c.upstreamDial
	mode := c.settings.DownloadUpstream
	if mode == "cf" {
		cfg.PlaybackNode = "cf"
	} else if mode == "hkg" {
		cfg.PlaybackNode = "nya"
	}
	cfg.ResolvePlayback = func(ctx context.Context, id, node string) (string, error) {
		return c.resolvePlayback(ctx, id, node, mode)
	}
	cfg.ResourceTransports = func(target string) []http.RoundTripper {
		var transports []http.RoundTripper
		for _, selection := range c.operationSelections(upstreamstate.Resource, upstreamstate.Constraints{Target: target}) {
			transports = append(transports, selection.Channel.Transport)
			if len(transports) == 4 {
				break
			}
		}
		return transports
	}
	s, err := cacheproxy.New(cfg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.service = s
	c.mu.Unlock()
	return nil
}

// AutoStart applies independent startup preferences after consent.
func (c *Console) AutoStart() { c.autoStart(readHostsStatus) }

func (c *Console) autoStart(inspect func() HostsStatus) {
	if !c.termsAccepted() {
		return
	}
	c.mu.Lock()
	enabled := c.settings.AutoStartCDN
	queueEnabled := c.settings.AutoStartQueue
	c.mu.Unlock()
	if queueEnabled {
		c.recordActionError("/api/queue/start", c.startQueue())
	}
	if !enabled {
		return
	}
	if err := c.start(); err != nil {
		return
	}
	if hosts := inspect(); hosts.NeedsMigration {
		c.mu.Lock()
		c.activation = Activation{Phase: "migration_required", Error: hosts.Message}
		c.mu.Unlock()
		slog.Warn("hosts_migration_required", "message", hosts.Message)
	}
}

func (c *Console) start() (err error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.startLocked()
}

func (c *Console) startLocked() (err error) {
	if !c.termsAccepted() {
		return errors.New("请先打开控制台阅读并同意使用条款")
	}
	defer func() {
		if err != nil {
			c.mu.Lock()
			c.cdnError = err.Error()
			c.mu.Unlock()
			slog.Error("cdn_start_failed", "error", err)
		}
	}()
	c.mu.Lock()
	closing, running := c.closing, c.httpServer != nil
	c.mu.Unlock()
	if closing {
		return errors.New("控制台正在退出")
	}
	if running {
		return nil
	}
	// Hosts do not affect binding: a running CDN can be connected using the separate hosts action.
	l, err := net.Listen("tcp4", c.videoAddress)
	if err != nil {
		return desktop.PortError(c.videoAddress, err)
	}
	secure, err := net.Listen("tcp4", c.httpsAddress)
	if err != nil {
		l.Close()
		return fmt.Errorf("网页 HTTPS 转发启动失败：%w", desktop.PortError(c.httpsAddress, err))
	}
	c.mu.Lock()
	err = c.ensureEngine()
	c.mu.Unlock()
	if err != nil {
		l.Close()
		secure.Close()
		return err
	}
	h := &http.Server{Handler: c.service, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)}
	c.mu.Lock()
	c.httpServer = h
	c.videoListener = l
	relay := newHTTPSRelay(secure, c.upstreamDial)
	relay.beginResourceLoad = upstreamrequest.Default.BeginResourceLoad
	c.https = relay
	c.cdnError = ""
	c.mu.Unlock()
	slog.Info("cdn_started", "address", l.Addr().String(), "https_address", secure.Addr().String())
	go func() {
		if err := relay.serve(); err != nil && !errors.Is(err, net.ErrClosed) {
			c.failCDN(h, err)
		}
	}()
	go func() {
		err := h.Serve(l)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.failCDN(h, err)
		}
	}()
	return nil
}

// Both listeners are one service: never report a healthy half-started CDN.
func (c *Console) failCDN(h *http.Server, err error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.httpServer == h {
		slog.Error("cdn_failed", "error", err)
		c.cdnError = err.Error()
		c.stopLocked()
	}
}

func (c *Console) stop() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked()
	c.cdnError = ""
	return nil
}

func (c *Console) stopLocked() {
	c.activation.FirstRequest = time.Time{}
	if !c.activation.Started.IsZero() {
		c.activation.Phase = "stopped"
	}
	listener, relay, server := c.videoListener, c.https, c.httpServer
	c.videoListener, c.https, c.httpServer = nil, nil, nil
	c.mu.Unlock()
	defer c.mu.Lock()
	if listener != nil {
		listener.Close()
	}
	if relay != nil {
		relay.close()
	}
	if server == nil {
		return
	}
	_ = server.Close()
	slog.Info("cdn_stopped")
}

// ExitRequested lets the application owner run the same cleanup as tray exit.
func (c *Console) ExitRequested() <-chan struct{} { return c.exitRequested }

// RestartRequested is handled by the application owner after full cleanup.
func (c *Console) RestartRequested() <-chan struct{} { return c.restartRequested }

// BeginShutdown rejects new work and cancels background tasks without waiting
// for them. The owner calls it before draining HTTP requests, then calls Close
// after closing HTTP connections to wait for tasks and flush the cache engine.
// It is safe to call repeatedly.
func (c *Console) BeginShutdown() {
	c.lifecycleMu.Lock()
	c.mu.Lock()
	c.closing = true
	c.cancelTermsExitLocked()
	c.queueDesired = false
	upstreamrequest.Default.Release(c.requestRevision)
	if c.batchCancel != nil {
		c.batchCancel()
	}
	if c.importCancel != nil {
		c.importCancel()
	}
	if c.queueCancel != nil {
		c.queueCancel()
	}
	c.mu.Unlock()
	c.inventoryMu.Lock()
	if c.inventoryCancel != nil {
		c.inventoryCancel()
	}
	c.inventoryMu.Unlock()
	c.lifecycleMu.Unlock()
}

func (c *Console) Close() error {
	c.BeginShutdown()
	c.mu.Lock()
	monitor := c.monitor
	monitorManualDone := c.monitorManualDone
	done, importDone, queueDone := c.batchDone, c.importDone, c.queueDone
	c.mu.Unlock()
	c.inventoryMu.Lock()
	inventoryDone := c.inventoryDone
	c.inventoryMu.Unlock()
	err := c.stop()
	if monitor != nil {
		monitor.Close()
	}
	if monitorManualDone != nil {
		<-monitorManualDone
	}
	if inventoryDone != nil {
		<-inventoryDone
	}
	if done != nil {
		<-done
	}
	if queueDone != nil {
		<-queueDone
	}
	if importDone != nil {
		<-importDone
	}
	c.lifecycleMu.Lock()
	c.mu.Lock()
	service := c.service
	c.service = nil
	c.mu.Unlock()
	if service != nil {
		err = service.Close()
	}
	c.lifecycleMu.Unlock()
	c.client.CloseIdleConnections()
	if tr, ok := c.client.Transport.(interface{ Retire() }); ok {
		tr.Retire()
	}
	if c.dns != nil {
		c.dns.close()
	}
	return err
}

func (c *Console) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	// Reject DNS rebinding and cross-origin writes; no external page can obtain the token.
	if r.Host != c.address {
		http.Error(w, "invalid host", 403)
		return
	}
	if c.serveTerms(w, r) {
		return
	}
	if r.Method == "GET" && r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, strings.ReplaceAll(page, "__TOKEN__", c.token))
		return
	}
	if r.Method == "GET" && r.URL.Path == "/about" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, aboutPage)
		return
	}
	if r.Method == "GET" && (r.URL.Path == "/assets/console.css" || r.URL.Path == "/assets/console.js" || r.URL.Path == "/assets/cache.js" || r.URL.Path == "/assets/navigation.js") {
		content, err := assets.ReadFile(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}
		contentType := "text/css; charset=utf-8"
		if strings.HasSuffix(r.URL.Path, ".js") {
			contentType = "text/javascript; charset=utf-8"
		}
		w.Header().Set("Content-Type", contentType)
		w.Write(content)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/identity" {
		writeJSON(w, desktop.AppIdentity)
		return
	}
	if r.URL.Path == "/api/status" && r.Method == "GET" {
		c.mu.Lock()
		running := c.httpServer != nil
		result := struct {
			Running           bool                    `json:"running"`
			Settings          Settings                `json:"settings"`
			ActiveSettings    Settings                `json:"activeSettings"`
			RestartRequired   bool                    `json:"restartRequired"`
			Hosts             HostsStatus             `json:"hosts"`
			PortOK            bool                    `json:"portOK"`
			CDNError          string                  `json:"cdnError"`
			ActionErrors      map[string]string       `json:"actionErrors"`
			Batch             Batch                   `json:"batch"`
			Import            ImportStatus            `json:"import"`
			PortOwner         *desktop.Owner          `json:"portOwner,omitempty"`
			Queue             QueueStatus             `json:"queue"`
			LastBatch         Batch                   `json:"lastBatch"`
			HTTPSPortOK       bool                    `json:"httpsPortOK"`
			HTTPSPortOwner    *desktop.Owner          `json:"httpsPortOwner,omitempty"`
			Traffic           cacheproxy.TrafficStats `json:"traffic"`
			SOCKS5PasswordSet bool                    `json:"socks5PasswordSet"`
			Activation        Activation              `json:"activation"`
			DefaultLogDir     string                  `json:"defaultLogDir"`
			UpstreamMonitor   upstreamstate.Status    `json:"upstreamMonitor"`
		}{
			Running: running, Settings: c.savedSettings, ActiveSettings: c.settings,
			RestartRequired: c.savedSettings != c.settings,
			PortOK:          running, HTTPSPortOK: running, CDNError: c.cdnError,
			Import: c.importStatus, Batch: c.batch, Queue: c.queue, LastBatch: c.lastBatch,
			SOCKS5PasswordSet: c.savedSettings.SOCKS5Password != "",
			Activation:        c.activation, DefaultLogDir: defaultLogDir(),
			UpstreamMonitor: c.monitorSnapshotLocked(),
		}
		result.Settings.SOCKS5Password = ""
		result.ActiveSettings.SOCKS5Password = ""
		result.Queue.Desired = c.queueDesired
		service := c.service
		result.ActionErrors = make(map[string]string, len(c.actionErrors))
		for source, message := range c.actionErrors {
			result.ActionErrors[source] = message
		}
		result.Queue.Songs = append([]vrclog.Song(nil), c.queue.Songs...)
		result.Queue.Active = append([]int64(nil), c.queue.Active...)
		result.Queue.Failures = append([]Failure(nil), c.queue.Failures...)
		result.Batch.Failures = append([]Failure(nil), c.batch.Failures...)
		c.mu.Unlock()
		result.Hosts = readHostsStatus()
		if service != nil {
			result.Traffic = service.TrafficStats()
		} else {
			result.Traffic = cacheproxy.ReadTrafficStats(result.ActiveSettings.StorageDir)
		}
		if !running {
			result.PortOK = portAvailable(c.videoAddress) == nil
			if !result.PortOK {
				result.PortOwner = desktop.PortOwner(c.videoAddress)
			}
			result.HTTPSPortOK = portAvailable(c.httpsAddress) == nil
			if !result.HTTPSPortOK {
				result.HTTPSPortOwner = desktop.PortOwner(c.httpsAddress)
			}
		}
		writeJSON(w, result)
		return
	}
	if r.URL.Path == "/api/inventory" && r.Method == "GET" {
		writeJSON(w, c.localInventory())
		return
	}
	if r.URL.Path == "/api/cache" || strings.HasPrefix(r.URL.Path, "/api/cache/") {
		c.cacheAPI(w, r)
		return
	}
	if r.URL.Path == "/api/downloads" && r.Method == "GET" {
		if r.Header.Get("X-StepStash-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
			http.Error(w, "invalid origin or token", http.StatusForbidden)
			return
		}
		// A Go reference keeps the engine object alive, not its database. The
		// snapshot reader handles concurrent Close without joining lifecycle I/O.
		c.mu.Lock()
		service, running := c.service, c.httpServer != nil
		c.mu.Unlock()
		result := struct {
			cacheproxy.ActiveDownloads
			Running bool `json:"running"`
		}{cacheproxy.ActiveDownloads{Tasks: []cacheproxy.ActiveDownload{}}, running}
		if service != nil {
			var err error
			result.ActiveDownloads, err = service.ActiveDownloads(r.Context())
			c.mu.Lock()
			if c.service != service {
				result.ActiveDownloads = cacheproxy.ActiveDownloads{Tasks: []cacheproxy.ActiveDownload{}}
				err = nil // Never publish tasks from a replaced storage engine.
			}
			result.Running = c.httpServer != nil
			c.mu.Unlock()
			if err != nil {
				http.Error(w, "活动下载读取失败，请稍后重试", http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, result)
		return
	}
	if r.URL.Path == "/api/requests" && r.Method == "GET" {
		if r.Header.Get("X-StepStash-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
			http.Error(w, "invalid origin or token", http.StatusForbidden)
			return
		}
		limit := 50
		if value := r.URL.Query().Get("limit"); value != "" {
			var err error
			limit, err = strconv.Atoi(value)
			if err != nil || limit < 1 || limit > cacheproxy.MaxRecentRequests {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
		}
		// Serialize with storage switches, without starting a cache engine.
		c.lifecycleMu.Lock()
		c.mu.Lock()
		root := c.settings.StorageDir
		c.mu.Unlock()
		result, err := cacheproxy.ReadRecentRequests(r.Context(), root, limit)
		c.lifecycleMu.Unlock()
		if err != nil {
			http.Error(w, "最近请求读取失败，请稍后重试", http.StatusInternalServerError)
			return
		}
		writeJSON(w, result)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "not found", 404)
		return
	}
	if r.Header.Get("X-StepStash-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
		http.Error(w, "invalid origin or token", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var err error
	switch r.URL.Path {
	case "/api/upstream/check":
		err = c.requestMonitorCheck()
	case "/api/upstream/check/catalog":
		err = c.requestMonitorCheck(upstreamstate.CheckCatalog)
	case "/api/upstream/check/playback":
		err = c.requestMonitorCheck(upstreamstate.CheckPlayback)
	case "/api/upstream/check/latency":
		err = c.requestMonitorCheck(upstreamstate.CheckLatency)
	case "/api/upstream/check/throughput":
		err = c.requestMonitorCheck(upstreamstate.CheckThroughput)
	case "/api/exit", "/api/restart":
		// Deliver the acknowledgement before the owner closes the HTTP listener.
		writeJSON(w, map[string]bool{"ok": true})
		_ = http.NewResponseController(w).Flush()
		c.exitOnce.Do(func() {
			slog.Info("console_action_completed", "action", r.URL.Path)
			if r.URL.Path == "/api/restart" {
				close(c.restartRequested)
			} else {
				close(c.exitRequested)
			}
		})
		return
	case "/api/import/start":
		var input struct {
			Source string `json:"source"`
		}
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			err = c.startImport(input.Source)
		}
	case "/api/import/cleanup":
		var input struct {
			ID        uint64 `json:"id"`
			Confirmed bool   `json:"confirmed"`
		}
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			err = c.cleanupImport(input.ID, input.Confirmed)
		}
	case "/api/inventory/scan":
		c.startInventoryScan()
	case "/api/batch/scan":
		err = c.startBatchMode(true)
	case "/api/batch/switch":
		err = c.switchTask()
	case "/api/start":
		err = c.start()
	case "/api/activation/enable":
		err = c.enableAcceleration(readHostsStatus, changeHosts)
	case "/api/stop":
		err = c.stop()
	case "/api/queue/start":
		err = c.startQueue()
	case "/api/queue/stop":
		err = c.stopQueue()
	case "/api/settings":
		var input struct {
			Settings
			Password *string `json:"socks5Password"`
		}
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			if input.Password != nil {
				input.Settings.SOCKS5Password = *input.Password
			}
			err = c.saveSettings(input.Settings, input.Password == nil)
		}
	case "/api/hosts/enable":
		err = c.changeHosts("enable")
	case "/api/hosts/disable":
		err = c.changeHosts("disable")
	case "/api/batch/start":
		err = c.startBatch()
	case "/api/batch/cancel":
		c.mu.Lock()
		if c.batchCancel != nil {
			c.batchCancel()
		}
		c.mu.Unlock()
	default:
		http.Error(w, "not found", 404)
		return
	}
	c.recordActionError(r.URL.Path, err)
	if err != nil {
		slog.Error("console_action_failed", "action", r.URL.Path, "error", err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(400)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("console_action_completed", "action", r.URL.Path)
	writeJSON(w, map[string]bool{"ok": true})
}

// Each operation group owns its error; unrelated successes must not erase it.
// CDN errors are owned by start/stop/failCDN, including automatic and tray calls.
func (c *Console) recordActionError(path string, err error) {
	source := strings.Split(strings.TrimPrefix(path, "/api/"), "/")[0]
	if source == "start" || source == "stop" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		delete(c.actionErrors, source)
	} else {
		if c.actionErrors == nil {
			c.actionErrors = make(map[string]string)
		}
		c.actionErrors[source] = err.Error()
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
