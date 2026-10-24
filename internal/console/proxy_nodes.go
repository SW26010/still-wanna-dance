package console

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"still-wanna-dance/internal/proxyengine/mihomo"
)

const unavailableProxyNodes = "已保存的节点快照无法读取，请重新导入并保存、重启；原密文仍保留"
const proxyNodeDocumentVersion = 2

// The whole node document is protected, including endpoints and display data.
// Immutable JSON strings keep Settings comparable without exposing node data
// through its public API. Protection and identity have independent purposes.
type proxyNodeDocument struct {
	Version int              `json:"version"`
	Nodes   []map[string]any `json:"nodes"`
}

type proxyImportDraft struct {
	Snapshot     string
	BaseSnapshot string
	BaseSecret   string
	Expires      time.Time
}

type proxyImportResult struct {
	mihomo.ImportResult
	ID string `json:"id,omitempty"`
}

type proxyNodeStatus struct {
	Saved       int    `json:"saved"`
	Loaded      int    `json:"loaded"`
	Active      int    `json:"active"`
	Error       string `json:"error,omitempty"`
	ActiveError string `json:"activeError,omitempty"`
}

func validateProxyNodeSelection(s Settings) error {
	if s.ProxyBackend == "mihomo" && s.UpstreamMode != "direct" && s.nodeSnapshot == "" && s.nodeSecretError == "" {
		return errors.New("请先导入至少一个受支持的 Mihomo 节点")
	}
	return nil
}

func nodeDocument(snapshot string) proxyNodeDocument {
	var doc proxyNodeDocument
	_ = json.Unmarshal([]byte(snapshot), &doc)
	return doc
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
	var doc proxyNodeDocument
	if len(plain) > mihomo.MaxImportBytes || json.Unmarshal(plain, &doc) != nil || len(doc.Nodes) == 0 || len(doc.Nodes) > mihomo.MaxImportNodes {
		return
	}
	if doc.Version != proxyNodeDocumentVersion {
		s.nodeSecretError = "节点快照版本不兼容，请从原始内容重新导入并保存、重启；原密文仍保留"
		return
	}
	for _, p := range doc.Nodes {
		if _, err := mihomo.Normalize(p); err != nil {
			return
		}
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

func nodeIdentity(key []byte, p map[string]any) string {
	data, _ := json.Marshal(p)
	defer clear(data)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("swd-mihomo-node-v2\x00"))
	mac.Write(data)
	return "stable-v1-" + hex.EncodeToString(mac.Sum(nil))
}

// Import only stages a bounded, expiring in-memory draft. Saving settings or
// the wizard explicitly commits it; preview never activates or persists it.
func (c *Console) importProxyNodes(data []byte) (proxyImportResult, error) {
	c.mu.Lock()
	base := c.savedSettings
	allowed := !c.closing && c.terms.valid()
	c.mu.Unlock()
	if !allowed {
		return proxyImportResult{}, errors.New("请先同意条款，并等待应用就绪")
	}
	parsed, err := mihomo.Import(data)
	result := proxyImportResult{ImportResult: parsed}
	if err != nil || parsed.Accepted == 0 {
		return result, err
	}
	snapshot, err := json.Marshal(proxyNodeDocument{Version: proxyNodeDocumentVersion, Nodes: parsed.Nodes})
	if err != nil || len(snapshot) > mihomo.MaxImportBytes {
		return result, errors.New("节点快照超过 2 MiB")
	}
	id := make([]byte, 24)
	if _, err = rand.Read(id); err != nil {
		return result, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return result, errors.New("程序正在退出")
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
		return result, errors.New("待保存的节点草稿过多，请保存或丢弃已有草稿后再导入")
	}
	result.ID = hex.EncodeToString(id)
	c.proxyImports[result.ID] = proxyImportDraft{string(snapshot), base.nodeSnapshot, base.nodeSecret, time.Now().Add(2 * time.Hour)}
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
		return s, errors.New("节点导入草稿已过期，请重新导入")
	}
	if old.nodeSnapshot != draft.BaseSnapshot || old.nodeSecret != draft.BaseSecret {
		return s, errors.New("已保存节点已在别处改变，请重新导入")
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
			status.Active = len(tr.proxies)
			if status.ActiveError == "" {
				status.ActiveError = tr.identityWarning
			}
		}
	}
	return status
}
