package console

import (
	"still-wanna-dance/internal/cacheproxy"
	"still-wanna-dance/internal/upstreamrequest"
	"testing"
	"time"
)

func observeTestResources(t *testing.T) {
	t.Helper()
	source := "test/" + t.Name()
	upstreamrequest.Default.ObserveResourceDomains(source, []string{"play.udon.dance", "nya.xin.moe"}, time.Minute)
	t.Cleanup(func() { upstreamrequest.Default.ObserveResourceDomains(source, nil, 0) })
}

// Explicit test-only resource origins; API nodes do not imply these names.
func fixtureCacheConfig() cacheproxy.Config {
	c := cacheproxy.DefaultConfig()
	c.Origins["play.udon.dance"] = "play.udon.dance:443"
	c.Origins["nya.xin.moe"] = "nya.xin.moe:443"
	return c
}
