package console

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/blocksched"
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamnet"
)

type Settings struct {
	nodeSnapshot                 string
	nodeSecret                   string
	nodeSecretError              string
	nodeImportID                 string // transient request only; cleared before publish
	proxySecret                  string
	proxySecretError             string
	ThroughputIntervalMinutes    int    `json:"throughputIntervalMinutes"`
	QueueWithGateway             bool   `json:"queueWithGateway"`
	RequestRetentionDays         int    `json:"requestRetentionDays"`
	RequestRetentionMode         string `json:"requestRetentionMode"`
	AutoAccelerate               bool   `json:"autoAccelerate"`
	AutoOpenWebUI                bool   `json:"autoOpenWebUI"`
	Theme                        string `json:"theme"`
	LogLevel                     string `json:"logLevel"`
	RestoreHostsOnStopOrExit     bool   `json:"restoreHostsOnStopOrExit"`
	PrepareAheadSeconds          int    `json:"prepareAheadSeconds"`
	DownloadConcurrencySoftLimit int    `json:"downloadConcurrencySoftLimit"`
	DownloadGrowIntervalMS       int    `json:"downloadGrowIntervalMS"`
	LocalProtectionGraceSeconds  int    `json:"localProtectionGraceSeconds"`
	UpstreamMode                 string `json:"upstreamMode"`
	ProxyBackend                 string `json:"proxyBackend"`
	SOCKS5Address                string `json:"socks5Address"`
	SOCKS5Username               string `json:"socks5Username"`
	SOCKS5Password               string `json:"socks5Password,omitempty"`
	VideoBudgetBytes             int64  `json:"videoBudgetBytes"`
	VideoStorageMode             string `json:"videoStorageMode"`
	RetainFavorites              bool   `json:"retainFavorites"`
	AggressiveBlockReclamation   string `json:"aggressiveBlockReclamation"`
	DatabasePath                 string `json:"databasePath"`
	VideoDir                     string `json:"videoDir"`
	VRChatLogDir                 string `json:"vrchatLogDir"` // empty follows the current user's default
}

// Decode files over these defaults so null/missing scalars retain their defaults,
// while explicit false and valid zero values survive.
func defaultSettings() Settings {
	return Settings{
		RequestRetentionMode: "unlimited",
		AutoAccelerate:       false, AutoOpenWebUI: true, QueueWithGateway: true, RestoreHostsOnStopOrExit: true,
		VideoStorageMode: "none", VideoBudgetBytes: 0, RetainFavorites: true,
		AggressiveBlockReclamation:   "auto",
		DownloadConcurrencySoftLimit: cacheproxy.VideoRequestSoftLimit, DownloadGrowIntervalMS: int(cacheproxy.DefaultDownloadGrowInterval / time.Millisecond),
		LocalProtectionGraceSeconds: int(cacheproxy.DefaultLocalProtectionGrace / time.Second),
	}
}

// Settings requests update only submitted fields. Decode over the latest saved
// settings under the lifecycle lock; omitted/null scalars retain their values,
// while explicit false, zero and empty strings still reach normal validation.
type settingsRequest struct {
	Settings
	ProxyImportID      string                   `json:"proxyImportID"`
	ConfirmVideoPolicy *videoPolicyConfirmation `json:"confirmVideoPolicy"`
	Password           *string                  `json:"socks5Password"`
}

func decodeSettingsRequest(data []byte, saved Settings) (settingsRequest, error) {
	input := settingsRequest{Settings: saved}
	if err := json.Unmarshal(data, &input); err != nil {
		return input, err
	}
	if input.Password != nil {
		input.Settings.SOCKS5Password = *input.Password
	}
	input.Settings.nodeImportID = input.ProxyImportID
	return input, nil
}

// Relative paths belong to the configuration, independent of the launch directory.
func resolveConfigSettings(s Settings, configPath string) (Settings, error) {
	for _, p := range []*string{&s.DatabasePath, &s.VideoDir, &s.VRChatLogDir} {
		*p = strings.TrimSpace(*p)
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(filepath.Dir(configPath), *p)
		}
	}
	return normalizeSettings(s)
}

// Keep directories inside the portable folder movable; external stores stay absolute.
func storedSettings(s Settings, configPath string) Settings {
	base := filepath.Dir(configPath)
	for _, p := range []*string{&s.DatabasePath, &s.VideoDir, &s.VRChatLogDir} {
		if pathContains(base, *p) {
			if rel, err := filepath.Rel(base, *p); err == nil {
				*p = rel
			}
		}
	}
	return s
}

func normalizeSettings(s Settings) (Settings, error) {
	level, err := applog.ParseLevel(s.LogLevel)
	if err != nil {
		return s, err
	}
	s.LogLevel = level.String()
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
	if s.DownloadConcurrencySoftLimit < 1 || s.DownloadConcurrencySoftLimit > cacheproxy.MaxDownloadConcurrencySoftLimit {
		return s, errors.New("下载并发上限（软）必须为 1～64")
	}
	if s.DownloadGrowIntervalMS < 0 || s.DownloadGrowIntervalMS > int(cacheproxy.MaxDownloadGrowInterval/time.Millisecond) {
		return s, errors.New("并发增长间隔必须为 0～2000 毫秒")
	}
	if s.LocalProtectionGraceSeconds < 0 || s.LocalProtectionGraceSeconds > int(cacheproxy.MaxLocalProtectionGrace/time.Second) {
		return s, errors.New("本地延续保护时长必须为 0～10000 秒")
	}
	if s.RequestRetentionDays < 0 || s.RequestRetentionDays > 36500 {
		return s, errors.New("请求明细保留天数必须为 0～36500")
	}
	if s.RequestRetentionMode != "none" && s.RequestRetentionMode != "limited" && s.RequestRetentionMode != "unlimited" {
		return s, errors.New("请求明细保留选项必须为 none、limited 或 unlimited")
	}
	if s.RequestRetentionMode == "limited" && s.RequestRetentionDays == 0 {
		return s, errors.New("选择「保留指定天数」时，请求明细保留天数必须大于 0")
	}
	if s.VideoBudgetBytes < 0 {
		return s, errors.New("视频存储预算不能为负数")
	}
	if s.VideoStorageMode != "none" && s.VideoStorageMode != "limited" && s.VideoStorageMode != "unlimited" {
		return s, errors.New("视频保留选项必须为 none、limited 或 unlimited")
	}
	if s.VideoStorageMode == "limited" && s.VideoBudgetBytes == 0 {
		return s, errors.New("选择「限制为」时，视频存储预算必须大于 0")
	}
	if s.AggressiveBlockReclamation != "auto" && s.AggressiveBlockReclamation != "on" && s.AggressiveBlockReclamation != "off" {
		return s, errors.New("激进的缓存回收策略必须为 auto、on 或 off")
	}
	for _, path := range []*string{&s.DatabasePath, &s.VideoDir, &s.VRChatLogDir} {
		*path = strings.TrimSpace(*path)
		if *path != "" {
			var err error
			*path, err = filepath.Abs(*path)
			if err != nil {
				return s, err
			}
		}
	}
	return s, nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
