package console

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/branding"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/catalogsync"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/faults"
	"still-wanna-dance/internal/playbacksource"
	"still-wanna-dance/internal/upstreamcheck"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
)

//go:embed assets/storage-benefit.svg
var assets embed.FS

type Console struct {
	editor                 *settingsEditor
	faults                 atomic.Pointer[faults.Center]
	store                  *cacheproxy.Store
	storageOpenError       error
	storageConfirmation    *storageConfirmationRequired
	work                   *cacheproxy.WorkDirectory // Borrowed from the application entry point.
	workOpenError          error                     // Workspace could not be opened during startup.
	channelIdentity        proxyChannelIdentity
	vrcxMu                 sync.Mutex // preview/apply/discard; acquire before lifecycleMu
	vrcxPreview            *vrcxPreview
	vrcxNotify             bool // startup Toast only; set during automatic library creation
	vrcxImportDismissed    bool // protected by mu; persisted user choice outside editable Settings
	videoImportSuggestion  bool // protected by mu; only the first run of a configuration, until acknowledged
	proxyModeMigrated      bool // startup notice; migration is already persisted
	identity               desktop.Identity
	lifecycle              *applicationLifecycle
	terms                  termsReceipt // protected by mu; separate from editable settings
	termsNeedsConfirmation bool         // an existing record could not be verified for this user/version
	termsStartupFailed     bool         // protected by mu; consent was saved but startup did not complete
	termsProtection        dataProtection
	termsPageID            string
	termsExitTimer         *time.Timer
	termsExitGeneration    uint64     // invalidates callbacks already waiting for mu
	activationMu           sync.Mutex // rejects duplicate acceleration requests, including across tabs
	activation             Activation // protected by mu; never persisted
	activationGeneration   uint64     // protected by mu; distinguishes same-clock-tick retries
	// Acquire lifecycleMu before mu; state readers never wait on lifecycleMu.
	lifecycleMu         sync.Mutex
	inventoryMu         sync.Mutex
	inventory           Inventory
	inventorySettings   Settings
	inventoryGeneration uint64
	inventoryDone       chan struct{}
	inventoryCancel     context.CancelFunc // protected by inventoryMu
	mu                  sync.Mutex
	settings            Settings // active configuration; theme and exit policy apply after save
	autoStartOnce       sync.Once
	autoStartPending    bool // protected by mu; startup deferred until consent succeeds
	updateAutoRequested bool // update checking is deferred until consent and network setup
	updateAutoStarted   bool
	update              updateStatus
	updateNotice        string
	updateCancel        context.CancelFunc
	updateDone          chan struct{}
	configPath          string
	token               string
	previewKey          string // Process-local media signing key, never exposed to WebUI.
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
	importTask          *videoImportTask
	importCancel        context.CancelFunc
	importDone          chan struct{}
	batchCancel         context.CancelFunc
	batchDone           chan struct{}
	queue               queueTask
	queueDesired        bool // queue enable intent; independent of manual downloads
	closing             bool
	client              *http.Client
	covers              songCoverCache
	checksumURL         string
	apiBase             string
	dns                 directResolver
	upstreamDial        upstreamDialFunc
	resources           *upstreamrequest.Resources
	requestChannel      *upstreamrequest.Channel
	requestRevision     uint64
	metadata            *catalogsync.Sync
	monitor             *upstreamstate.Monitor
	upstreamRequested   bool
	monitorManual       bool
	monitorManualAt     time.Time
	monitorManualDone   chan struct{}
	networkDiagnostics  *networkDiagnostics
}

func New(configPath, address string, work *cacheproxy.WorkDirectory) (*Console, error) {
	return newConsole(configPath, address, nil, work)
}

func newConsole(configPath, address string, dns directResolver, work *cacheproxy.WorkDirectory) (*Console, error) {
	return newConsoleWithWorkError(configPath, address, dns, work, nil)
}

// NewWithWorkError permits the settings UI to recover an unavailable cache
// location. Video operations remain unavailable until a successful restart.
func NewWithWorkError(configPath, address string, work *cacheproxy.WorkDirectory, workErr error) (*Console, error) {
	return newConsoleWithWorkError(configPath, address, nil, work, workErr)
}

