package upstreamnet

import "testing"

func TestSOCKS5AddressSyntax(t *testing.T) {
	for _, address := range []string{"127.0.0.1:7891", "localhost:1", "proxy.example:65535", "[::1]:7891", "[fe80::1%eth0]:7891"} {
		if err := ValidateSOCKS5Address(address); err != nil {
			t.Errorf("valid address %q: %v", address, err)
		}
	}
	for _, address := range []string{"", "anything", "127.0.0.1", "127.0.0.1:abc", "127.0.0.1:0", "127.0.0.1:65536", "socks5://127.0.0.1:7891", "%%%:7891", "[garbage]:7891", "999.999.999.999:7891", "127.0.0.1:+7891", "bad..host:1", "-proxy:1", "proxy_:1", "::1:7891"} {
		if err := ValidateSOCKS5Address(address); err == nil {
			t.Errorf("invalid address accepted: %q", address)
		}
	}
}
