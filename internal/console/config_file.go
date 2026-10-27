package console

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"still-wanna-dance/internal/apppaths"
	"still-wanna-dance/internal/workfiles"
)

const configSchemaVersion = apppaths.ConfigVersion

// This controls automatic browser opening for a new console process. Explicit
// tray/open and second-instance activation remain available independently.
// Missing or invalid consent must expose the existing confirmation flow.
func (c *Console) OpenWebUIOnLaunch() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.terms.valid() || c.settings.AutoOpenWebUI
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

// Caller serializes writes with lifecycleMu (or owns an unpublished Console).
// Re-read the disk envelope so a future file is never overwritten and unknown
// fields from this format version survive ordinary settings saves.
func (c *Console) writeConfigFile(s Settings) error {
	_, fields, err := readConfigFile(c.configPath)
	if err != nil {
		return err
	}
	if fields == nil {
		fields = make(map[string]json.RawMessage)
	}
	secret, err := storedProxySecret(s)
	if err != nil {
		return err
	}
	nodes, err := storedProxyNodes(s)
	if err != nil {
		return err
	}
	stored := c.storedSettings(s)
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
	data, err = json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	return workfiles.Write(c.configPath, data)
}