func newConsoleWithWorkError(configPath, address string, dns directResolver, work *cacheproxy.WorkDirectory, workErr error) (*Console, error) {
	if work == nil && workErr == nil {
		return nil, errors.New("应用工作目录尚未准备")
	}
	identity, err := desktop.NewIdentity(configPath)
	if err != nil {
		return nil, err
	}
	configPath = identity.Config
	if err := apppaths.ValidateConfigPath(configPath); err != nil {
		return nil, err
	}
	if err := apppaths.ValidateWorkSeparation(identity.Executable, configPath); err != nil {
		return nil, err
	}
	token, err := newManagementToken()
	if err != nil {
		return nil, err
	}
	c := &Console{address: address, identity: identity, configPath: identity.Config, token: token, lifecycle: newApplicationLifecycle()}
	c.editor = newSettingsEditor(identity, &c.lifecycleMu)
	c.editor.termsAccepted = func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.terms.valid() }
	c.editor.onSaved = func(s Settings, commit func()) {
		c.mu.Lock()
		defer c.mu.Unlock()
		commit()
		c.settings.Theme = s.Theme
		c.settings.RestoreHostsOnStopOrExit = s.RestoreHostsOnStopOrExit
		c.settings.nodeInfo = s.nodeInfo
	}
	c.editor.onResult = c.recordActionError
	if work != nil {
		c.editor.workPath = work.Path()
	}
	c.work, c.workOpenError = work, workErr
	c.videoAddress, c.httpsAddress = "127.0.0.1:80", "127.0.0.1:443"
	c.apiBase, c.checksumURL = playbacksource.DefaultBaseURL, cacheproxy.UdonCatalogURL
	c.previewKey = rand.Text()
	c.dns = dns
	c.resources = upstreamrequest.NewResources()
	c.requestChannel = upstreamrequest.NewChannel()
	loaded, err := loadConfigFile(configPath)
	if err != nil {
		return nil, err
	}
	c.videoImportSuggestion = !loaded.Exists
	c.vrcxImportDismissed = loaded.VRCXImportDismissed
	c.settings = loaded.Settings
	c.editor.initializeSettings(c.settings)
	err = c.editor.validateResourcePaths(c.settings)
	if err == nil {
		err = c.loadTerms()
	}
	if err == nil {
		c.upstreamDial, c.client, err = c.networkFor(c.settings)
	}
	if err == nil && loaded.UpstreamAuto {
		err = migrateAutoUpstreamMode(c.configPath)
		c.proxyModeMigrated = err == nil
	}
	if err == nil {
		original := c.settings
		c.prepareStorage()
		if !loaded.Exists || c.settings != original {
			err = saveConfigSettings(c.configPath, c.settings)
		}
		c.editor.initializeSettings(c.settings)
	}
	if err == nil {
		c.loadSnapshots()
		c.loadImportTask()
		if c.settings.UpstreamMode == "direct" {
			c.requestRevision = c.requestChannel.Publish(c.client.Transport.(upstreamrequest.Candidates))
		}
	}
	if err != nil && c.store != nil {
		c.store.Close()
	}
	if err != nil && c.client != nil {
		c.client.CloseIdleConnections()
		if tr, ok := c.client.Transport.(interface{ Retire() }); ok {
			tr.Retire()
		}
		if c.dns != nil {
			c.dns.Close()
		}
	}
	return c, err
}

// AutoStart attempts acceleration once per process, after valid terms acceptance.
func (c *Console) AutoStart() {
	c.autoStartOnce.Do(func() {
		c.mu.Lock()
		c.autoStartPending = true
		ready := c.terms.valid()
		c.mu.Unlock()
		if ready {
			c.autoStart()
		}
	})
}

// Also called after explicit consent; repeated acceptance never retries startup.
func (c *Console) autoStart() {
	c.mu.Lock()
	if !c.autoStartPending || !c.terms.valid() || c.termsStartupFailed {
		c.mu.Unlock()
		return
	}
	c.autoStartPending = false
	enabled := c.settings.AutoAccelerate
	c.mu.Unlock()
	if enabled {
		err := c.enableAcceleration(c.editor.hosts.status, c.changeHostsLocked)
		c.reportFault("action:activation", err)
		if err != nil {
			slog.Error("自动开启加速失败", "event_name", "auto_acceleration_failed", "error", err)
		}
	}
}

