package console

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	"still-wanna-dance/internal/proxyengine/mihomo"
)

const unavailableProxyNodes = "Mihomo 配置无效或无法读取"

type proxyImportDraft struct {
	Snapshot     string
	BaseSnapshot string
	BaseSecret   string
	Expires      time.Time
}

type proxyImportResult struct {
	mihomo.Summary
	ID string `json:"id,omitempty"`
}

type proxyNodeStatus struct {
	Saved       int    `json:"saved"`
	Loaded      int    `json:"loaded"`
	Dashboard   bool   `json:"dashboard"`
	Error       string `json:"error,omitempty"`
	ActiveError string `json:"activeError,omitempty"`
}

func validateProxyNodeSelection(s Settings) error {
	if s.ProxyBackend == "mihomo" && s.UpstreamMode != "direct" && s.nodeSnapshot == "" && s.nodeSecretError == "" {
		return invalidSetting(errors.New("请先导入有效的 Mihomo 配置"), "proxyImportText")
	}
	return nil
}

func configurationNodeCount(snapshot string) int {
	summary, _ := mihomo.Inspect([]byte(snapshot))
	return summary.Nodes
}

func loadProxyNodes(fields map[string]json.RawMessage, s *Settings) {
	raw := fields["proxyNodes"]
	if len(raw) == 0 {
		return
	}
	s.nodeSecret, s.nodeSecretError = string(raw), unavailableProxyNodes
	plain, err := decodeProtectedEnvelope(raw)
	if err != nil {
		return
	}
	defer clear(plain)
	if _, err := mihomo.Inspect(plain); err != nil {
		return
	}
	s.nodeSnapshot, s.nodeSecret, s.nodeSecretError = string(plain), "", ""
}

func storedProxyNodes(s Settings) (json.RawMessage, error) {
	if s.nodeSecretError != "" {
		if s.nodeSecret == "" {
			return nil, errors.New(unavailableProxyNodes)
		}
		return json.RawMessage(s.nodeSecret), nil
	}
	if s.nodeSnapshot == "" {
		return nil, nil
	}
	plain := []byte(s.nodeSnapshot)
	defer clear(plain)
	return encodeProtectedEnvelope(plain)
}

// Import only stages a bounded, expiring in-memory draft. Saving settings
// explicitly commits it; preview never activates or persists it.
func (c *Console) importProxyNodes(ctx context.Context, data []byte) (proxyImportResult, error) {
	c.mu.Lock()
	base := c.savedSettings
	allowed := !c.closing && c.terms.valid()
	c.mu.Unlock()
	if !allowed {
		return proxyImportResult{}, rejectRequest(errors.New("请先同意条款，并等待应用就绪"))
	}
	parsed, err := mihomo.Validate(ctx, data, filepath.Join(c.files().StateDir, "mihomo"))
	result := proxyImportResult{Summary: parsed}
	if err != nil {
		return result, invalidSetting(err, "proxyImportText")
	}
	id := make([]byte, 24)
	if _, err = rand.Read(id); err != nil {
		return result, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return result, rejectRequest(errors.New("程序正在退出"))
	}
	if c.proxyImports == nil {
		c.proxyImports = make(map[string]proxyImportDraft)
	}
	for key, draft := range c.proxyImports {
		if time.Now().After(draft.Expires) {
			delete(c.proxyImports, key)
		}
	}
	if len(c.proxyImports) >= 8 {
		return result, rejectRequest(errors.New("待保存的配置草稿过多，请保存或丢弃已有草稿后再导入"))
	}
	result.ID = hex.EncodeToString(id)
	c.proxyImports[result.ID] = proxyImportDraft{string(data), base.nodeSnapshot, base.nodeSecret, time.Now().Add(2 * time.Hour)}
	return result, nil
}

func (c *Console) mergeProxyNodes(s Settings, old Settings) (Settings, error) {
	if s.nodeImportID == "" {
		s.nodeSnapshot, s.nodeSecret, s.nodeSecretError = old.nodeSnapshot, old.nodeSecret, old.nodeSecretError
		return s, nil
	}
	c.mu.Lock()
	draft, exists := c.proxyImports[s.nodeImportID]
	c.mu.Unlock()
	if !exists || time.Now().After(draft.Expires) {
		return s, invalidSetting(errors.New("配置导入草稿已过期，请重新导入"), "proxyImportText")
	}
	if old.nodeSnapshot != draft.BaseSnapshot || old.nodeSecret != draft.BaseSecret {
		return s, invalidSetting(errors.New("已保存配置已在别处改变，请重新导入"), "proxyImportText")
	}
	s.nodeSnapshot, s.nodeSecret, s.nodeSecretError = draft.Snapshot, "", ""
	return s, nil
}

// Caller holds mu. Only counts and fixed errors are returned, never user names,
// addresses, credentials, URIs, or encrypted envelopes.
func (c *Console) proxyNodesStatusLocked() proxyNodeStatus {
	status := proxyNodeStatus{Saved: c.savedNodeCount, Loaded: c.loadedNodeCount, Error: c.savedSettings.nodeSecretError, ActiveError: c.settings.nodeSecretError}
	if c.client != nil {
		if tr, ok := c.client.Transport.(*mihomoTransport); ok {
			status.Dashboard = tr.core != nil
			if status.ActiveError == "" {
				status.ActiveError = tr.startupError
			}
			if tr.core != nil && !tr.core.SelectionCacheAvailable() {
				if status.ActiveError != "" {
					status.ActiveError += "；"
				}
				status.ActiveError += "选择缓存不可用；代理仍可使用，本次的节点选择不会保存，请检查数据目录后重启"
			}
		}
	}
	return status
}
