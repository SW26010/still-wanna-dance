package cacheproxy

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Reject unsupported storage before startup creates, cleans or rewrites data.
func checkStorageLayout(root string) error {
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