// Caller holds lifecycleMu, not mu.
func (c *Console) startLocked() (err error) {
	if !c.termsAccepted() {
		return errors.New("请先打开 WebUI 阅读并同意使用条款")
	}
	defer func() {
		if err != nil {
			c.mu.Lock()
			c.cdnError = err.Error()
			c.mu.Unlock()
			slog.Error("本地视频服务启动失败", "event_name", "cdn_start_failed", "error", err)
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
	// Publish ownership together with binding so status probes cannot contend
	// with our listeners, including while the engine is still opening.
	c.mu.Lock()
	l, err := net.Listen("tcp4", c.videoAddress)
	if err != nil {
		c.mu.Unlock()
		return desktop.PortError(c.videoAddress, err)
	}
	secure, err := net.Listen("tcp4", c.httpsAddress)
	if err != nil {
		l.Close()
		c.mu.Unlock()
		return fmt.Errorf("网页 HTTPS 转发启动失败：%w", desktop.PortError(c.httpsAddress, err))
	}
	relay := newHTTPSRelay(secure, c.upstreamDial)
	relay.beginResourceLoad = c.resources.BeginResourceLoad
	relay.isResourceHost = c.resources.IsResourceDomain
	c.videoListener, c.https = l, relay
	c.mu.Unlock()
	engine, err := c.ensureEngineLocked()
	if err != nil {
		c.mu.Lock()
		l.Close()
		relay.close() // Serving has not started, so there are no clients to drain.
		c.videoListener, c.https = nil, nil
		c.mu.Unlock()
		return err
	}
	h := &http.Server{Handler: engine, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)}
	c.mu.Lock()
	c.httpServer = h
	c.cdnError = ""
	c.reportFault("service", nil)
	c.mu.Unlock()
	slog.Info("本地视频服务已启动", "event_name", "cdn_started", "address", l.Addr().String(), "https_address", secure.Addr().String())
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
	if c.httpServer != h {
		c.mu.Unlock()
		return
	}
	slog.Error("本地视频服务发生故障", "event_name", "cdn_failed", "error", err)
	c.cdnError = err.Error()
	c.mu.Unlock()
	c.reportFault("service", err)
	c.stopLocked()
}

func (c *Console) stop() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.stopLocked()
	c.mu.Lock()
	c.cdnError = ""
	c.mu.Unlock()
	c.reportFault("service", nil)
	return nil
}

// Caller holds lifecycleMu, not mu. Queue withdrawal and connection draining
// may block, so this method owns the short state-lock sections itself.
func (c *Console) stopLocked() {
	c.stopQueueLocked()
	c.mu.Lock()
	c.activation.FirstRequest = time.Time{}
	if !c.activation.Started.IsZero() {
		c.activation.Phase = "stopped"
	}
	listener, relay, server := c.videoListener, c.https, c.httpServer
	// Release ports before clearing ownership. Draining clients stays outside mu.
	if listener != nil {
		listener.Close()
	}
	if relay != nil {
		relay.listener.Close()
	}
	c.videoListener, c.https, c.httpServer = nil, nil, nil
	c.mu.Unlock()
	if relay != nil {
		relay.close()
	}
	if server == nil {
		return
	}
	_ = server.Close()
	slog.Info("本地视频服务已停止", "event_name", "cdn_stopped")
}

// BeginShutdown rejects new work and cancels background tasks without waiting
// for them. The owner calls it before draining HTTP requests, then calls Close
// after closing HTTP connections to wait for tasks and flush the cache engine.
// It is safe to call repeatedly.
func (c *Console) BeginShutdown() {
	c.lifecycle.beginShutdown()
	// Cancel a pending authorization before waiting for lifecycleMu. Existing
	// helper connections remain usable for cleanup without a new UAC request.
	c.editor.beginShutdown()
	c.lifecycleMu.Lock()
	c.mu.Lock()
	c.closing = true
	c.notifyBlockEngineLocked()
	if c.updateCancel != nil {
		c.updateCancel()
	}
	c.cancelTermsExitLocked()
	c.queueDesired = false
	if c.requestRevision != 0 {
		c.requestChannel.Release(c.requestRevision)
	}
	c.resources.Close()
	c.networkDiagnostics.shutdown()
	if c.batchCancel != nil {
		c.batchCancel()
	}
	if c.importCancel != nil {
		c.importCancel()
	}
	c.mu.Unlock()
	c.queue.requestStop()
	c.inventoryMu.Lock()
	if c.inventoryCancel != nil {
		c.inventoryCancel()
	}
	c.inventoryMu.Unlock()
	c.lifecycleMu.Unlock()
}

