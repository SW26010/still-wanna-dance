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
	"strconv"
	"strings"
	"sync"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/branding"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/catalogsync"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/playbacksource"
	"still-wanna-dance/internal/upstreamrequest"
	"still-wanna-dance/internal/upstreamstate"
	"still-wanna-dance/internal/vrclog"
)

//go:embed assets/storage-benefit.svg
var assets embed.FS

type Console struct {
	store                  *cacheproxy.Store
	storeOpenError         error
	work                   *cacheproxy.WorkDirectory // Borrowed from the application entry point.
	channelIdentity        proxyChannelIdentity
	vrcxMu                 sync.Mutex // preview/apply/discard; acquire before lifecycleMu
	vrcxPreview            *vrcxPreview
	vrcxNotify             bool                        // startup Toast only; set during automatic library creation
	vrcxImportDismissed    bool                        // protected by mu; persisted user choice outside editable Settings
	videoImportSuggestion  bool                        // protected by mu; only the first run of a configuration, until acknowledged
	proxyModeMigrated      bool                        // startup notice; migration is already persisted
	proxyImports           map[string]proxyImportDraft // protected by mu
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
	autoStartOnce       sync.Once
	autoStartPending    bool // protected by mu; startup deferred until consent succeeds
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
	resources           *upstreamrequest.Resources
	requestRevision     uint64
	metadata            *catalogsync.Sync
	monitor             *upstreamstate.Monitor
	upstreamRequested   bool
	monitorManual       bool
	monitorManualAt     time.Time
	monitorManualDone   chan struct{}
	networkDiagnostics  *networkDiagnostics
	pathPickerCancel    context.CancelFunc // protected by mu; serializes all path dialogs
	pathPicker          func(context.Context, desktop.PathDialog) (string, error)
}

func New(configPath, address string, work *cacheproxy.WorkDirectory) (*Console, error) {
	c, err := newConsole(configPath, address, nil, work)
	if err == nil && c.store != nil {
		if seedErr := c.store.ApplyBundledMediaSeed(context.Background()); seedErr != nil {
			return nil, errors.Join(seedErr, c.Close())
		}
	}
	return c, err
}

