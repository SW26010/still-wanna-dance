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

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/blocksched"
	"still-wanna-dance/internal/branding"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/upstreamnet"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
	"still-wanna-dance/internal/vrclog"
	"still-wanna-dance/internal/workfiles"
)

//go:embed assets/storage-benefit.svg
var assets embed.FS

type Settings struct {
	nodeSnapshot                string
	nodeSecret                  string
	nodeSecretError             string
	nodeImportID                string // transient request only; cleared before publish
	proxySecret                 string
	proxySecretError            string
	ThroughputIntervalMinutes   int    `json:"throughputIntervalMinutes"`
	QueueWithGateway            bool   `json:"queueWithGateway"`
	RequestRetentionDays        int    `json:"requestRetentionDays"`
	AutoAccelerate              bool   `json:"autoAccelerate"`
	AutoOpenWebUI               bool   `json:"autoOpenWebUI"`
	Theme                       string `json:"theme"`
	RestoreHostsOnExit          bool   `json:"restoreHostsOnExit"`
	PrepareAheadSeconds         int    `json:"prepareAheadSeconds"`
	DownloadSoftLimit           int    `json:"downloadSoftLimit"`
	DownloadGrowIntervalMS      int    `json:"downloadGrowIntervalMS"`
	LocalProtectionGraceSeconds int    `json:"localProtectionGraceSeconds"`
	DownloadUpstream            string `json:"downloadUpstream"`
	UpstreamMode                string `json:"upstreamMode"`
	ProxyBackend                string `json:"proxyBackend"`
	SOCKS5Address               string `json:"socks5Address"`
	SOCKS5Username              string `json:"socks5Username"`
	SOCKS5Password              string `json:"socks5Password,omitempty"`
	MaxCacheBytes               int64  `json:"maxCacheBytes"`
	VideoStorageMode            string `json:"videoStorageMode"`
	RetainFavorites             bool   `json:"retainFavorites"`
	AggressiveCacheReclamation  string `json:"aggressiveCacheReclamation"`
	StorageDir                  string `json:"storageDir"`
	LogDir                      string `json:"logDir"`
	ManualLogDir                bool   `json:"manualLogDir"`
}

type Console struct {
	vrcxMu                 sync.Mutex // preview/apply/discard; acquire before lifecycleMu
	vrcxPreview            *vrcxPreview
	proxyImports           map[string]proxyImportDraft // protected by mu
	identity               desktop.Identity
	exitRequested          chan struct{}
	restartRequested       chan struct{}
	exitOnce               sync.Once
	terms                  termsReceipt // protected by mu; separate from editable settings
	termsNeedsConfirmation bool         // an existing record could not be verified for this user/version
	termsProtection        dataProtection
	termsPageID            string
	termsExitTimer         *time.Timer
	termsExitGeneration    uint64     // invalidates callbacks already waiting for mu
	activationMu           sync.Mutex // rejects duplicate wizard submissions, including across tabs
	activation             Activation // protected by mu; never persisted
	activationGeneration   uint64     // protected by mu; distinguishes same-clock-tick retries
	// Acquire lifecycleMu before mu; state readers never wait on lifecycleMu.
	lifecycleMu         sync.Mutex
	hosts               *hostsController // operations are serialized by lifecycleMu
	queueUpdateMu       sync.Mutex       // acquire before mu; serializes engine queue protection
	inventoryMu         sync.Mutex
	inventory           Inventory
	inventorySettings   Settings
	inventoryGeneration uint64
	inventoryDone       chan struct{}
	inventoryCancel     context.CancelFunc // protected by inventoryMu
	mu                  sync.Mutex
	settings            Settings // active configuration; theme and exit policy apply after save
	savedSettings       Settings // last successfully persisted configuration
	savedNodeCount      int
	loadedNodeCount     int
	setupVersionMu      sync.Mutex // key I/O never holds mu
	setupVersion        settingsVersionCache
	autoStartOnce       sync.Once
	autoStartPending    bool // protected by mu; startup deferred until setup succeeds after consent
	configPath          string
	token               string
	address             string
	videoAddress        string
	httpsAddress        string
	https               *httpsRelay
	service             *cacheproxy.Server
	blockEngineWaiters  map[chan struct{}]bool // lazy, only while a visible timeline awaits engine creation; mu
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
	queueDesired        bool // queue enable intent; independent of manual downloads
	queueCancel         context.CancelFunc
	queueDone           chan struct{}
	closing             bool
	client              *http.Client
	checksumURL         string
	apiBase             string
	dns                 directResolver
	upstreamDial        upstreamDialFunc
	requestRevision     uint64
	monitor             *upstreamstate.Monitor
	monitorRequested    bool
	monitorManual       bool
	monitorManualAt     time.Time
	monitorManualDone   chan struct{}
	setupNetwork        *setupNetwork
	setupApplyMu        sync.Mutex
	setupApplication    SetupApplication   // protected by mu
	setupFolderCancel   context.CancelFunc // protected by mu; also serializes dialogs
	setupFolderPicker   func(context.Context, string, string) (string, error)
}

