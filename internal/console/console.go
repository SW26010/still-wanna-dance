package console

import (
	"context"
	"crypto/rand"
	_ "embed"
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
	"strings"
	"sync"
	"time"

	"stepstash/internal/cacheproxy"
	"stepstash/internal/desktop"
	"stepstash/internal/vrclog"
)

//go:embed index.html
var page string

type Settings struct {
	MaxCacheBytes int64  `json:"maxCacheBytes"`
	SongsDir      string `json:"songsDir"`
	CacheDir      string `json:"cacheDir"`
	LogDir        string `json:"logDir"`
}

type Console struct {
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
	service             *cacheproxy.Server
	httpServer          *http.Server
	lastError           string
	batch               Batch
	lastBatch           Batch
	batchCancel         context.CancelFunc
	batchDone           chan struct{}
	queue               QueueStatus
	queueCancel         context.CancelFunc
	queueDone           chan struct{}
	closing             bool
	client              *http.Client
	apiBase             string
	dns                 *directDNS
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
	c := &Console{configPath: configPath, address: address, videoAddress: "127.0.0.1:80", token: hex.EncodeToString(b), apiBase: "http://api.udon.dance", client: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	c.settings = Settings{SongsDir: "wannadance-song", CacheDir: "stepstash-cache"}
	c.dns = &directDNS{}
	c.client.Transport = &http.Transport{DialContext: c.dns.DialContext, ResponseHeaderTimeout: 20 * time.Second}
	if b, err := os.ReadFile(configPath); err == nil {
		if err = json.Unmarshal(b, &c.settings); err != nil {
			return nil, fmt.Errorf("读取控制台配置：%w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	c.settings, err = c.resolveSettings(c.settings)
	if err == nil {
		c.loadSnapshots()
	}
	return c, err
}

// Relative paths belong to the configuration, independent of the launch directory.
func (c *Console) resolveSettings(s Settings) (Settings, error) {
	for _, p := range []*string{&s.SongsDir, &s.CacheDir, &s.LogDir} {
		if strings.TrimSpace(*p) != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(filepath.Dir(c.configPath), *p)
		}
	}
	return absoluteSettings(s)
}

// Keep directories inside the portable folder movable; external libraries stay absolute.
func (c *Console) storedSettings(s Settings) Settings {
	base := filepath.Dir(c.configPath)
	for _, p := range []*string{&s.SongsDir, &s.CacheDir, &s.LogDir} {
		if pathContains(base, *p) {
			if rel, err := filepath.Rel(base, *p); err == nil {
				*p = rel
			}
		}
	}
	return s
}

func absoluteSettings(s Settings) (Settings, error) {
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
	if strings.TrimSpace(s.SongsDir) == "" || strings.TrimSpace(s.CacheDir) == "" {
		return s, errors.New("请填写歌曲库和临时缓存目录")
	}
	var err error
	s.SongsDir, err = filepath.Abs(s.SongsDir)
	if err != nil {
		return s, err
	}
	s.CacheDir, err = filepath.Abs(s.CacheDir)
	if err != nil {
		return s, err
	}
	a, b := strings.ToLower(s.SongsDir), strings.ToLower(s.CacheDir)
	if pathContains(a, b) || pathContains(b, a) {
		return s, errors.New("歌曲库和临时缓存必须是互不包含的独立目录")
	}
	return s, nil
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("控制台正在退出")
	}
	if c.httpServer != nil || c.batch.Running || c.queue.Running {
		return errors.New("请先关闭 CDN、队列预缓存和批量任务，再修改目录")
	}
	var err error
	s, err = c.resolveSettings(s)
	if err != nil {
		return err
	}
	for _, p := range []string{s.SongsDir, s.CacheDir} {
		if err = writableDir(p); err != nil {
			return fmt.Errorf("目录不可写：%w", err)
		}
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
	changedLibrary := !sameLibrary(c.settings, s)
	c.settings = s
	if changedLibrary {
		c.loadSnapshots()
	}
	slog.Info("settings_saved", "songs_dir", s.SongsDir, "cache_dir", s.CacheDir, "vrchat_log_dir", s.LogDir)
	if c.service != nil {
		_ = c.service.Close()
		c.service = nil
	}
	return nil
}

// Call with c.mu held. Playback and independent downloads share the cache engine.
func (c *Console) ensureEngine() error {
	if c.service != nil {
		return nil
	}
	for _, p := range []string{c.settings.SongsDir, c.settings.CacheDir} {
		if err := writableDir(p); err != nil {
			return fmt.Errorf("缓存目录不可写：%w", err)
		}
	}
	cfg := cacheproxy.DefaultConfig()
	cfg.Logger = slog.Default().With("component", "cache")
	cfg.SongsDir = c.settings.SongsDir
	cfg.CacheDir = c.settings.CacheDir
	cfg.MaxCacheBytes = c.settings.MaxCacheBytes
	cfg.StatsPath = filepath.Join(filepath.Dir(c.configPath), "stepstash-usage.sqlite")
	cfg.DialContext = c.dns.DialContext
	s, err := cacheproxy.New(cfg)
	if err != nil {
		return err
	}
	c.service = s
	return nil
}

func (c *Console) start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("控制台正在退出")
	}
	if c.httpServer != nil {
		return nil
	}
	// Hosts do not affect binding: a running CDN can be connected using the separate hosts action.
	_ = readHostsStatus()
	l, err := net.Listen("tcp4", c.videoAddress)
	if err != nil {
		return desktop.PortError(c.videoAddress, err)
	}
	if err = c.ensureEngine(); err != nil {
		l.Close()
		return err
	}
	h := &http.Server{Handler: c.service, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError)}
	c.httpServer = h
	c.lastError = ""
	slog.Info("cdn_started", "address", l.Addr().String())
	go func() {
		err := h.Serve(l)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("cdn_failed", "error", err)
			c.mu.Lock()
			c.lastError = err.Error()
			if c.httpServer == h {
				c.httpServer = nil
			}
			c.mu.Unlock()
		}
	}()
	return nil
}

