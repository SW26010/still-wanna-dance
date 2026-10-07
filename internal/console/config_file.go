package console

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const configSchemaVersion = 1

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
		return nil, nil, fmt.Errorf("读取控制台配置：%w", err)
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
	data, err := json.Marshal(c.storedSettings(s))
	if err != nil {
		return err
	}
	// This is the only omitempty setting; clearing it must remove the old value.
	delete(fields, "socks5Password")
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	fields["schemaVersion"], err = json.Marshal(configSchemaVersion)
	if err != nil {
		return err
	}
	data, err = json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.configPath), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.configPath), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), c.configPath)
}