func newConsole(configPath, address string, dns directResolver, work *cacheproxy.WorkDirectory) (*Console, error) {
	if work == nil {
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
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	c := &Console{work: work, configPath: configPath, address: address, videoAddress: "127.0.0.1:80", httpsAddress: "127.0.0.1:443", token: hex.EncodeToString(b), apiBase: playbacksource.DefaultBaseURL, checksumURL: cacheproxy.UdonCatalogURL}
	c.previewKey = rand.Text()
	c.identity = identity
	c.hosts = newHostsController(hostsPath(), launchHostsSession)
	c.lifecycle = newApplicationLifecycle()
	c.dns = dns
	c.resources = upstreamrequest.NewResources()
	loaded, err := loadConfigFile(configPath)
	if err != nil {
		return nil, err
	}
	c.videoImportSuggestion = !loaded.Exists
	c.vrcxImportDismissed = loaded.VRCXImportDismissed
	c.settings = loaded.Settings
	err = c.validateResourcePaths(c.settings)
	c.savedSettings = c.settings
	c.loadedNodeCount = len(nodeDocument(c.settings.nodeSnapshot).Nodes)
	c.savedNodeCount = c.loadedNodeCount
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
		unconfigured := c.settings.DatabasePath == "" && c.settings.VideoDir == ""
		if unconfigured {
			c.storeOpenError = c.prepareDefaultLibrary()
		} else {
			c.store, c.storeOpenError = cacheproxy.OpenStore(libraryPaths(c.settings).DatabasePath)
		}
		if !loaded.Exists || unconfigured && c.store != nil {
			err = saveConfigSettings(c.configPath, c.settings)
		}
		c.savedSettings = c.settings
	}
	if err == nil {
		c.loadSnapshots()
		c.loadImportTask()
		if c.settings.UpstreamMode == "direct" {
			c.requestRevision = upstreamrequest.Default.Publish(c.client.Transport.(upstreamrequest.Candidates))
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
		if err := c.enableAcceleration(c.hosts.status, c.changeHostsLocked); err != nil {
			slog.Error("auto_acceleration_failed", "error", err)
		}
	}
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
	c.mu.Unlock()
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
	defer c.mu.Lock()
	if relay != nil {
		relay.close()
	}
	if server == nil {
		return
	}
	_ = server.Close()
	slog.Info("cdn_stopped")
}

// BeginShutdown rejects new work and cancels background tasks without waiting
// for them. The owner calls it before draining HTTP requests, then calls Close
// after closing HTTP connections to wait for tasks and flush the cache engine.
// It is safe to call repeatedly.
func (c *Console) BeginShutdown() {
	c.lifecycle.beginShutdown()
	// Cancel a pending authorization before waiting for lifecycleMu. Existing
	// helper connections remain usable for cleanup without a new UAC request.
	c.hosts.cancel()
	c.lifecycleMu.Lock()
	c.mu.Lock()
	c.closing = true
	c.notifyBlockEngineLocked()
	if c.pathPickerCancel != nil {
		c.pathPickerCancel()
	}
	c.cancelTermsExitLocked()
	c.queueDesired = false
	if c.requestRevision != 0 {
		upstreamrequest.Default.Release(c.requestRevision)
	}
	c.resources.Close()
	if c.networkDiagnostics != nil {
		if c.networkDiagnostics.connectivityTimer != nil {
			c.networkDiagnostics.connectivityTimer.Stop()
		}
		c.networkDiagnostics.connectivity.NextCheck = time.Time{}
		if c.networkDiagnostics.connectivityCancel != nil {
			c.networkDiagnostics.connectivityCancel()
		}
		c.networkDiagnostics.connectivity.Closed = true
		c.publishNetworkConnectivityLocked()
		if c.networkDiagnostics.calibration != nil {
			c.networkDiagnostics.calibration.Shutdown()
		}
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
	grace, cancel := context.WithTimeout(context.Background(), lifecycleWriteTimeout)
	c.lifecycle.close(grace)
	cancel()
	c.lifecycleMu.Lock()
	c.mu.Lock()
	restore := c.settings.RestoreHostsOnStopOrExit
	c.mu.Unlock()
	hostsErr := c.hosts.close(restore)
	c.lifecycleMu.Unlock()
	if hostsErr != nil {
		hostsErr = fmt.Errorf("退出时未能确认游戏连接已清理，下次启动后请恢复游戏直连：%w", hostsErr)
		slog.Error("hosts_cleanup_failed", "error", hostsErr)
	}
	c.mu.Lock()
	metadata := c.metadata
	monitor := c.monitor
	networkDiagnostics := c.networkDiagnostics
	monitorManualDone := c.monitorManualDone
	done, importDone, queueDone := c.batchDone, c.importDone, c.queueDone
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
		result := struct {
			Running               bool                    `json:"running"`
			Settings              Settings                `json:"settings"`
			ActiveSettings        Settings                `json:"activeSettings"`
			RestartRequired       bool                    `json:"restartRequired"`
			Hosts                 HostsStatus             `json:"hosts"`
			PortOK                bool                    `json:"portOK"`
			CDNError              string                  `json:"cdnError"`
			StorageError          string                  `json:"storageError,omitempty"`
			VRCXImportAvailable   bool                    `json:"vrcxImportAvailable"`
			VideoImportSuggested  bool                    `json:"videoImportSuggested"`
			ProxyModeMigrated     bool                    `json:"proxyModeMigrated,omitempty"`
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
			DefaultDatabasePath   string                  `json:"defaultDatabasePath"`
			DefaultVideoDir       string                  `json:"defaultVideoDir"`
			PortableDir           string                  `json:"portableDir"`
			CatalogSync           catalogSyncStatus       `json:"catalogSync"`
			UpstreamMonitor       upstreamstate.Status    `json:"upstreamMonitor"`
		}{
			Running: running, Settings: c.savedSettings, ActiveSettings: c.settings,
			RestartRequired:      c.savedSettings != c.settings,
			VideoImportSuggested: c.videoImportSuggestion,
			ProxyModeMigrated:    c.proxyModeMigrated,
			PortOK:               running, HTTPSPortOK: running, CDNError: c.cdnError,
			Import: c.importStatus, Batch: c.batch, Queue: c.queue, LastBatch: c.lastBatch,
			SOCKS5PasswordSet:     c.savedSettings.SOCKS5Password != "",
			SOCKS5CredentialError: c.savedSettings.proxySecretError,
			ProxyNodes:            c.proxyNodesStatusLocked(),
			Activation:            c.activation, DefaultLogDir: defaultLogDir(),
			DefaultDatabasePath: c.defaultDatabasePath(), DefaultVideoDir: c.defaultVideoDir(), PortableDir: c.portableDir(),
			UpstreamMonitor: c.monitorSnapshotLocked(),
			CatalogSync:     c.catalogSyncSnapshotLocked(),
		}
		result.Settings.SOCKS5Password = ""
		result.ActiveSettings.SOCKS5Password = ""
		result.Queue.Desired = c.queueDesired
		service, vrcxDismissed := c.service, c.vrcxImportDismissed
		result.ActionErrors = make(map[string]string, len(c.actionErrors))
		for source, message := range c.actionErrors {
			result.ActionErrors[source] = message
		}
		result.Queue.Songs = append([]vrclog.Song(nil), c.queue.Songs...)
		result.Queue.Active = append([]int64(nil), c.queue.Active...)
		result.Queue.Failures = append([]Failure(nil), c.queue.Failures...)
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
		result.Hosts = c.hosts.status()
		result.VideoImportSuggested = result.VideoImportSuggested && result.Hosts.Ready && !result.Hosts.Managed && !result.Hosts.Conflict
		if store, err := c.businessStore(); err != nil {
			result.StorageError = applog.SafeError(err).Error()
		} else if !vrcxDismissed {
			count, err := store.SongUsageCount(r.Context())
			result.VRCXImportAvailable = err == nil && count == 0
		}
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
			result.Traffic = cacheproxy.ReadTrafficStats(libraryPaths(result.ActiveSettings).DatabasePath)
		}
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
	if r.URL.Path == "/api/path/select" {
		c.servePathPicker(w, r)
		return
	}
	if r.URL.Path == "/api/library/preview" {
		c.preparePreview(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/storage/") {
		c.serveStorage(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/vrcx/") {
		c.serveVRCX(w, r)
		return
	}
	var err error
	switch r.URL.Path {
	case "/api/import/dismiss-suggestion":
		c.mu.Lock()
		c.videoImportSuggestion = false
		c.mu.Unlock()
		writeJSON(w, map[string]bool{"ok": true})
		return
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
	case "/api/network/calibration/start":
		var input struct{}
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			err = c.controlNetworkCalibration(false)
		}
	case "/api/network/calibration/cancel":
		err = c.controlNetworkCalibration(true)
	case "/api/network/connectivity/start":
		var input struct{}
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			err = c.controlNetworkConnectivity(false)
		}
	case "/api/network/connectivity/cancel":
		err = c.controlNetworkConnectivity(true)
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
		c.lifecycle.request(ShutdownAction(strings.TrimPrefix(r.URL.Path, "/api/")), func(action ShutdownAction) {
			controller := http.NewResponseController(w)
			_ = controller.SetWriteDeadline(time.Now().Add(lifecycleWriteTimeout))
			writeJSON(w, struct {
				OK     bool           `json:"ok"`
				Action ShutdownAction `json:"action"`
			}{true, action})
			_ = controller.Flush()
			_ = controller.SetWriteDeadline(time.Time{})
			slog.Info("console_action_completed", "action", r.URL.Path, "shutdown_action", action)
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
	case "/api/import/pause", "/api/import/resume", "/api/import/discard":
		var input struct {
			ID        uint64 `json:"id"`
			Confirmed bool   `json:"confirmed"`
		}
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
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
		c.startInventoryScan()
	case "/api/batch/scan":
		err = c.startBatchMode(true)
	case "/api/activation/enable":
		err = c.enableAcceleration(c.hosts.status, c.changeHostsLocked)
	case "/api/activation/disable":
		err = c.disableAcceleration()
	case "/api/hosts/prepare":
		err = c.prepareHostsRestore()
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
		// Read the bounded request before taking the settings lifecycle lock.
		var input json.RawMessage
		if err = json.NewDecoder(r.Body).Decode(&input); err == nil {
			err = c.saveSettingsPatch(input)
		}
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
		failure := map[string]string{"error": err.Error()}
		if errors.Is(err, errHostsAuthorizationCanceled) {
			failure["code"] = "uac_canceled"
		}
		writeJSON(w, failure)
		return
	}
	slog.Info("console_action_completed", "action", r.URL.Path)
	writeJSON(w, map[string]bool{"ok": true})
}

// Each operation group owns its error; unrelated successes must not erase it.
// CDN errors are owned by start/stop/failCDN, including automatic and tray calls.
func (c *Console) recordActionError(path string, err error) {
	source := strings.Split(strings.TrimPrefix(path, "/api/"), "/")[0]
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
