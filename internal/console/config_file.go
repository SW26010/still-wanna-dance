package console

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"still-wanna-dance/internal/applog"
	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/workfiles"
)

const configSchemaVersion = apppaths.ConfigVersion

// Callers receive typed settings without decoding JSON or interpreting protected
// payloads. Loading never prepares resources or rewrites the file.
type loadedConfig struct {
	Settings            Settings
	VRCXImportDismissed bool
	Exists              bool
	UpstreamAuto        bool // Legacy disk value; never an executable mode.
}

func loadConfigFile(path string) (loadedConfig, error) {
	data, fields, err := readConfigFile(path)
	if err != nil {
		return loadedConfig{}, err
	}
	loaded := loadedConfig{Settings: defaultSettings(), Exists: data != nil}
	if data != nil {
		if err := json.Unmarshal(data, &loaded.Settings); err != nil {
			return loaded, fmt.Errorf("读取程序配置：%w", err)
		}
		if loaded.Settings.UpstreamMode == "auto" {
			loaded.Settings.UpstreamMode = "proxy"
			loaded.UpstreamAuto = true
		}
		loadProxySecret(fields, &loaded.Settings)
		loadProxyNodes(fields, &loaded.Settings)
		if raw := fields["vrcxImportDismissed"]; raw != nil {
			if err := json.Unmarshal(raw, &loaded.VRCXImportDismissed); err != nil {
				return loaded, fmt.Errorf("读取程序配置 vrcxImportDismissed：%w", err)
			}
		}
	}
	loaded.Settings, err = resolveConfigSettings(loaded.Settings, path)
	return loaded, err
}

// ReadLogLevel bootstraps logging before Console construction can emit events.
// The entrypoint holds the config lease; use the same envelope validation and
// JSON field matching as the full settings load, without opening any resources.
func ReadLogLevel(path string) (applog.Level, error) {
	data, _, err := readConfigFile(path)
	if err != nil {
		return applog.Info, err
	}
	var settings struct {
		LogLevel string `json:"logLevel"`
	}
	if data != nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return applog.Info, fmt.Errorf("读取程序日志级别：%w", err)
		}
	}
	return applog.ParseLevel(settings.LogLevel)
}

// The disk document is separate from the editable settings API. The latter
// cannot set format versions or inject arbitrary fields into the configuration.
func readConfigFile(path string) ([]byte, map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, nil, fmt.Errorf("读取程序配置：%w", err)
	}
	var version int
	if err := json.Unmarshal(fields["schemaVersion"], &version); err != nil {
		return nil, nil, fmt.Errorf("配置缺少有效的 schemaVersion；原文件未修改")
	}
	if version > configSchemaVersion {
		return nil, nil, fmt.Errorf("配置格式版本 %d 较新，请更新程序；原文件未修改", version)
	}
	if version != configSchemaVersion {
		return nil, nil, fmt.Errorf("不支持配置格式版本 %d；原文件未修改", version)
	}
	for key := range fields {
		if strings.EqualFold(key, "storageDir") {
			return nil, nil, fmt.Errorf("配置包含已停用的 storageDir；请分别指定 databasePath 和 videoDir，原文件未修改")
		}
		if strings.EqualFold(key, "proxyNodes") && key != "proxyNodes" {
			return nil, nil, fmt.Errorf("节点快照字段必须为 proxyNodes；原文件未修改")
		}
		if strings.EqualFold(key, "proxySecret") && key != "proxySecret" {
			return nil, nil, fmt.Errorf("代理凭据字段必须为 proxySecret；原文件未修改")
		}
		if strings.EqualFold(key, "socks5Password") {
			return nil, nil, fmt.Errorf("配置包含不支持的明文代理密码；请使用新配置，原文件未修改")
		}
	}
	return data, fields, nil
}

// Both full settings saves and non-editable preferences use this transaction.
// The caller holds the configuration lease and serializes the entire update
// with lifecycleMu (or owns an unpublished Console). Atomic replacement alone
// does not serialize read/modify/write operations.
func updateConfigFile(path string, update func(fields map[string]json.RawMessage, exists bool) error) error {
	_, fields, err := readConfigFile(path)
	if err != nil {
		return err
	}
	exists := fields != nil
	if !exists {
		fields = make(map[string]json.RawMessage)
	}
	if err := update(fields, exists); err != nil {
		return err
	}
	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	return workfiles.Write(path, data)
}

// Re-read the disk envelope so a future file is never overwritten and unknown
// fields from this format version survive ordinary settings saves.
func saveConfigSettings(path string, s Settings) error {
	return updateConfigFile(path, func(fields map[string]json.RawMessage, _ bool) error {
		secret, err := storedProxySecret(s)
		if err != nil {
			return err
		}
		nodes, err := storedProxyNodes(s)
		if err != nil {
			return err
		}
		stored := storedSettings(s, path)
		stored.SOCKS5Password = ""
		data, err := json.Marshal(stored)
		if err != nil {
			return err
		}
		var settings map[string]json.RawMessage
		if err := json.Unmarshal(data, &settings); err != nil {
			return err
		}
		// encoding/json accepts case-insensitive struct field names. Remove those
		// aliases as well, or they could override a canonical value on the next read.
		// The password is the only omitempty field and may be absent when cleared.
		for key := range fields {
			if strings.EqualFold(key, "downloadUpstream") || strings.EqualFold(key, "socks5Password") || strings.EqualFold(key, "proxySecret") || strings.EqualFold(key, "proxyNodes") {
				delete(fields, key)
				continue
			}
			for name := range settings {
				if strings.EqualFold(key, name) {
					delete(fields, key)
					break
				}
			}
		}
		for name, value := range settings {
			fields[name] = value
		}
		if secret != nil {
			fields["proxySecret"] = secret
		}
		if nodes != nil {
			fields["proxyNodes"] = nodes
		}
		fields["schemaVersion"], err = json.Marshal(configSchemaVersion)
		if err != nil {
			return err
		}
		return nil
	})
}

func saveConfigVRCXDismissal(path string) error {
	return updateConfigFile(path, func(fields map[string]json.RawMessage, exists bool) error {
		if !exists {
			return os.ErrNotExist
		}
		fields["vrcxImportDismissed"] = json.RawMessage("true")
		return nil
	})
}

// Persist only the retired mode, preserving encrypted credentials, nodes and
// unknown JSON values without decoding or re-encrypting them. Startup owns the
// config lease.
func migrateAutoUpstreamMode(path string) error {
	return updateConfigFile(path, func(fields map[string]json.RawMessage, exists bool) error {
		if !exists {
			return os.ErrNotExist
		}
		for key := range fields {
			if strings.EqualFold(key, "upstreamMode") {
				delete(fields, key)
			}
		}
		fields["upstreamMode"] = json.RawMessage(`"proxy"`)
		return nil
	})
}
