package console

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/desktop"
	"still-wanna-dance/internal/workfiles"
)

// Only fields exposed by the wizard may change. Advanced settings stay intact.
type setupPlan struct {
	ID            string  `json:"id"`
	BaseVersion   string  `json:"baseVersion"`
	Page          string  `json:"page"`
	StorageDir    string  `json:"storageDir"`
	CustomStorage bool    `json:"customStorage"`
	MaxCacheBytes int64   `json:"maxCacheBytes"`
	UpstreamMode  string  `json:"upstreamMode"`
	ProxyBackend  string  `json:"proxyBackend"`
	SOCKS5Address string  `json:"socks5Address"`
	SOCKS5User    string  `json:"socks5Username"`
	Password      *string `json:"socks5Password"`
	ImportSource  string  `json:"importSource"`
	ProxyImportID string  `json:"proxyImportID"`
}

// The approved continuation contains no credentials. A restart can start its
// import once, and only with the exact configuration that was confirmed.
type SetupApplication struct {
	ID            string `json:"id"`
	Phase         string `json:"phase"`
	Error         string `json:"error,omitempty"`
	ConfigSaved   bool   `json:"configSaved"`
	ImportStarted bool   `json:"importStarted"`
	ImportSource  string `json:"importSource,omitempty"`
	TargetVersion string `json:"targetVersion,omitempty"`
}

func (c *Console) setupSettingsVersion(s Settings) (string, error) {
	key, err := loadProxyIdentityKey(c.files().ChannelKey)
	if err != nil {
		return "", fmt.Errorf("无法验证引导配置版本，请恢复通道身份密钥后重试：%w", err)
	}
	defer clear(key)
	data, _ := json.Marshal(s)
	// Include unreadable protected credentials in the concurrency check as well.
	if s.proxySecretError != "" {
		data = append(data, []byte(s.proxySecret)...)
	}
	// Nodes are intentionally absent from Settings JSON, but must participate
	// in stale-draft detection and restart continuation verification.
	nodes, _ := json.Marshal([]string{s.nodeSnapshot, s.nodeSecret})
	data = append(data, nodes...)
	clear(nodes)
	defer clear(data)
	// Domain separation keeps this verifier independent of public channel IDs.
	// Never fall back to a public digest or a process-local key: restart matching
	// must cover credentials, including protected credentials we cannot decrypt.
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("swd-setup-settings-v1\x00"))
	mac.Write(data)
	return "hmac-v1-" + hex.EncodeToString(mac.Sum(nil)), nil
}

func (a SetupApplication) public() SetupApplication {
	// The browser only needs the execution result, including for legacy receipts
	// whose internal target version was an unkeyed credential verifier.
	a.TargetVersion = ""
	return a
}

func (c *Console) validateSetupPlan(p setupPlan) (Settings, error) {
	// Advisory checks must not wait for a hosts UAC operation holding lifecycleMu.
	// Final approval repeats these checks under that lock before writing settings.
	return c.validateSetupPlanSnapshot(p)
}

func (c *Console) validateSetupPlanSnapshot(p setupPlan) (Settings, error) {
	c.mu.Lock()
	s := c.savedSettings
	closing, accepted := c.closing, c.terms.valid()
	c.mu.Unlock()
	if closing || !accepted {
		return s, errors.New("请先同意条款，并等待应用就绪")
	}
	version, err := c.setupSettingsVersion(s)
	if err != nil {
		return s, err
	}
	if p.BaseVersion != version {
		return s, errors.New("正式设置已在别处改变，请放弃本轮草稿并重新进入引导")
	}
	if p.Page == "" || p.Page == "storage" {
		s.StorageDir, s.MaxCacheBytes = p.StorageDir, p.MaxCacheBytes
		if err := c.validateStorageSelection(s.StorageDir, p.CustomStorage); err != nil {
			return s, err
		}
	}
	if p.Page == "" || p.Page == "proxy" {
		if p.UpstreamMode == "" {
			return s, errors.New("请至少选择一种连接方式")
		}
		s.UpstreamMode, s.SOCKS5Address, s.SOCKS5Username = p.UpstreamMode, p.SOCKS5Address, p.SOCKS5User
		s.ProxyBackend = p.ProxyBackend
		if p.Password != nil {
			s.SOCKS5Password, s.proxySecret, s.proxySecretError = *p.Password, "", ""
		}
		s.nodeImportID = p.ProxyImportID
		s, err = c.mergeProxyNodes(s, s)
		if err != nil {
			return s, err
		}
	}
	s, err = c.resolveSettings(s)
	if err != nil {
		return s, err
	}
	if p.Page == "" || p.Page == "storage" {
		if err = writableDir(s.StorageDir); err != nil {
			return s, fmt.Errorf("存储目录不可写：%w", err)
		}
		if _, err = cacheproxy.InspectStorage(s.StorageDir); err != nil {
			return s, err
		}
		if s.StorageDir == c.settings.StorageDir && c.storageUsageKnown() {
			if exists, _ := cacheproxy.InspectStorage(s.StorageDir); !exists {
				return s, errors.New("当前已使用的存储库失联，请先恢复原目录与数据库")
			}
		}
	}
	if (p.Page == "" || p.Page == "import" || p.Page == "storage") && strings.TrimSpace(p.ImportSource) != "" {
		if _, err = setupImportSource(p.ImportSource, s.StorageDir, p.Page == ""); err != nil {
			return s, err
		}
	}
	return s, nil
}