func (c *Console) stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.httpServer == nil {
		return nil
	}
	_ = c.httpServer.Close()
	c.httpServer = nil
	slog.Info("cdn_stopped")
	return nil
}

func (c *Console) Close() error {
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
	c.mu.Lock()
	if c.service != nil {
		err = c.service.Close()
		c.service = nil
	}
	c.mu.Unlock()
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
	if r.Method == "GET" && r.URL.Path == "/api/identity" {
		writeJSON(w, desktop.AppIdentity)
		return
	}
	if r.URL.Path == "/api/status" && r.Method == "GET" {
		c.mu.Lock()
		running := c.httpServer != nil
		result := struct {
			Running   bool           `json:"running"`
			Settings  Settings       `json:"settings"`
			Hosts     HostsStatus    `json:"hosts"`
			PortOK    bool           `json:"portOK"`
			Error     string         `json:"error"`
			Batch     Batch          `json:"batch"`
			PortOwner *desktop.Owner `json:"portOwner,omitempty"`
			Queue     QueueStatus    `json:"queue"`
			LastBatch Batch          `json:"lastBatch"`
		}{running, c.settings, readHostsStatus(), running, c.lastError, c.batch, nil, c.queue, c.lastBatch}
		result.Queue.Songs = append([]vrclog.Song(nil), c.queue.Songs...)
		result.Queue.Failures = append([]Failure(nil), c.queue.Failures...)
		result.Batch.Failures = append([]Failure(nil), c.batch.Failures...)
		c.mu.Unlock()
		if !running {
			result.PortOK = portAvailable(c.videoAddress) == nil
			if !result.PortOK {
				result.PortOwner = desktop.PortOwner(c.videoAddress)
			}
		}
		writeJSON(w, result)
		return
	}
	if r.URL.Path == "/api/inventory" && r.Method == "GET" {
		writeJSON(w, c.localInventory())
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
	case "/api/batch/scan":
		err = c.startBatchMode(true)
	case "/api/batch/switch":
		err = c.switchTask(true)
	case "/api/queue/switch":
		err = c.switchTask(false)
	case "/api/start":
		err = c.start()
	case "/api/stop":
		err = c.stop()
	case "/api/settings":
		var s Settings
		if err = json.NewDecoder(r.Body).Decode(&s); err == nil {
			err = c.save(s)
		}
	case "/api/hosts/enable":
		err = changeHosts("enable")
	case "/api/hosts/disable":
		err = changeHosts("disable")
	case "/api/batch/start":
		err = c.startBatch()
	case "/api/queue/start":
		err = c.startQueue()
	case "/api/queue/stop":
		c.mu.Lock()
		if c.queueCancel != nil {
			c.queueCancel()
		}
		c.mu.Unlock()
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
	if err != nil {
		slog.Error("console_action_failed", "action", r.URL.Path, "error", err)
		c.mu.Lock()
		c.lastError = err.Error()
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(400)
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("console_action_completed", "action", r.URL.Path)
	writeJSON(w, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