func New(configPath, address string) (*Console, error) {
	return newConsole(configPath, address, upstreamnet.NewResolver(nil))
}

func newConsole(configPath, address string, dns directResolver) (*Console, error) {
	identity, err := desktop.NewIdentity(configPath)
	if err != nil {
		return nil, err
	}
	configPath = identity.Config
	if err := apppaths.ValidateConfigPath(configPath); err != nil {
		return nil, err
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	c := &Console{configPath: configPath, address: address, videoAddress: "127.0.0.1:80", httpsAddress: "127.0.0.1:443", token: hex.EncodeToString(b), apiBase: "https://api.udon.dance", checksumURL: "https://x.kiva.moe/api/v2/wanna/songs"}
	c.identity = identity
	c.hosts = newHostsController(hostsPath(), launchHostsSession)
	c.settings = Settings{
		StorageDir: apppaths.ForConfig(configPath).DefaultStorage, RequestRetentionDays: 30,
		AutoAccelerate: true, AutoOpenWebUI: true, QueueWithGateway: true, RestoreHostsOnExit: true,
		VideoStorageMode: "none", MaxCacheBytes: 0, RetainFavorites: true,
		AggressiveCacheReclamation: "auto",
		DownloadSoftLimit:          cacheproxy.VideoRequestSoftLimit, DownloadGrowIntervalMS: int(cacheproxy.DefaultDownloadGrowInterval / time.Millisecond),
		LocalProtectionGraceSeconds: int(cacheproxy.DefaultLocalProtectionGrace / time.Second),
	}
	c.exitRequested = make(chan struct{})
	c.restartRequested = make(chan struct{})
	c.dns = dns
	data, fields, err := readConfigFile(configPath)
	if err != nil {
		return nil, err
	}
	if data != nil {
		if err := json.Unmarshal(data, &c.settings); err != nil {
			return nil, fmt.Errorf("读取程序配置：%w", err)
		}
		loadProxySecret(fields, &c.settings)
		loadProxyNodes(fields, &c.settings)
	}
	c.settings, err = c.resolveSettings(c.settings)
	c.savedSettings = c.settings
	c.loadedNodeCount = len(nodeDocument(c.settings.nodeSnapshot).Nodes)
	c.savedNodeCount = c.loadedNodeCount
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
		c.loadSetupApplication()
		c.requestRevision = upstreamrequest.Default.Publish(c.client.Transport)
	}
	if err != nil && c.client != nil {
		c.client.CloseIdleConnections()
		if tr, ok := c.client.Transport.(interface{ Retire() }); ok {
			tr.Retire()
		}
		c.dns.Close()
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
	if s.Theme == "" {
		s.Theme = "system"
	}
	if s.Theme != "system" && s.Theme != "light" && s.Theme != "dark" {
		return s, errors.New("主题必须为 system、light 或 dark")
	}
	if s.ThroughputIntervalMinutes == 0 {
		s.ThroughputIntervalMinutes = 20
	}
	if s.ThroughputIntervalMinutes < 1 || s.ThroughputIntervalMinutes > 1440 {
		return s, errors.New("自动吞吐检测间隔必须为 1～1440 分钟")
	}
	if s.UpstreamMode == "" {
		s.UpstreamMode = "direct"
	}
	if s.UpstreamMode == "socks5" {
		s.UpstreamMode = "proxy"
	} // legacy saved policy
	if s.UpstreamMode != "direct" && s.UpstreamMode != "proxy" && s.UpstreamMode != "auto" {
		return s, errors.New("上游连接必须是 direct、proxy 或 auto")
	}
	if s.ProxyBackend == "" {
		s.ProxyBackend = "external-socks5"
	}
	if s.ProxyBackend != "external-socks5" && s.ProxyBackend != "mihomo" {
		return s, errors.New("代理后端必须是 external-socks5 或 mihomo")
	}
	s.SOCKS5Address = strings.TrimSpace(s.SOCKS5Address)
	if s.UpstreamMode != "direct" && s.ProxyBackend == "external-socks5" {
		if err := upstreamnet.ValidateSOCKS5Address(s.SOCKS5Address); err != nil {
			return s, err
		}
		if err := upstreamnet.ValidateSOCKS5Credentials(s.SOCKS5Username, s.SOCKS5Password); err != nil && s.proxySecretError == "" {
			return s, err
		}
	}
	if s.PrepareAheadSeconds == 0 {
		s.PrepareAheadSeconds = int(blocksched.DefaultPrepareAhead / time.Second)
	}
	if s.PrepareAheadSeconds < int(blocksched.MinPrepareAhead/time.Second) || s.PrepareAheadSeconds > int(blocksched.MaxPrepareAhead/time.Second) {
		return s, errors.New("提前准备时间 M 必须为 100～16000 秒")
	}
	if s.DownloadSoftLimit < 1 || s.DownloadSoftLimit > cacheproxy.MaxDownloadSoftLimit {
		return s, errors.New("下载并发上限（软）必须为 1～64")
	}
	if s.DownloadGrowIntervalMS < 0 || s.DownloadGrowIntervalMS > int(cacheproxy.MaxDownloadGrowInterval/time.Millisecond) {
		return s, errors.New("并发增长间隔必须为 0～2000 毫秒")
	}
	if s.LocalProtectionGraceSeconds < 0 || s.LocalProtectionGraceSeconds > int(cacheproxy.MaxLocalProtectionGrace/time.Second) {
		return s, errors.New("本地延续保护时长必须为 0～10000 秒")
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
		return s, errors.New("视频存储预算不能为负数")
	}
	if s.VideoStorageMode != "none" && s.VideoStorageMode != "limited" && s.VideoStorageMode != "unlimited" {
		return s, errors.New("视频保留选项必须为 none、limited 或 unlimited")
	}
	if s.VideoStorageMode == "limited" && s.MaxCacheBytes == 0 {
		return s, errors.New("选择「限制为」时，视频存储预算必须大于 0")
	}
	if s.AggressiveCacheReclamation != "auto" && s.AggressiveCacheReclamation != "on" && s.AggressiveCacheReclamation != "off" {
		return s, errors.New("激进的缓存回收策略必须为 auto、on 或 off")
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
	// Probe the nearest existing ancestor without creating an unused media root.
	// Actual initialization belongs to the storage engine under its directory lock.
	for {
		info, err := os.Stat(path)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("路径不是目录：%s", path)
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return err
		}
		path = parent
	}
	return workfiles.Probe(path)
}

func (c *Console) save(s Settings) error {
	return c.saveSettings(s, false)
}

// An omitted password in the settings API preserves the stored secret. Merge
// under the lifecycle lock so concurrent saves cannot restore stale secrets.
func (c *Console) saveSettings(s Settings, preservePassword bool) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.saveSettingsLocked(s, preservePassword)
}

