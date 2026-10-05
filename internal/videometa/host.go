package videometa

import (
	"net"
	"strings"
)

// ValidHost validates a resource DNS name, not membership in an upstream set.
// Membership must come from validated API responses, never client Host input.
func ValidHost(host string) bool {
	if host != strings.ToLower(host) || len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".invalid"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