func (c *Console) Close() error {
	c.BeginShutdown()
	c.covers.close()
	grace, cancel := context.WithTimeout(context.Background(), lifecycleWriteTimeout)
	c.lifecycle.close(grace)
	cancel()
	c.lifecycleMu.Lock()
	c.mu.Lock()
	restore := c.settings.RestoreHostsOnStopOrExit
	c.mu.Unlock()
	hostsErr := c.editor.hosts.close(restore)
	c.lifecycleMu.Unlock()
	if hostsErr != nil {
		hostsErr = fmt.Errorf("退出时未能确认游戏连接已清理，下次启动后请恢复游戏直连：%w", hostsErr)
		slog.Error("游戏接入映射清理失败", "event_name", "hosts_cleanup_failed", "error", hostsErr)
	}
	c.mu.Lock()
	metadata := c.metadata
	monitor := c.monitor
	networkDiagnostics := c.networkDiagnostics
	monitorManualDone := c.monitorManualDone
	updateDone := c.updateDone
	done, importDone := c.batchDone, c.importDone
	c.mu.Unlock()
	c.inventoryMu.Lock()
	inventoryDone := c.inventoryDone
	c.inventoryMu.Unlock()
	err := c.stop()
	networkDiagnostics.close()
	if metadata != nil {
		metadata.Close()
	}
	if monitor != nil {
		monitor.Close()
	}
	if monitorManualDone != nil {
		<-monitorManualDone
	}
	if updateDone != nil {
		<-updateDone
	}
	if inventoryDone != nil {
		<-inventoryDone
	}
	if done != nil {
		<-done
	}
	if importDone != nil {
		<-importDone
	}
	c.lifecycleMu.Lock()
	err = errors.Join(err, c.closeEngineLocked())
	if c.store != nil {
		err = errors.Join(err, c.store.Close())
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
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob:; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
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
	if r.URL.Path == "/api/lifecycle" {
		c.serveLifecycle(w, r)
		return
	}
	if c.serveTerms(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/proxy/") || r.URL.Path == "/api/mihomo" || strings.HasPrefix(r.URL.Path, "/api/mihomo/") {
		c.serveDashboard(w, r)
		return
	}
	if r.URL.Path == "/media/preview" {
		c.servePreview(w, r)
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
		saved := c.editor.snapshot()
		result := struct {
			Running               bool                         `json:"running"`
			Settings              Settings                     `json:"settings"`
			ActiveSettings        Settings                     `json:"activeSettings"`
			RestartRequired       bool                         `json:"restartRequired"`
			Hosts                 HostsStatus                  `json:"hosts"`
			PortOK                bool                         `json:"portOK"`
			CDNError              string                       `json:"cdnError"`
			StorageError          string                       `json:"storageError,omitempty"`
			StorageConfirmation   string                       `json:"storageConfirmation,omitempty"`
			StorageIssues         map[string]storageFieldIssue `json:"storageIssues,omitempty"`
			WorkError             string                       `json:"workError,omitempty"`
			VRCXImportAvailable   bool                         `json:"vrcxImportAvailable"`
			VideoImportSuggested  bool                         `json:"videoImportSuggested"`
			ProxyModeMigrated     bool                         `json:"proxyModeMigrated,omitempty"`
			ActionErrors          map[string]string            `json:"actionErrors"`
			Batch                 Batch                        `json:"batch"`
			Import                ImportStatus                 `json:"import"`
			PortOwner             *desktop.Owner               `json:"portOwner,omitempty"`
			Queue                 QueueStatus                  `json:"queue"`
			LastBatch             Batch                        `json:"lastBatch"`
			HTTPSPortOK           bool                         `json:"httpsPortOK"`
			HTTPSPortOwner        *desktop.Owner               `json:"httpsPortOwner,omitempty"`
			SOCKS5PasswordSet     bool                         `json:"socks5PasswordSet"`
			SOCKS5CredentialError string                       `json:"socks5CredentialError,omitempty"`
			SOCKS5Service         *socks5ServiceStatus         `json:"socks5Service,omitempty"`
			ProxyNodes            proxyNodeStatus              `json:"proxyNodes"`
			Activation            Activation                   `json:"activation"`
			DefaultLogDir         string                       `json:"defaultLogDir"`
			DefaultDatabasePath   string                       `json:"defaultDatabasePath"`
			DefaultVideoDir       string                       `json:"defaultVideoDir"`
			DefaultBlockCacheDir  string                       `json:"defaultBlockCacheDir"`
			PortableDir           string                       `json:"portableDir"`
			CatalogSync           catalogSyncStatus            `json:"catalogSync"`
			UpstreamMonitor       upstreamstate.Status         `json:"upstreamMonitor"`
			Update                updateStatus                 `json:"update"`
		}{
			Running: running, Settings: saved, ActiveSettings: c.settings,
			RestartRequired:      saved != c.settings,
			VideoImportSuggested: c.videoImportSuggestion,
			ProxyModeMigrated:    c.proxyModeMigrated,
			PortOK:               running, HTTPSPortOK: running, CDNError: c.cdnError,
			Import: c.importStatus, Batch: c.batch, Queue: c.queue.snapshot(), LastBatch: c.lastBatch,
			SOCKS5PasswordSet:     saved.SOCKS5Password != "",
			SOCKS5CredentialError: saved.proxySecretError,
			SOCKS5Service:         c.socks5ServiceSnapshotLocked(),
			ProxyNodes:            c.proxyNodesStatusLocked(),
			Activation:            c.activation, DefaultLogDir: defaultLogDir(),
			DefaultDatabasePath: c.editor.defaultDatabasePath(), DefaultVideoDir: c.editor.defaultVideoDir(), DefaultBlockCacheDir: defaultBlockCacheDir(c.identity.Executable), PortableDir: c.editor.portableDir(),
			UpstreamMonitor: c.monitorSnapshotLocked(),
			CatalogSync:     c.catalogSyncSnapshotLocked(),
			Update:          c.updateSnapshotLocked(),
		}
		result.Settings.SOCKS5Password = ""
		result.ActiveSettings.SOCKS5Password = ""
		result.Queue.Desired = c.queueDesired
		service, vrcxDismissed := c.service, c.vrcxImportDismissed
		result.ActionErrors = make(map[string]string, len(c.actionErrors))
		for source, message := range c.actionErrors {
			result.ActionErrors[source] = message
		}
		result.Batch.Failures = append([]Failure(nil), c.batch.Failures...)
		// Port ownership and running must come from the same snapshot. Binding,
		// releasing and probing ports share mu; engine I/O never holds it.
		if !running {
			result.PortOK = c.videoListener != nil || portAvailable(c.videoAddress) == nil
			if !result.PortOK {
				result.PortOwner = desktop.PortOwner(c.videoAddress)
			}
			result.HTTPSPortOK = c.https != nil || portAvailable(c.httpsAddress) == nil
			if !result.HTTPSPortOK {
				result.HTTPSPortOwner = desktop.PortOwner(c.httpsAddress)
			}
		}
		c.mu.Unlock()
		if c.workOpenError != nil {
			result.WorkError = applog.SafeError(c.workOpenError).Error()
		}
		result.Hosts = c.editor.hosts.status()
		result.VideoImportSuggested = result.VideoImportSuggested && result.Hosts.Ready && !result.Hosts.Managed && !result.Hosts.Conflict
		if c.storageOpenError != nil {
			result.StorageError = applog.SafeError(c.storageOpenError).Error()
		}
		if c.storageConfirmation != nil {
			result.StorageConfirmation = applog.SafeError(c.storageConfirmation).Error()
		}
		if store, err := c.businessStore(); err != nil {
			var confirmation *storageConfirmationRequired
			if errors.As(err, &confirmation) {
				result.StorageConfirmation = applog.SafeError(err).Error()
			} else {
				result.StorageError = applog.SafeError(err).Error()
			}
		} else if !vrcxDismissed {
			count, err := store.SongUsageCount(r.Context())
			result.VRCXImportAvailable = err == nil && count == 0
		}
		if service != nil {
			if err := service.StorageError(); err != nil {
				result.StorageError = applog.SafeError(err).Error()
			}
			result.Queue.Titles = service.QueueTitles()
			for i := range result.Queue.Failures {
				f := &result.Queue.Failures[i]
				f.Name = result.Queue.Titles[strconv.FormatInt(f.ID, 10)]
			}
		}
		result.StorageIssues = c.storageSettingsIssues(result.ActiveSettings, service)
		writeJSON(w, result)
		return
	}
	if r.URL.Path == "/api/inventory" && r.Method == "GET" {
		writeJSON(w, c.localInventory())
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/history/events" {
		c.serveRequestHistoryEvents(w, r)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/block-demands/events" {
		c.serveBlockEvents(w, r)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/network/calibration/events" {
		c.serveNetworkCalibrationEvents(w, r)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/api/network/connectivity/events" {
		c.serveNetworkConnectivityEvents(w, r)
		return
	}
	c.serveManagement(w, r)
}

func (c *Console) serveAction(w http.ResponseWriter, r *http.Request) {
	if handler := c.editor.settingsHandler(r.URL.Path, true); handler != nil {
		handler(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.URL.Path == "/api/diagnostics/export" {
		c.serveDiagnosticExport(w, r)
		return
	}
	if r.URL.Path == "/api/update/check" {
		c.serveUpdateCheck(w, r)
		return
	}
	if r.URL.Path == "/api/update/notice" {
		c.serveUpdateNotice(w)
		return
	}
	if r.URL.Path == "/api/library/preview" {
		c.preparePreview(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/vrcx/") {
		c.serveVRCX(w, r)
		return
	}
	var err error
	var source string
	switch r.URL.Path {
	case "/api/import/dismiss-suggestion":
		c.mu.Lock()
		c.videoImportSuggestion = false
		c.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
		return
	case "/api/network/calibration/start":
		source = "network-calibration"
		var input struct{}
		if err = decodeRequest(json.NewDecoder(r.Body), &input); err == nil {
			err = c.controlNetworkCalibration(false)
		}
	case "/api/network/calibration/cancel":
		source = "network-calibration-cancel"
		err = c.controlNetworkCalibration(true)
	case "/api/network/connectivity/start":
		source = "network-connectivity"
		var input struct {
			Operation upstreamcheck.Operation `json:"operation"`
		}
		if err = decodeRequest(json.NewDecoder(r.Body), &input); err == nil {
			err = c.controlNetworkConnectivityOperation(false, input.Operation)
		}
	case "/api/network/connectivity/cancel":
		source = "network-connectivity-cancel"
		err = c.controlNetworkConnectivity(true)
	case "/api/network/socks5/check":
		source = "network-socks5"
		err = c.checkSOCKS5Service()
	case "/api/upstream/check":
		source = "upstream"
		err = c.requestMonitorCheck()
	case "/api/upstream/check/catalog":
		source = "upstream-catalog"
		err = c.requestMonitorCheck(upstreamstate.CheckCatalog)
	case "/api/upstream/check/playback":
		source = "upstream-playback"
		err = c.requestMonitorCheck(upstreamstate.CheckPlayback)
	case "/api/upstream/check/latency":
		source = "upstream-latency"
		err = c.requestMonitorCheck(upstreamstate.CheckLatency)
	case "/api/upstream/check/throughput":
		source = "upstream-throughput"
		err = c.requestMonitorCheck(upstreamstate.CheckThroughput)
	case "/api/exit", "/api/restart":
		c.lifecycle.request(ShutdownAction(strings.TrimPrefix(r.URL.Path, "/api/")), func(action ShutdownAction) {
			controller := http.NewResponseController(w)
			_ = controller.SetWriteDeadline(time.Now().Add(lifecycleWriteTimeout))
			writeJSON(w, struct {
				OK     bool           `json:"ok"`
				Action ShutdownAction `json:"action"`
			}{true, action})
			_ = controller.Flush()
			_ = controller.SetWriteDeadline(time.Time{})
			slog.Info("WebUI 操作已完成", "event_name", "console_action_completed", "action", r.URL.Path, "shutdown_action", action)
		})
		return
	case "/api/import/start":
		source = "import"
		var input struct {
			Source string `json:"source"`
		}
		if err = decodeRequest(json.NewDecoder(r.Body), &input); err == nil {
			err = c.startImport(input.Source)
		}
	case "/api/import/cleanup":
		source = "import"
		var input struct {
			ID        uint64 `json:"id"`
			Confirmed bool   `json:"confirmed"`
		}
		if err = decodeRequest(json.NewDecoder(r.Body), &input); err == nil {
			err = c.cleanupImport(input.ID, input.Confirmed)
		}
	case "/api/import/pause", "/api/import/resume", "/api/import/discard":
		source = "import"
		var input struct {
			ID        uint64 `json:"id"`
			Confirmed bool   `json:"confirmed"`
		}
		if err = decodeRequest(json.NewDecoder(r.Body), &input); err == nil {
			switch r.URL.Path {
			case "/api/import/pause":
				err = c.pauseImport(input.ID)
			case "/api/import/resume":
				err = c.resumeImport(input.ID)
			case "/api/import/discard":
				err = c.discardImport(input.ID, input.Confirmed)
			}
		}
	case "/api/inventory/scan":
		source = "inventory"
		c.startInventoryScan()
	case "/api/batch/scan":
		source = "batch"
		err = c.startBatchMode(true)
	case "/api/activation/enable":
		source = "activation"
		err = c.enableAcceleration(c.editor.hosts.status, c.changeHostsLocked)
	case "/api/activation/disable":
		source = "activation"
		err = c.disableAcceleration()
	case "/api/hosts/prepare":
		source = "hosts"
		err = c.prepareHostsRestore()
	case "/api/batch/start":
		source = "batch"
		err = c.startBatch()
	case "/api/batch/download":
		source = "batch"
		var input struct {
			StorageID string `json:"storageID"`
			cacheproxy.DownloadTarget
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err = decodeRequest(decoder, &input); err == nil {
			err = c.startBatchTarget(input.StorageID, input.DownloadTarget)
		}
	case "/api/batch/cancel":
		source = "batch-cancel"
		c.mu.Lock()
		if c.batchCancel != nil {
			c.batchCancel()
		}
		c.mu.Unlock()
	default:
		http.Error(w, "not found", 404)
		return
	}
	c.writeActionResult(w, r, source, err)
}

func (c *Console) writeActionResult(w http.ResponseWriter, r *http.Request, source string, err error) {
	c.recordActionError(source, err)
	writeActionResult(w, r, err)
}

func writeActionResult(w http.ResponseWriter, r *http.Request, err error) {
	if result, ok := w.(*faultResponseWriter); ok {
		result.reported = true
	}
	if err != nil {
		slog.Error("WebUI 操作失败", "event_name", "console_action_failed", "action", r.URL.Path, "error", err)
		if writeSettingsValidation(w, err) {
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(400)
		failure := map[string]string{"error": err.Error()}
		if errors.Is(err, errHostsAuthorizationCanceled) {
			failure["code"] = "uac_canceled"
		}
		writeJSON(w, failure)
		return
	}
	slog.Info("WebUI 操作已完成", "event_name", "console_action_completed", "action", r.URL.Path)
	writeJSON(w, map[string]bool{"ok": true})
}

// Each operation group owns its error; unrelated successes must not erase it.
// CDN errors are owned by start/stop/failCDN, including automatic and tray calls.
func (c *Console) recordActionError(source string, err error) {
	// Rejecting a draft neither creates an operational fault nor proves that
	// a previous fault recovered. Leave both retained boundaries untouched.
	if nonFaultError(err) {
		return
	}
	c.mu.Lock()
	if err == nil {
		delete(c.actionErrors, source)
	} else {
		if c.actionErrors == nil {
			c.actionErrors = make(map[string]string)
		}
		c.actionErrors[source] = err.Error()
	}
	c.mu.Unlock()
	c.reportFault("action:"+source, err)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