func (c *Console) saveSettingsLocked(s Settings, preservePassword bool) error {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return errors.New("程序正在退出")
	}
	oldSettings := c.savedSettings
	c.mu.Unlock()
	// Ordinary forms omit the independently saved theme, so a stale form cannot
	// overwrite a newer appearance choice from another page or browser.
	if s.Theme == "" {
		s.Theme = oldSettings.Theme
	}
	if preservePassword {
		s.SOCKS5Password = oldSettings.SOCKS5Password
		s.proxySecret = oldSettings.proxySecret
		s.proxySecretError = oldSettings.proxySecretError
	} else {
		s.proxySecret = ""
		s.proxySecretError = ""
	}
	var err error
	s, err = c.mergeProxyNodes(s, oldSettings)
	if err != nil {
		return err
	}
	importID := s.nodeImportID
	s.nodeImportID = ""
	s, err = c.resolveSettings(s)
	if err != nil {
		return err
	}
	if err = validateProxyNodeSelection(s); err != nil {
		return err
	}
	// Preserve an existing library when saving unrelated settings. New choices
	// must use the default location or a directory outside Portable.
	if s.StorageDir != oldSettings.StorageDir {
		if err = c.validateStorageSelection(s.StorageDir, false); err != nil {
			return err
		}
	}
	if err = writableDir(s.StorageDir); err != nil {
		return fmt.Errorf("目录不可写：%w", err)
	}
	if s.RestoreHostsOnExit && !oldSettings.RestoreHostsOnExit {
		if err = c.hosts.prepareRestore(); err != nil {
			return fmt.Errorf("未保存设置：无法准备退出时恢复游戏直连的权限：%w", err)
		}
	}
	if err = c.writeConfigFile(s); err != nil {
		return err
	}
	nodeCount := len(nodeDocument(s.nodeSnapshot).Nodes)
	c.refreshSettingsVersionIfUsed(s)
	c.mu.Lock()
	c.savedSettings = s
	c.settings.RestoreHostsOnExit = s.RestoreHostsOnExit
	c.settings.Theme = s.Theme
	c.savedNodeCount = nodeCount
	delete(c.proxyImports, importID)
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
	if _, err := c.inspectStorage(c.settings.StorageDir); err != nil {
		return err
	}
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
	cfg.CacheUnlimited = c.settings.VideoStorageMode == "unlimited"
	cfg.RetainFavorites = c.settings.RetainFavorites
	cfg.AggressiveCacheReclamation = c.settings.AggressiveCacheReclamation
	if c.settings.VideoStorageMode == "none" {
		cfg.MaxCacheBytes = 0
	}
	cfg.RequestRetentionDays = c.settings.RequestRetentionDays
	cfg.PrepareAhead = time.Duration(c.settings.PrepareAheadSeconds) * time.Second
	cfg.DownloadSoftLimit = c.settings.DownloadSoftLimit
	cfg.DownloadGrowInterval = time.Duration(c.settings.DownloadGrowIntervalMS) * time.Millisecond
	cfg.LocalProtectionGrace = time.Duration(c.settings.LocalProtectionGraceSeconds) * time.Second
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
		}
		return transports
	}
	s, err := cacheproxy.New(cfg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.service = s
	c.notifyBlockEngineLocked()
	c.mu.Unlock()
	return nil
}

