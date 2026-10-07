package cacheproxy

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Called under the directory lock before initialization or offline deletion.
// Known legacy markers reject the root; their contents are never opened.
func checkStorageLayout(root string) error {
	if _, err := os.Lstat(filepath.Join(root, "stepstash.sqlite")); err == nil {
		return fmt.Errorf("发现旧存储标记 stepstash.sqlite，请使用独立的新存储目录；不迁移或清理旧数据")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查旧存储标记：%w", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "videos"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".mp4") && len(entry.Name()) == 68 {
			if _, err := hex.DecodeString(strings.TrimSuffix(entry.Name(), ".mp4")); err == nil {
				return fmt.Errorf("发现旧组合键视频，请选择新的 MD5 存储目录；不迁移旧数据")
			}
		}
	}
	return nil
}
