package cacheproxy

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Reject unsupported storage before startup creates, cleans or rewrites data.
func checkStorageFormat(root string) error {
	db, err := openScanDatabase(root)
	if err != nil {
		return err
	}
	if db != nil {
		defer db.Close()
		var version, tables int
		if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return err
		}
		if (tables > 0 && version != 3) || (version != 0 && version != 3) {
			return fmt.Errorf("旧存储格式不兼容歌曲目录格式，请选择新的存储目录；旧数据未修改")
		}
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
