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

	"stepstash/internal/cacheproxy"
	"stepstash/internal/desktop"
	"stepstash/internal/vrclog"
)

//go:embed index.html
var page string

//go:embed about.html
var aboutPage string

//go:embed assets/console.css assets/console.js assets/cache.js assets/navigation.js
var assets embed.FS

type Settings struct {
	RequestRetentionDays   int    `json:"requestRetentionDays"`
	AutoStartCDN           bool   `json:"autoStartCDN"`
	ScanResolveConcurrency int    `json:"scanResolveConcurrency"`
	ScanCheckConcurrency   int    `json:"scanCheckConcurrency"`
	QueuePrefetchCount     int    `json:"queuePrefetchCount"`
	DownloadUpstream       string `json:"downloadUpstream"`
	UpstreamMode           string `json:"upstreamMode"`
	SOCKS5Address          string `json:"socks5Address"`
	SOCKS5Username         string `json:"socks5Username"`
	SOCKS5Password         string `json:"socks5Password,omitempty"`
	MaxCacheBytes          int64  `json:"maxCacheBytes"`
	StorageDir             string `json:"storageDir"`
	LogDir                 string `json:"logDir"`
}

type Console struct {
	activationMu         sync.Mutex // rejects duplicate wizard submissions, including across tabs
	activation           Activation // protected by mu; never persisted
	activationGeneration uint64     // protected by mu; distinguishes same-clock-tick retries
	// Acquire lifecycleMu before mu; state readers never wait on lifecycleMu.
	lifecycleMu         sync.Mutex
	queueUpdateMu       sync.Mutex // acquire before mu; serializes engine queue protection
	settingsRevision    uint64     // protected by mu; invalidates reads across saves (including ABA)
	taskMu              sync.Mutex
	inventoryMu         sync.Mutex
	inventory           Inventory
	inventorySettings   Settings
	inventoryGeneration uint64
	inventoryDone       chan struct{}
	mu                  sync.Mutex
	settings            Settings
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
	scanPlan            *scanPlan
	batchCancel         context.CancelFunc
	batchDone           chan struct{}
	batchResumeQueue    bool
	queue               QueueStatus
	queueCancel         context.CancelFunc
	queueDone           chan struct{}
	closing             bool
	client              *http.Client
	checksumURL         string
	apiBase             string
	dns                 *directDNS
	upstreamDial        upstreamDialFunc
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
	c.settings = Settings{StorageDir: "stepstash-data", RequestRetentionDays: 30}
	c.dns = &directDNS{}
	if b, err := os.ReadFile(configPath); err == nil {
		if err = json.Unmarshal(b, &c.settings); err != nil {
			return nil, fmt.Errorf("读取控制台配置：%w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	c.settings, err = c.resolveSettings(c.settings)
	if err == nil {
		c.upstreamDial, c.client, err = c.networkFor(c.settings)
	}
	if err == nil {
		c.loadSnapshots()
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
	if s.UpstreamMode == "" {
		s.UpstreamMode = "direct"
	}
	if s.UpstreamMode != "direct" && s.UpstreamMode != "socks5" {
		return s, errors.New("上游连接必须是 direct 或 socks5")
	}
	s.SOCKS5Address = strings.TrimSpace(s.SOCKS5Address)
	if s.UpstreamMode == "socks5" {
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
	if s.ScanResolveConcurrency == 0 {
		s.ScanResolveConcurrency = 4
	}
	if s.ScanCheckConcurrency == 0 {
		s.ScanCheckConcurrency = 1
	}
	if s.ScanResolveConcurrency < 1 || s.ScanResolveConcurrency > 16 {
		return s, errors.New("扫描地址请求并发必须为 1～16")
	}
	if s.ScanCheckConcurrency < 1 || s.ScanCheckConcurrency > 4 {
		return s, errors.New("扫描本地校验并发必须为 1～4")
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
	if strings.TrimSpace(s.LogDir) == "" {
		s.LogDir = defaultLogDir()
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
	if c.httpServer != nil || c.batch.Running || c.queue.Running {
		c.mu.Unlock()
		return errors.New("请先关闭 CDN、队列预缓存和批量任务，再保存设置")
	}
	oldSettings, service := c.settings, c.service
	c.mu.Unlock()
	if preservePassword {
		s.SOCKS5Password = oldSettings.SOCKS5Password
	}
	var err error
	s, err = c.resolveSettings(s)
	if err != nil {
		return err
	}
	networkChanged := oldSettings.UpstreamMode != s.UpstreamMode || oldSettings.SOCKS5Address != s.SOCKS5Address || oldSettings.SOCKS5Username != s.SOCKS5Username || oldSettings.SOCKS5Password != s.SOCKS5Password
	var dial upstreamDialFunc
	var client *http.Client
	if networkChanged {
		dial, client, err = c.networkFor(s)
		if err != nil {
			return err
		}
	}
	if err = writableDir(s.StorageDir); err != nil {
		return fmt.Errorf("目录不可写：%w", err)
	}
	if err = os.MkdirAll(filepath.Dir(c.configPath), 0700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c.storedSettings(s), "", "  ")
	f, err := os.CreateTemp(filepath.Dir(c.configPath), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), c.configPath); err != nil {
		return err
	}
	changedLibrary := !sameLibrary(oldSettings, s)
	var snapshots *Console
	if changedLibrary {
		snapshots = &Console{configPath: c.configPath, settings: s}
		snapshots.loadSnapshots()
	}
	slog.Info("settings_saved", "storage_dir", s.StorageDir, "vrchat_log_dir", s.LogDir)
	if service != nil {
		_ = service.Close()
		c.mu.Lock()
		c.service = nil
		c.mu.Unlock()
	}
	c.inventoryMu.Lock()
	c.mu.Lock()
	c.settings = s
	c.settingsRevision++
	oldClient := c.client
	if networkChanged {
		c.upstreamDial, c.client = dial, client
	}
	c.scanPlan = nil
	if changedLibrary {
		c.lastBatch, c.batch = snapshots.lastBatch, snapshots.batch
		c.inventorySettings = s
		c.inventoryGeneration++
		c.inventory = snapshots.inventory
	}
	c.mu.Unlock()
	c.inventoryMu.Unlock()
	if networkChanged {
		oldClient.CloseIdleConnections()
	}
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
	cfg.Logger = slog.Default().With("component", "cache")
	cfg.StorageDir = c.settings.StorageDir
	cfg.MaxCacheBytes = c.settings.MaxCacheBytes
	cfg.RequestRetentionDays = c.settings.RequestRetentionDays
	cfg.DialContext = c.upstreamDial
	mode := c.settings.DownloadUpstream
	cfg.ResolvePlayback = func(ctx context.Context, id, node string) (string, error) {
		return c.resolvePlayback(ctx, id, node, mode)
	}
	cfg.KeepRequestedRoute = mode == "auto"
	cfg.ResolveRoutes = func(ctx context.Context, id string) ([]string, error) {
		return c.resolveRoutes(ctx, id, mode)
	}
	cfg.ResolveCurrent = func(ctx context.Context, id string) (string, error) {
		songID, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return "", err
		}
		// Auto retains HKG as the version authority, matching read-only scans.
		// A CF response cannot order conflicting versions or safely
		// replace that authority when HKG is unavailable.
		currentRoute := mode
		if currentRoute == "auto" {
			currentRoute = "hkg"
		}
		return c.resolveNode(ctx, songID, currentRoute)
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

// AutoStart triggers the same start operation as the CDN button at process launch.
func (c *Console) AutoStart() {
	c.mu.Lock()
	enabled := c.settings.AutoStartCDN
	c.mu.Unlock()
	if !enabled {
		return
	}
	_ = c.start()
}

func (c *Console) start() (err error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.startLocked()
}

func (c *Console) startLocked() (err error) {
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
	_ = readHostsStatus()
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

func (c *Console) Close() error {
	c.lifecycleMu.Lock()
	c.mu.Lock()
	c.closing = true
	if c.batchCancel != nil {
		c.batchCancel()
	}
	done := c.batchDone
	if c.queueCancel != nil {
		c.queueCancel()
	}
	queueDone := c.queueDone
	c.mu.Unlock()
	c.lifecycleMu.Unlock()
	err := c.stop()
	c.inventoryMu.Lock()
	inventoryDone := c.inventoryDone
	c.inventoryMu.Unlock()
	if inventoryDone != nil {
		<-inventoryDone
	}
	if done != nil {
		<-done
	}
	if queueDone != nil {
		<-queueDone
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
			Hosts             HostsStatus             `json:"hosts"`
			PortOK            bool                    `json:"portOK"`
			CDNError          string                  `json:"cdnError"`
			ActionErrors      map[string]string       `json:"actionErrors"`
			Batch             Batch                   `json:"batch"`
			PortOwner         *desktop.Owner          `json:"portOwner,omitempty"`
			Queue             QueueStatus             `json:"queue"`
			LastBatch         Batch                   `json:"lastBatch"`
			HTTPSPortOK       bool                    `json:"httpsPortOK"`
			HTTPSPortOwner    *desktop.Owner          `json:"httpsPortOwner,omitempty"`
			Traffic           cacheproxy.TrafficStats `json:"traffic"`
			SOCKS5PasswordSet bool                    `json:"socks5PasswordSet"`
			Activation        Activation              `json:"activation"`
		}{running, c.settings, HostsStatus{}, running, c.cdnError, nil, c.batch, nil, c.queue, c.lastBatch, running, nil, cacheproxy.TrafficStats{}, c.settings.SOCKS5Password != "", c.activation}
		result.Settings.SOCKS5Password = ""
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
			result.Traffic = cacheproxy.ReadTrafficStats(result.Settings.StorageDir)
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
	case "/api/inventory/scan":
		c.startInventoryScan()
	case "/api/batch/verify":
		err = c.startBatchCheck(true, true)
	case "/api/batch/scan":
		err = c.startBatchMode(true)
	case "/api/batch/switch":
		err = c.switchTask(true)
	case "/api/queue/switch":
		err = c.switchTask(false)
	case "/api/start":
		err = c.start()
	case "/api/activation/enable":
		err = c.enableAcceleration(readHostsStatus, changeHosts)
	case "/api/stop":
		err = c.stop()
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
	case "/api/queue/start":
		err = c.startQueue()
	case "/api/queue/stop":
		c.taskMu.Lock()
		c.lifecycleMu.Lock()
		c.mu.Lock()
		c.batchResumeQueue = false
		if c.queueCancel != nil {
			c.queueCancel()
		}
		c.mu.Unlock()
		c.lifecycleMu.Unlock()
		c.taskMu.Unlock()
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
