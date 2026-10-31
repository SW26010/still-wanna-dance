package upstreamrequest

import (
	"encoding/hex"
	"strings"
)

// PersistentProxyID identifies a versioned, keyed configuration fingerprint.
// Numeric pool sequence identities are never safe to restore across processes.
func PersistentProxyID(id string) bool {
	if !strings.HasPrefix(id, "stable-v1-") {
		return false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(id, "stable-v1-"))
	return err == nil && len(b) == 32
}
