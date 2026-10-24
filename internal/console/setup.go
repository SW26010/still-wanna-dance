package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"still-wanna-dance/internal/workfiles"
)

// SetupProgress is navigation metadata, never configuration or proof of consent.
// Keep it in the configuration's existing state directory so portable copies and
// custom configurations obey the same ownership rules as other UI snapshots.
type SetupProgress struct {
	SchemaVersion int    `json:"schemaVersion"`
	Step          string `json:"step"`
	State         string `json:"state"`
	FurthestStep  string `json:"furthestStep,omitempty"`
}

var setupSteps = []string{"welcome", "terms", "storage", "import", "direct", "proxy", "environment", "finish"}

func (p SetupProgress) valid() bool {
	if p.SchemaVersion != 1 {
		return false
	}
	if slices.Index(setupSteps, p.Step) < 0 {
		return false
	}
	// Older progress records omit this navigation-only checkpoint.
	if p.FurthestStep != "" && slices.Index(setupSteps, p.FurthestStep) < 0 {
		return false
	}
	return p.State == "active" || p.State == "completed" || p.State == "skipped"
}

func (c *Console) setupProgress() (SetupProgress, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := SetupProgress{SchemaVersion: 1, Step: "welcome", State: "active"}
	f, err := os.Open(c.files().State("setup"))
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, fmt.Errorf("读取引导进度：%w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return p, fmt.Errorf("读取引导进度：%w", err)
	}
	if len(b) > 4096 || json.Unmarshal(b, &p) != nil || !p.valid() {
		return SetupProgress{}, errors.New("引导进度格式不受支持；仍可使用 WebUI 或重新开始引导")
	}
	return p, nil
}

func (c *Console) saveSetupProgress(p SetupProgress) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return errors.New("程序正在退出")
	}
	if !c.terms.valid() {
		return errors.New("请先阅读并同意使用条款")
	}
	if !p.valid() {
		return errors.New("无效的引导进度")
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	path := c.files().State("setup")
	if previous, err := os.ReadFile(path); err == nil && string(previous) == string(data) {
		return nil
	}
	return workfiles.Write(path, data)
}
