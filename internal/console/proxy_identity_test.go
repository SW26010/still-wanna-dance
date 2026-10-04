package console

import (
	"os"
	"strings"
	"testing"

	"still-wanna-dance/internal/upstreamrequest"
)

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