func setupImportSource(source, storage string, checkTarget bool) (string, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", fmt.Errorf("无法读取导入目录：%w", err)
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return "", errors.New("请选择可读取的导入目录")
	}
	if checkTarget {
		target, err := desktop.CanonicalPath(storage)
		if err != nil {
			return "", err
		}
		if insideImportRoot(target, source) {
			return "", errors.New("导入源不能位于目标存储库内")
		}
	}
	return source, nil
}

func (c *Console) writeSetupApplication(a SetupApplication) error {
	data, err := json.Marshal(a)
	if err == nil && a.ID != "" {
		err = workfiles.Write(c.setupReceiptPath(a.ID), data)
	}
	if err == nil {
		err = workfiles.Write(c.files().State("setup-apply"), data)
	}
	c.mu.Lock()
	if err != nil {
		a.Phase, a.Error = "failed", "记录应用结果失败："+err.Error()
	}
	c.setupApplication = a
	c.mu.Unlock()
	return err
}

func (c *Console) setupReceiptPath(id string) string {
	sum := sha256.Sum256([]byte(id))
	return c.files().State("setup-apply-" + hex.EncodeToString(sum[:]))
}

func (c *Console) readSetupReceipt(id string) (SetupApplication, error) {
	if id == "" || len(id) > 80 {
		return SetupApplication{}, errors.New("无效的应用清单 ID")
	}
	c.mu.Lock()
	current := c.setupApplication
	c.mu.Unlock()
	if current.ID == id {
		return current, nil
	}
	f, err := os.Open(c.setupReceiptPath(id))
	if err != nil {
		return SetupApplication{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 8193))
	var receipt SetupApplication
	if err != nil || len(data) > 8192 || json.Unmarshal(data, &receipt) != nil || receipt.ID != id {
		return SetupApplication{}, errors.New("无法确认此清单的历史回执；不会重复执行")
	}
	return receipt, nil
}

func (c *Console) loadSetupApplication() {
	data, err := os.ReadFile(c.files().State("setup-apply"))
	if os.IsNotExist(err) {
		return
	}
	var a SetupApplication
	if err != nil || len(data) > 8192 || json.Unmarshal(data, &a) != nil || a.ID == "" {
		c.setupApplication = SetupApplication{Phase: "failed", Error: "无法读取上次引导的应用记录；未启动导入"}
		return
	}
	if a.Phase == "preparing" || a.Phase == "importing" {
		a.Phase, a.Error = "failed", "上次应用被中断，请核对正式设置与导入任务；不会自动重试导入"
		_ = c.writeSetupApplication(a)
	}
	c.setupApplication = a
}

