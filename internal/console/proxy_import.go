package console

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"still-wanna-dance/internal/proxyengine/mihomo"
)

// Only configuration bytes enter the draft. The acquisition URL is transient
// and is never included in the saved configuration, diagnostics or status.
func proxyConfigurationContent(ctx context.Context, source string, input []byte) ([]byte, error) {
	switch source {
	case "", "config":
		if len(input) > mihomo.MaxImportBytes {
			return nil, errors.New("配置文件超过 2 MiB")
		}
		return input, nil
	case "base64":
		for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			data, err := encoding.DecodeString(strings.TrimSpace(string(input)))
			if err == nil && len(data) <= mihomo.MaxImportBytes {
				return data, nil
			}
			clear(data)
		}
		return nil, errors.New("Base64 配置无效或超过 2 MiB")
	case "url":
		address := strings.TrimSpace(string(input))
		u, err := url.Parse(address)
		if err != nil || len(address) > 8192 || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" {
			return nil, errors.New("请输入有效的 HTTP 或 HTTPS 配置 URL")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, errors.New("配置 URL 无效")
		}
		transport := &http.Transport{Proxy: nil}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
		response, err := client.Do(req)
		if err != nil {
			return nil, errors.New("配置文件下载失败")
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, errors.New("配置文件下载失败：服务器返回错误状态")
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, mihomo.MaxImportBytes+1))
		if err != nil || len(data) > mihomo.MaxImportBytes {
			clear(data)
			return nil, errors.New("配置文件读取失败或超过 2 MiB")
		}
		return data, nil
	default:
		return nil, errors.New("配置来源无效")
	}
}

func (c *Console) serveProxyImport(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	allowed := !c.closing && c.terms.valid()
	c.mu.Unlock()
	if !allowed {
		preserveResponseFault(w, rejectRequest(errors.New("请先同意条款，并等待应用就绪")))
		http.Error(w, "请先同意条款，并等待应用就绪", http.StatusBadRequest)
		return
	}
	// Base64 expands a bounded 2 MiB document by one third.
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
	input, err := io.ReadAll(r.Body)
	defer clear(input)
	if err != nil {
		writeSettingsValidation(w, invalidSetting(errors.New("配置内容读取失败或超出限制"), "proxyImportText"))
		return
	}
	data, err := proxyConfigurationContent(r.Context(), r.Header.Get("X-SWD-Import-Source"), input)
	defer clear(data)
	if err != nil {
		writeSettingsValidation(w, invalidSetting(err, "proxyImportText"))
		return
	}
	name := "Mihomo 配置"
	switch r.Header.Get("X-SWD-Import-Source") {
	case "url":
		name = "URL 配置"
	case "base64":
		name = "Base64 配置"
	default:
		if _, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
			name = params["filename"]
		}
	}
	result, err := c.importProxyNodes(r.Context(), data, name)
	if err != nil {
		if writeSettingsValidation(w, err) {
			return
		}
		preserveResponseFault(w, err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}
