package mihomo

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/profile/cachefile"
)

func TestNativeSelectionCacheUsesNamesWithoutGeneratingEndpoints(t *testing.T) {
	if !runtimeProcess(t) {
		return
	}
	var enabled atomic.Bool
	enabled.Store(true)
	named := groupExit(t, "订阅显示名.example:9443", "", &enabled)
	unnamed := groupExit(t, "", "", &enabled)
	delete(unnamed, "name")
	unnamed["username"] = "fixture-user"
	unnamed["password"] = "fixture-only-secret"
	configs := []Config{
		{ID: "stable-v1-" + strings.Repeat("a", 64), Parameters: named},
		{ID: "stable-v1-" + strings.Repeat("b", 64), Parameters: unnamed},
	}
	home := t.TempDir()
	r, err := NewRuntime(configs, RuntimeOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	names := runtimeProxyNames(t, r)
	address := fmt.Sprintf("%s:%v", unnamed["server"], unnamed["port"])
	if !strings.HasPrefix(names[1], "订阅显示名.example:9443 ") || !strings.HasPrefix(names[2], "节点 2 ") || strings.Contains(names[2], address) {
		t.Fatal("unexpected displayed node names", names)
	}
	selectRuntimeProxy(t, r, names[1])
	if cachefile.Cache().SelectedMap()[ProxyGroup] != names[1] {
		t.Fatal("native cache did not retain the supplied display name")
	}
	selectRuntimeProxy(t, r, names[2])
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	cache, err := os.ReadFile(filepath.Join(home, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(cache, []byte(names[2])) {
		t.Fatal("native cache did not save the selected display name")
	}
	for _, parameter := range []string{address, "fixture-user", "fixture-only-secret"} {
		if bytes.Contains(cache, []byte(parameter)) {
			t.Fatal("connection parameters were written to the selection cache")
		}
	}
}

func TestSelectionCacheFailureDoesNotBlockProxy(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		for _, disabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("invalid=%t/disabled=%t", invalid, disabled), func(t *testing.T) {
				if !runtimeProcess(t) {
					return
				}
				home := t.TempDir()
				cachePath := filepath.Join(home, "cache.db")
				marker := []byte("unrelated fixture must survive")
				keep := filepath.Join(home, "keep.txt")
				if !invalid {
					// A directory makes the native cache open fail on every platform.
					if err := os.Mkdir(cachePath, 0700); err != nil {
						t.Fatal(err)
					}
					keep = filepath.Join(cachePath, "keep.txt")
				} else if err := os.WriteFile(cachePath, bytes.Repeat([]byte("invalid cache fixture\n"), 1024), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(keep, marker, 0600); err != nil {
					t.Fatal(err)
				}
				var enabled atomic.Bool
				enabled.Store(true)
				configs := []Config{{ID: "stable-v1-" + strings.Repeat("a", 64), Parameters: groupExit(t, "cache-independent-exit", "", &enabled)}}
				r, err := NewRuntime(configs, RuntimeOptions{Home: home, DisableSelectionPersistence: disabled})
				if err != nil {
					t.Fatal("selection cache blocked proxy startup", err)
				}
				t.Cleanup(func() { _ = r.Close() })
				if available := cachefile.Cache().DB != nil; available != invalid {
					t.Fatalf("native cache available=%t, want %t", available, invalid)
				}
				if r.SelectionCacheAvailable() != invalid {
					t.Fatal("runtime cache status does not reflect native availability")
				}
				names := runtimeProxyNames(t, r)
				selectRuntimeProxy(t, r, names[1])
				transport := &http.Transport{DialContext: r.DialContext}
				defer transport.CloseIdleConnections()
				resp, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get("http://business.invalid/")
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || string(body) != "cache-independent-exit" {
					t.Fatal("proxy request failed without selection persistence", err)
				}
				selection := cachefile.Cache().SelectedMap()[ProxyGroup]
				if invalid && !disabled && selection != names[1] || (!invalid || disabled) && selection != "" {
					t.Fatal("unexpected persisted selection", selection)
				}
				transport.CloseIdleConnections()
				if err := r.Close(); err != nil {
					t.Fatal("cache failure affected shutdown", err)
				}
				if got, err := os.ReadFile(keep); err != nil || !bytes.Equal(got, marker) {
					t.Fatal("native cache handling removed unrelated material", err)
				}
			})
		}
	}
}