func (c *Console) applySetupPlan(p setupPlan) (SetupApplication, error) {
	c.setupApplyMu.Lock()
	defer c.setupApplyMu.Unlock()
	c.lifecycleMu.Lock()
	c.mu.Lock()
	previous, active := c.setupApplication, c.settings
	taskRunning := c.importStatus.Running || c.batch.Running || c.queue.Running
	c.mu.Unlock()
	if p.ID != "" && len(p.ID) <= 80 {
		if receipt, readErr := c.readSetupReceipt(p.ID); readErr == nil {
			c.lifecycleMu.Unlock()
			return receipt, nil
		} else if !os.IsNotExist(readErr) {
			c.lifecycleMu.Unlock()
			return previous, readErr
		}
	}
	if previous.ID == p.ID && p.ID != "" {
		c.lifecycleMu.Unlock()
		return previous, nil // A lost acknowledgement must never duplicate an import.
	}
	if previous.Phase == "restart" || previous.Phase == "importing" {
		c.lifecycleMu.Unlock()
		return previous, errors.New("上一份清单仍在应用，请等待完成")
	}
	if p.ID == "" || len(p.ID) > 80 || p.Page != "" {
		c.lifecycleMu.Unlock()
		return previous, errors.New("无效的应用清单")
	}
	s, err := c.validateSetupPlanSnapshot(p)
	if err != nil {
		c.lifecycleMu.Unlock()
		return previous, err
	}
	effective := s
	effective.nodeImportID = ""
	if taskRunning && (effective != active || strings.TrimSpace(p.ImportSource) != "") {
		c.lifecycleMu.Unlock()
		return previous, errors.New("现有导入或队列任务仍在运行，请结束后再应用；本轮草稿保留")
	}
	version, err := c.setupSettingsVersion(s)
	if err != nil {
		c.lifecycleMu.Unlock()
		return previous, err
	}
	a := SetupApplication{ID: p.ID, Phase: "preparing", TargetVersion: version}
	if strings.TrimSpace(p.ImportSource) != "" {
		a.ImportSource, err = setupImportSource(p.ImportSource, s.StorageDir, true)
		if err != nil {
			c.lifecycleMu.Unlock()
			return previous, err
		}
	}
	if err = c.writeSetupApplication(a); err == nil {
		err = c.saveSettingsLocked(s, p.Password == nil)
		if err == nil {
			a.ConfigSaved = true
			// Use the exact normalized, persisted settings for restart matching.
			c.mu.Lock()
			a.TargetVersion, err = c.setupSettingsVersion(c.savedSettings)
			c.mu.Unlock()
			a.Phase = "restart"
			if err == nil {
				err = c.writeSetupApplication(a)
			}
		}
	}
	c.lifecycleMu.Unlock()
	if err != nil {
		a.Phase, a.Error = "failed", err.Error()
		_ = c.writeSetupApplication(a)
		return a, nil // Return partial results instead of claiming nothing happened.
	}
	if effective == active {
		c.resumeSetupApplicationLocked(c.startImport)
		c.mu.Lock()
		a = c.setupApplication
		c.mu.Unlock()
	}
	return a, nil
}

func (c *Console) resumeSetupApplication(startImport func(string) error) {
	c.setupApplyMu.Lock()
	defer c.setupApplyMu.Unlock()
	c.resumeSetupApplicationLocked(startImport)
}

func (c *Console) resumeSetupApplicationLocked(startImport func(string) error) {
	c.mu.Lock()
	a, active, accepted := c.setupApplication, c.settings, c.terms.valid()
	c.mu.Unlock()
	if a.Phase != "restart" || !accepted {
		return
	}
	version, err := c.setupSettingsVersion(active)
	if err != nil || a.TargetVersion != version {
		a.Phase, a.Error = "failed", "当前配置与已确认清单不一致；未启动导入，请重新核对"
		if err != nil {
			a.Error = err.Error() + "；未启动导入"
		}
		a.TargetVersion = "" // Do not preserve a legacy unkeyed verifier on rewrite.
		_ = c.writeSetupApplication(a)
		return
	}
	if a.ImportSource != "" {
		// Persist the claim BEFORE starting. Crashes never replay this operation.
		a.Phase = "importing"
		if c.writeSetupApplication(a) != nil {
			return
		}
		if err := startImport(a.ImportSource); err != nil {
			a.Phase, a.Error = "failed", "设置已应用，导入未能启动："+err.Error()
			_ = c.writeSetupApplication(a)
			return
		}
		a.ImportStarted = true
	}
	a.Phase = "completed"
	if c.writeSetupApplication(a) == nil {
		_ = c.saveSetupProgress(SetupProgress{SchemaVersion: 1, Step: "finish", State: "completed"})
	}
}
