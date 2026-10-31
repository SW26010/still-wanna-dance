package console

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestChannelIdentityConcurrentConsumersShareIndependentCopies(t *testing.T) {
	c := &Console{configPath: filepath.Join(t.TempDir(), "config.json")}
	type result struct {
		key        string
		persistent bool
		err        error
	}
	results := make(chan result, 24)
	var wg sync.WaitGroup
	for range cap(results) {
		wg.Go(func() {
			key, persistent, err := c.channelIdentityKey()
			results <- result{string(key), persistent, err}
			clear(key)
		})
	}
	wg.Wait()
	close(results)
	onDisk, err := os.ReadFile(c.files().ChannelKey)
	if err != nil {
		t.Fatal(err)
	}
	for r := range results {
		if r.err != nil || !r.persistent || len(r.key) != 32 || r.key != string(onDisk) {
			t.Fatal("consumers disagreed or cleared a shared key", r.persistent, r.err)
		}
	}
}

func TestChannelIdentityFailedPreparationRemainsTemporaryUntilRestart(t *testing.T) {
	c := &Console{configPath: filepath.Join(t.TempDir(), "config.json")}
	path := c.files().ChannelKey
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	first, persistent, err := c.channelIdentityKey()
	if err != nil || persistent || len(first) != 32 {
		t.Fatal(persistent, err)
	}
	defer clear(first)
	// Even if the file subsequently becomes readable (or a failed write left
	// all bytes behind), this instance must not promote its prepared result.
	repaired := strings.Repeat("a", 32)
	if err := os.WriteFile(path, []byte(repaired), 0600); err != nil {
		t.Fatal(err)
	}
	next, persistent, err := c.channelIdentityKey()
	defer clear(next)
	if err != nil || persistent || string(next) != string(first) {
		t.Fatal("identity changed after failed preparation", persistent, err)
	}
	restarted := &Console{configPath: c.configPath}
	key, persistent, err := restarted.channelIdentityKey()
	defer clear(key)
	if err != nil || !persistent || string(key) != repaired {
		t.Fatal("restart did not read repaired key", persistent, err)
	}
}

// File-layout checks exercise the identity still used by native Mihomo nodes.
func testNodeIdentity(c *Console) (string, error) {
	key, _, err := c.channelIdentityKey()
	defer clear(key)
	return nodeIdentity(key, map[string]any{"type": "ss", "server": "localhost", "port": float64(1080), "cipher": "aes-128-gcm", "password": "fixture"}), err
}

func TestExternalProxyDoesNotReadOrCreateChannelIdentity(t *testing.T) {
	c := testConsole(t)
	path := c.files().ChannelKey
	s := Settings{UpstreamMode: "proxy", SOCKS5Address: "127.0.0.1:1080"}
	for _, corrupt := range []bool{false, true} {
		if corrupt {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		_, client, err := c.networkFor(s)
		if err != nil {
			t.Fatal(err)
		}
		client.CloseIdleConnections()
		if _, ok := client.Transport.(upstreamrequest.Candidates); ok {
			t.Fatal("proxy exposes direct channel candidates")
		}
		key, err := os.ReadFile(path)
		if !corrupt && !os.IsNotExist(err) {
			t.Fatal("proxy created channel identity", err)
		}
		if corrupt && (err != nil || string(key) != "broken") {
			t.Fatal("proxy changed identity file", err)
		}
	}
}
