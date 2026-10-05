package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"still-wanna-dance/internal/upstreamrequest"
)

func TestProxyIdentityCorruptionDoesNotPreventNetwork(t *testing.T) {
	for _, mode := range []string{"auto", "socks5"} {
		for _, data := range []string{"", "short", strings.Repeat("x", 33), "directory", "blocked-parent"} {
			t.Run(mode+"/"+data, func(t *testing.T) {
				c := testConsole(t)
				s := Settings{UpstreamMode: mode, SOCKS5Address: "127.0.0.1:1080"}
				oldID, err := c.proxyIdentity(s)
				if err != nil {
					t.Fatal(err)
				}
				path := c.configPath + ".channel-key"
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				switch data {
				case "directory":
					err = os.Mkdir(path, 0700)
				case "blocked-parent":
					err = os.WriteFile(path, []byte("parent"), 0600)
					c.configPath = filepath.Join(path, "config.json")
				default:
					err = os.WriteFile(path, []byte(data), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				_, client, err := c.networkFor(s)
				if err != nil {
					t.Fatal(err)
				}
				pool := client.Transport.(*upstreamrequest.Transport).Pool
				defer pool.Close()
				var id string
				for _, candidate := range pool.Current("https://play.udon.dance") {
					if candidate.Mode == "socks5" {
						id = candidate.ID
					}
				}
				if id == "" || strings.HasSuffix(id, oldID) {
					t.Fatal("proxy missing or retained old identity", id)
				}
				if data == "directory" || data == "blocked-parent" {
					if strings.Contains(id, "stable-v1-") {
						t.Fatal("unpersisted identity can claim samples", id)
					}
					return
				}
				key, err := os.ReadFile(path)
				if err != nil || len(key) != 32 {
					t.Fatal("key not regenerated", err)
				}
				restored, err := (&Console{configPath: c.configPath}).proxyIdentity(s)
				if err != nil || !strings.HasSuffix(id, restored) || restored == "" {
					t.Fatal("new identity not stable across restart", err)
				}
				backups, err := filepath.Glob(path + ".corrupt-*")
				if err != nil || len(backups) != 1 {
					t.Fatalf("missing backup: %v, %v", backups, err)
				}
				b, err := os.ReadFile(backups[0])
				if err != nil || string(b) != data {
					t.Fatal("backup changed", err)
				}
			})
		}
	}
}

func TestProxyIdentityStableAcrossPoolsAndConfigurationSensitive(t *testing.T) {
	c := testConsole(t)
	base := Settings{UpstreamMode: "socks5", SOCKS5Address: "127.0.0.1:1080", SOCKS5Username: "private-user", SOCKS5Password: "private-password"}
	identity := func(owner *Console, s Settings) string {
		t.Helper()
		_, client, err := owner.networkFor(s)
		if err != nil {
			t.Fatal(err)
		}
		pool := client.Transport.(*upstreamrequest.Transport).Pool
		defer pool.Close()
		cs := pool.Current("https://play.udon.dance")
		for _, candidate := range cs {
			if candidate.Mode == "socks5" {
				return candidate.ID
			}
		}
		t.Fatal("missing SOCKS5 channel")
		return ""
	}
	first := identity(c, base)
	for _, field := range []string{"address", "username", "password"} {
		changed := base
		switch field {
		case "address":
			changed.SOCKS5Address = "127.0.0.1:1081"
		case "username":
			changed.SOCKS5Username = "another-user"
		case "password":
			changed.SOCKS5Password = "another-password"
		}
		if identity(c, changed) == first {
			t.Fatal("config change retained identity", field)
		}
	}
	// A fresh owner reads the same on-disk key, regardless of pool sequence.
	restarted := &Console{configPath: c.configPath, dns: c.dns}
	if identity(restarted, base) != first {
		t.Fatal("same proxy changed identity on restart")
	}
	automatic := base
	automatic.UpstreamMode = "auto"
	if identity(restarted, automatic) != first {
		t.Fatal("same proxy path changed identity in auto mode")
	}
	for _, secret := range []string{base.SOCKS5Address, base.SOCKS5Username, base.SOCKS5Password} {
		if strings.Contains(first, secret) {
			t.Fatal("channel ID exposed configuration")
		}
	}
	if err := os.Remove(c.configPath + ".channel-key"); err != nil {
		t.Fatal(err)
	}
	if identity(restarted, base) == first {
		t.Fatal("lost identity key reused old samples")
	}
}
