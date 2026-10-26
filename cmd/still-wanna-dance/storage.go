package main

import (
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"still-wanna-dance/internal/cacheproxy"
)

// Unlike saved desktop settings, command-line arguments are the entire
// retention decision for this run. Require it explicitly for existing videos
// so the default zero budget cannot evict a newly selected collection.
func checkVideoPolicy(path string, explicitPolicy bool) error {
	dir, err := cacheproxy.CacheDirectory(path)
	if err != nil {
		return err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if explicitPolicy {
		return nil
	}
	for _, file := range files {
		name := file.Name()
		if !strings.HasSuffix(name, ".mp4") || !file.Type().IsRegular() {
			continue
		}
		key := strings.TrimSuffix(name, ".mp4")
		md5, err := hex.DecodeString(key)
		if err == nil && len(md5) == 16 && key == strings.ToLower(key) {
			return errors.New("视频目录已有完整视频；请显式指定 -max-cache-bytes 或 -cache-unlimited，以确认本次按当前数据库的收藏和保留策略管理这些文件")
		}
	}
	return nil
}
