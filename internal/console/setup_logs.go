package console

import (
	"errors"
	"io"
	"os"

	"still-wanna-dance/internal/vrclog"
)

type setupLogs struct {
	State     string `json:"state"`
	Message   string `json:"message"`
	Directory string `json:"directory"`
	Path      string `json:"path,omitempty"`
}

func (c *Console) inspectSetupLogs() setupLogs {
	c.mu.Lock()
	dir := c.settings.LogDir // Active location; neither drafts nor pending settings.
	c.mu.Unlock()
	return inspectSetupLogDirectory(dir)
}

// A bounded read probe only. No log contents leave this function, and no queue,
// watcher, directory or setting is created by inspecting the current location.
func inspectSetupLogDirectory(dir string) setupLogs {
	result := setupLogs{State: "unknown", Directory: dir}
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		result.State, result.Message = "failed", "VRC 日志位置不是目录。"
		return result
	}
	path, err := vrclog.LatestPath(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			result.Message = "未找到 VRC 日志目录；启动过 VRChat 后可重新检测。"
		} else {
			result.State, result.Message = "failed", "无法读取 VRC 日志目录："+err.Error()
		}
		return result
	}
	if path == "" {
		result.Message = "未找到 VRC 日志文件；启动过 VRChat 后可重新检测。"
		return result
	}
	result.Path = path
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
		var info os.FileInfo
		info, err = file.Stat()
		if err == nil && !info.Mode().IsRegular() {
			err = errors.New("日志路径不是普通文件")
		}
		if err == nil {
			var probe [1]byte
			_, err = file.Read(probe[:])
			if errors.Is(err, io.EOF) {
				err = nil // An empty current log is still discoverable and readable.
			}
		}
	}
	if err != nil {
		result.State, result.Message = "failed", "VRC 日志无法读取："+err.Error()
		return result
	}
	result.State, result.Message = "ok", "已检测到可读取的 VRC 日志。"
	return result
}