// AutoStart attempts acceleration once per process, after the final setup apply.
func (c *Console) AutoStart() {
	c.autoStartOnce.Do(func() {
		c.mu.Lock()
		c.autoStartPending = true
		ready := c.terms.valid() && c.setupApplication.Phase == "completed"
		c.mu.Unlock()
		if ready {
			c.autoStart()
		}
		c.resumeSetupApplication(c.startImport)
	})
}

// Also called after a successful final setup apply, including restart continuation.
func (c *Console) autoStart() {
	c.mu.Lock()
	if !c.autoStartPending || !c.terms.valid() {
		c.mu.Unlock()
		return
	}
	c.autoStartPending = false
	enabled := c.settings.AutoAccelerate
	c.mu.Unlock()
	if enabled {
		if err := c.enableAcceleration(c.hosts.status, c.changeHostsLocked); err != nil {
			slog.Error("auto_acceleration_failed", "error", err)
		}
	}
}

func (c *Console) start() (err error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.startLocked()
}

func (c *Console) startLocked() (err error) {
	if !c.termsAccepted() {
		return errors.New("请先打开 WebUI 阅读并同意使用条款")
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
		return errors.New("程序正在退出")
	}
	if running {
		c.startGatewayQueueLocked()
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
	relay := newHTTPSRelay(secure, c.dns.DialContext)
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
	c.startGatewayQueueLocked()
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
	if c.settings.QueueWithGateway {
		c.mu.Unlock()
		c.stopQueueLocked()
		c.mu.Lock()
	}
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
	// Cancel a pending authorization before waiting for lifecycleMu. Existing
	// helper connections remain usable for cleanup without a new UAC request.
	c.hosts.cancel()
	c.lifecycleMu.Lock()
	c.mu.Lock()
	c.closing = true
	c.notifyBlockEngineLocked()
	if c.setupFolderCancel != nil {
		c.setupFolderCancel()
	}
	c.cancelTermsExitLocked()
	c.queueDesired = false
	upstreamrequest.Default.Release(c.requestRevision)
	if c.setupNetwork != nil {
		if c.setupNetwork.connectivityCancel != nil {
			c.setupNetwork.connectivityCancel()
		}
		c.setupNetwork.connectivity.Closed = true
		c.publishSetupConnectivityLocked()
		if c.setupNetwork.calibration != nil {
			c.setupNetwork.calibration.Shutdown()
		}
		c.setupNetwork.channel.Release(c.setupNetwork.revision)
	}
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
	c.lifecycleMu.Lock()
	c.mu.Lock()
	restore := c.settings.RestoreHostsOnExit
	c.mu.Unlock()
	hostsErr := c.hosts.close(restore)
	c.lifecycleMu.Unlock()
	if hostsErr != nil {
		hostsErr = fmt.Errorf("退出时未能确认游戏连接已清理，下次启动后请恢复游戏直连：%w", hostsErr)
		slog.Error("hosts_cleanup_failed", "error", hostsErr)
	}
	c.mu.Lock()
	monitor := c.monitor
	setupNetwork := c.setupNetwork
	monitorManualDone := c.monitorManualDone
	done, importDone, queueDone := c.batchDone, c.importDone, c.queueDone
	c.mu.Unlock()
	c.inventoryMu.Lock()
	inventoryDone := c.inventoryDone
	c.inventoryMu.Unlock()
	err := c.stop()
	setupNetwork.close()
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
		c.dns.Close()
	}
	return errors.Join(err, hostsErr)
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
	// Branding is public even before terms acceptance, like the other UI assets.
	if r.Method == http.MethodGet && r.URL.Path == "/assets/logo.svg" {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(branding.SVG)
		return
	}
	if r.Method == http.MethodGet && (r.URL.Path == "/favicon.ico" || r.URL.Path == "/favicon-light.ico") {
		w.Header().Set("Content-Type", "image/x-icon")
		if r.URL.Path == "/favicon-light.ico" {
			w.Write(branding.LightICO)
		} else {
			w.Write(branding.ICO)
		}
		return
	}
	if serveWebUIAsset(w, r) {
		return
	}
	if c.serveTerms(w, r) {
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/setup" {
		progress, err := c.setupProgress()
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, progress)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/" {
		c.serveConsolePage(w, nil)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/about" {
		c.serveWebUI(w, "about", "关于", nil)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/help" {
		c.serveHelp(w)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/assets/storage-benefit.svg" {
		content, err := assets.ReadFile(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			http.Error(w, "asset unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(content)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/identity" {
		writeJSON(w, c.identity)
		return
	}
	if r.URL.Path == "/api/status" && r.Method == "GET" {
		c.mu.Lock()
		running := c.httpServer != nil
		result := struct {
			Running               bool                    `json:"running"`
			Settings              Settings                `json:"settings"`
			ActiveSettings        Settings                `json:"activeSettings"`
			RestartRequired       bool                    `json:"restartRequired"`
			Hosts                 HostsStatus             `json:"hosts"`
			PortOK                bool                    `json:"portOK"`
			CDNError              string                  `json:"cdnError"`
			StorageError          string                  `json:"storageError,omitempty"`
			ActionErrors          map[string]string       `json:"actionErrors"`
			Batch                 Batch                   `json:"batch"`
			Import                ImportStatus            `json:"import"`
			PortOwner             *desktop.Owner          `json:"portOwner,omitempty"`
			Queue                 QueueStatus             `json:"queue"`
			LastBatch             Batch                   `json:"lastBatch"`
			HTTPSPortOK           bool                    `json:"httpsPortOK"`
			HTTPSPortOwner        *desktop.Owner          `json:"httpsPortOwner,omitempty"`
			Traffic               cacheproxy.TrafficStats `json:"traffic"`
			SOCKS5PasswordSet     bool                    `json:"socks5PasswordSet"`
			SOCKS5CredentialError string                  `json:"socks5CredentialError,omitempty"`
			ProxyNodes            proxyNodeStatus         `json:"proxyNodes"`
			Activation            Activation              `json:"activation"`
			DefaultLogDir         string                  `json:"defaultLogDir"`
			DefaultStorageDir     string                  `json:"defaultStorageDir"`
			PortableDir           string                  `json:"portableDir"`
			UpstreamMonitor       upstreamstate.Status    `json:"upstreamMonitor"`
			SetupNetwork          setupNetworkStatus      `json:"setupNetwork"`
			SetupApplication      SetupApplication        `json:"setupApplication"`
			SetupSettingsVersion  string                  `json:"setupSettingsVersion"`
			SetupSettingsError    string                  `json:"setupSettingsError,omitempty"`
		}{
			Running: running, Settings: c.savedSettings, ActiveSettings: c.settings,
			RestartRequired: c.savedSettings != c.settings,
			PortOK:          running, HTTPSPortOK: running, CDNError: c.cdnError,
			Import: c.importStatus, Batch: c.batch, Queue: c.queue, LastBatch: c.lastBatch,
			SOCKS5PasswordSet:     c.savedSettings.SOCKS5Password != "",
			SOCKS5CredentialError: c.savedSettings.proxySecretError,
			ProxyNodes:            c.proxyNodesStatusLocked(),
			Activation:            c.activation, DefaultLogDir: defaultLogDir(),
			DefaultStorageDir: c.defaultStorageDir(), PortableDir: portableDir(),
			UpstreamMonitor:  c.monitorSnapshotLocked(),
			SetupNetwork:     c.setupNetworkSnapshotLocked(),
			SetupApplication: c.setupApplication.public(),
		}
		versionSettings := result.Settings
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
		var versionErr error
		result.SetupSettingsVersion, versionErr = c.cachedSettingsVersion(versionSettings)
		if versionErr != nil {
			result.SetupSettingsError = versionErr.Error()
		}
		result.Hosts = c.hosts.status()
		if service != nil {
			if err := service.StorageError(); err != nil {
				result.StorageError = applog.SafeError(err).Error()
			}
			result.Traffic = service.TrafficStats()
			result.Queue.Titles = service.QueueTitles()
			for i := range result.Queue.Failures {
				f := &result.Queue.Failures[i]
				f.Name = result.Queue.Titles[strconv.FormatInt(f.ID, 10)]
			}
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
	if r.URL.Path == "/api/songs" {
		c.songPolicyAPI(w, r)
		return
	}
	if r.URL.Path == "/api/library" {
		c.libraryAPI(w, r)
		return
	}
	if r.URL.Path == "/api/downloads" && r.Method == "GET" {
		if r.Header.Get("X-SWD-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
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
			Running bool                   `json:"running"`
			GETs    cacheproxy.CurrentGETs `json:"gets"`
		}{cacheproxy.ActiveDownloads{Tasks: []cacheproxy.ActiveDownload{}}, running, cacheproxy.CurrentGETs{Requests: []cacheproxy.CurrentGET{}}}
		if service != nil {
			var err error
			result.ActiveDownloads, err = service.ActiveDownloads(r.Context())
			result.GETs = service.CurrentGETs()
			c.mu.Lock()
			if c.service != service {
				result.ActiveDownloads = cacheproxy.ActiveDownloads{Tasks: []cacheproxy.ActiveDownload{}}
				result.GETs = cacheproxy.CurrentGETs{Requests: []cacheproxy.CurrentGET{}}
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
	if (r.URL.Path == "/api/history" || r.URL.Path == "/api/latency") && r.Method == "GET" {
		c.serveRequestHistory(w, r)
		return
	}
	if r.URL.Path == "/api/requests" && r.Method == "GET" {
		if r.Header.Get("X-SWD-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
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
		// The active storage root is fixed for this Console. The reader owns
		// its read-only connection and does not borrow the engine's lifetime.
		c.mu.Lock()
		root := c.settings.StorageDir
		c.mu.Unlock()
		result, err := cacheproxy.ReadRecentRequests(r.Context(), root, limit)
		if err != nil {
			http.Error(w, "最近请求读取失败，请稍后重试", http.StatusInternalServerError)
			return
		}
		writeJSON(w, result)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/block-demands/events" {
		c.serveBlockEvents(w, r)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/setup/network/calibration/events" {
		c.serveSetupCalibrationEvents(w, r)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/setup/network/connectivity/events" {
		c.serveSetupConnectivityEvents(w, r)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "not found", 404)
		return
	}
	if r.Header.Get("X-SWD-Token") != c.token || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+c.address) {
		http.Error(w, "invalid origin or token", 403)
		return
	}
	// Only this data-only import endpoint has a larger payload allowance.
	if r.URL.Path == "/api/proxy/import" {
		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		data, err := io.ReadAll(r.Body)
		defer clear(data)
		if err != nil {
			http.Error(w, "节点内容读取失败或超过 2 MiB", http.StatusBadRequest)
			return
		}
		result, err := c.importProxyNodes(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, result)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if strings.HasPrefix(r.URL.Path, "/api/vrcx/") {
		c.serveVRCX(w, r)
		return
	}
	var err error
	switch r.URL.Path {
	case "/api/proxy/import/discard":
		var input struct {
			ID string `json:"id"`
		}
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			c.mu.Lock()
			delete(c.proxyImports, input.ID)
			c.mu.Unlock()
			// Idempotent even after a successful save or expiration. Only the
			// named in-memory draft is released; saved settings never change.
			writeJSON(w, map[string]bool{"ok": true})
			return
		}
	case "/api/setup/folder":
		var input struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			var path string
			path, err = c.selectSetupFolder(r.Context(), input.Kind, input.Path)
			if err == nil {
				writeJSON(w, map[string]any{"path": path, "canceled": path == ""})
				return
			}
		}
	case "/api/setup/receipt":
		var input struct {
			ID string `json:"id"`
		}
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			c.setupApplyMu.Lock()
			var receipt SetupApplication
			receipt, err = c.readSetupReceipt(input.ID)
			c.setupApplyMu.Unlock()
			if err == nil {
				writeJSON(w, receipt.public())
				return
			}
			if os.IsNotExist(err) {
				w.WriteHeader(http.StatusNotFound)
				writeJSON(w, map[string]string{"error": "未找到原应用回执；请核对正式配置与导入任务，不会自动重新执行"})
				return
			}
		}
	case "/api/setup/validate", "/api/setup/apply":
		var input setupPlan
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			if r.URL.Path == "/api/setup/validate" {
				var effects setupEffects
				effects, err = c.reviewSetupPlan(input)
				if err == nil {
					writeJSON(w, effects)
					return
				}
			} else {
				var result SetupApplication
				result, err = c.applySetupPlan(input)
				if err == nil {
					writeJSON(w, result.public())
					_ = http.NewResponseController(w).Flush()
					if result.Phase == "restart" {
						c.exitOnce.Do(func() { close(c.restartRequested) })
					}
					return
				}
			}
		}
	case "/api/setup/network":
		err = c.checkSetupNetwork()
	case "/api/setup/network/calibration/start":
		var input setupCalibrationRequest
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			err = c.controlSetupCalibration(false, input)
		}
	case "/api/setup/network/calibration/cancel":
		err = c.controlSetupCalibration(true, setupCalibrationRequest{})
	case "/api/setup/network/connectivity/start":
		var input setupCalibrationRequest
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			err = c.controlSetupConnectivity(false, input)
		}
	case "/api/setup/network/connectivity/cancel":
		err = c.controlSetupConnectivity(true, setupCalibrationRequest{})
	case "/api/setup/network/catalog":
		err = c.checkSetupNetwork(upstreamstate.CheckCatalog)
	case "/api/setup/network/playback":
		err = c.checkSetupNetwork(upstreamstate.CheckPlayback)
	case "/api/setup/network/throughput":
		err = c.checkSetupNetwork(upstreamstate.CheckThroughput)
	case "/api/setup/logs":
		writeJSON(w, c.inspectSetupLogs())
		return
	case "/api/setup/storage":
		var input struct {
			Path string `json:"path"`
		}
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			var result setupStorage
			result, err = c.inspectSetupStorage(input.Path)
			if err == nil {
				writeJSON(w, result)
				return
			}
		}
	case "/api/setup":
		var progress SetupProgress
		if err = json.NewDecoder(r.Body).Decode(&progress); err == nil {
			err = c.saveSetupProgress(progress)
		}
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
	case "/api/start":
		err = c.startForUser()
	case "/api/activation/enable":
		err = c.enableAcceleration(c.hosts.status, c.changeHostsLocked)
	case "/api/stop":
		err = c.stop()
	case "/api/queue/start":
		err = c.startQueue()
	case "/api/queue/stop":
		err = c.stopQueue()
	case "/api/settings/theme":
		var input struct {
			Theme string `json:"theme"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&input); err == nil {
			err = c.saveTheme(input.Theme)
		}
	case "/api/settings":
		var input struct {
			Settings
			CustomStorage bool    `json:"customStorage"`
			ProxyImportID string  `json:"proxyImportID"`
			Password      *string `json:"socks5Password"`
		}
		input.DownloadSoftLimit = cacheproxy.VideoRequestSoftLimit
		input.RestoreHostsOnExit = true
		input.RetainFavorites = true
		input.DownloadGrowIntervalMS = int(cacheproxy.DefaultDownloadGrowInterval / time.Millisecond)
		input.LocalProtectionGraceSeconds = int(cacheproxy.DefaultLocalProtectionGrace / time.Second)
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			if input.CustomStorage {
				if err = c.validateStorageSelection(input.StorageDir, true); err != nil {
					break
				}
			}
			if input.Password != nil {
				input.Settings.SOCKS5Password = *input.Password
			}
			input.Settings.nodeImportID = input.ProxyImportID
			err = c.saveSettings(input.Settings, input.Password == nil)
		}
	case "/api/hosts/enable":
		err = c.changeHosts("enable")
	case "/api/hosts/disable":
		err = c.changeHosts("disable")
	case "/api/batch/start":
		err = c.startBatch()
	case "/api/batch/download":
		var input struct {
			StorageID string `json:"storageID"`
			cacheproxy.DownloadTarget
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&input); err == nil {
			err = c.startBatchTarget(input.StorageID, input.DownloadTarget)
		}
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
